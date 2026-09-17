package notify

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

const (
	// queueCapacity bounds Emit's buffer; Emit never blocks, it drops.
	queueCapacity = 256
	// mergeThreshold: once more than this many events are pending, events
	// with the same Category.Kind are merged into the newest one.
	mergeThreshold = 50
	// minSendGap is the throttle: at most one Send per bus per second.
	minSendGap = time.Second
	// maxRetries after a failed Send, with backoff below.
	maxRetries = 3
	// heldCapacity bounds the quiet-hours/mute backlog (oldest dropped); the
	// digest still reports the true count.
	heldCapacity = 100
	// closeGrace is how long Close waits for queued events to be delivered
	// before cancelling in-flight sends.
	closeGrace = 5 * time.Second
)

// backoff between attempts (attempt n waits backoff[n]) unless the error
// carries a RetryAfter.
var backoff = [maxRetries]time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// alwaysPass events skip quiet hours and mute: they are the user's chance
// to cancel something. The digest is on the list so the summary of one
// quiet window can never be swallowed by the next.
var alwaysPass = map[string]bool{
	"remote.grace_scheduled": true,
	"remote.force":           true,
	"system.digest":          true,
}

// retryAfterer is implemented by rate-limit errors (telegram's 429) that
// say how long to wait before the next attempt.
type retryAfterer interface {
	RetryAfter() time.Duration
}

var (
	pkgLogMu sync.RWMutex
	pkgLog   func(string, ...any)
)

// SetLogger sets the package-wide logger used by buses whose Options.Log
// is nil. The service passes its logMsg here.
func SetLogger(fn func(string, ...any)) {
	pkgLogMu.Lock()
	pkgLog = fn
	pkgLogMu.Unlock()
}

// Options configures a Bus. Only Sink is needed for delivery; the nil
// defaults are DefaultConfig, no quiet hours, time.Now and real timers.
type Options struct {
	Sink   Sink
	Config func() Config     // hot reload: called for every event
	Quiet  func() QuietHours // hot reload: called for every event
	Now    func() time.Time  // test clock; nil means time.Now
	// Sleep replaces the throttle/backoff waits; nil means a real,
	// Close-aware sleep. Tests pass a fake that advances Now.
	Sleep func(time.Duration)
	// After replaces time.After for the worker's deadline timer
	// (aggregation windows, quiet-hours end, mute end); nil means
	// time.After. Tests pass a fake driven by the same clock as Now.
	After func(time.Duration) <-chan time.Time
	Log   func(string, ...any)
}

// Bus is the single in-process event pipeline: Emit → filter → aggregate
// → quiet/mute → throttle → Sink.Send (with retries). One worker goroutine
// drains the queue, so events reach the sink in emit order. The same
// goroutine owns the deadline timer that closes aggregation windows and
// releases the quiet-hours/mute backlog as a digest.
type Bus struct {
	opts    Options
	queue   chan Event
	wake    chan struct{} // pokes the worker to re-evaluate its deadlines
	closing chan struct{} // closed by Close: finish the queue, then exit
	done    chan struct{} // closed when the worker has exited
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once

	// taps receive every event before the pipeline sees it; see Tap.
	tapMu sync.RWMutex
	taps  []func(Event)

	mu         sync.Mutex
	mutedUntil time.Time
	muteFrom   time.Time
	held       []Event // set aside by quiet hours/mute, oldest first (≤ heldCapacity)
	heldTotal  int     // everything held since the last flush, dropped ones included
	heldSince  time.Time
	heldQuiet  QuietHours // the quiet window that was active while holding ...
	heldByQ    bool       // ... when set; otherwise only a mute was holding

	// worker-only state
	lastSend time.Time
	windows  map[string]*window // open aggregation windows by kind+source
	timer    <-chan time.Time   // armed deadline timer, nil when idle
	timerAt  time.Time
}

// New starts a bus and its worker goroutine.
func New(opts Options) *Bus {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bus{
		opts:    opts,
		queue:   make(chan Event, queueCapacity),
		wake:    make(chan struct{}, 1),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		windows: make(map[string]*window),
	}
	go b.run()
	return b
}

// Tap registers fn to receive every emitted event *before* the pipeline —
// no category filter, no aggregation, no quiet hours, no throttle. It
// exists for consumers that mirror device state rather than notify a
// person (the SmartThings push sink, edge-driver doc §4.5).
//
// fn runs on the emitting goroutine, so it must return quickly; the one
// deliberate exception is power.stopping, which the push sink delivers
// synchronously to hold the stop back for up to 1.5s.
func (b *Bus) Tap(fn func(Event)) {
	if b == nil || fn == nil {
		return
	}
	b.tapMu.Lock()
	b.taps = append(b.taps, fn)
	b.tapMu.Unlock()
}

// TapOnly hands ev to the taps without queueing it for the pipeline: pure
// device state ("the screen is off now") is not a notification and must
// never reach a notification channel. A zero At is set to now.
func (b *Bus) TapOnly(ev Event) {
	if b == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = b.now()
	}
	b.runTaps(ev)
}

// runTaps calls every registered tap with ev.
func (b *Bus) runTaps(ev Event) {
	b.tapMu.RLock()
	taps := b.taps
	b.tapMu.RUnlock()
	for _, fn := range taps {
		fn(ev)
	}
}

// Emit queues ev without blocking. When the queue is full or the bus is
// closed the event is dropped and logged. A zero At is set to now. Taps
// see the event first, and see it even when the pipeline drops it.
func (b *Bus) Emit(ev Event) {
	if b == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = b.now()
	}
	b.runTaps(ev)
	select {
	case <-b.closing:
		b.logf("notify: bus closed, dropping %s", ev.Key())
		return
	default:
	}
	select {
	case b.queue <- ev:
	default:
		b.logf("notify: queue full, dropping %s", ev.Key())
	}
}

// Mute holds every non-always-pass event for d (the /mute command). It
// shares the quiet-hours backlog: when the mute ends — by timeout or
// Unmute — the backlog goes out as one system.digest (or is dropped when
// QuietHours.Digest is off). Calling Mute again replaces the current
// mute; d <= 0 is an Unmute.
func (b *Bus) Mute(d time.Duration) {
	if b == nil {
		return
	}
	if d <= 0 {
		b.Unmute()
		return
	}
	now := b.now()
	until := now.Add(d)
	b.mu.Lock()
	b.mutedUntil = until
	b.muteFrom = now
	b.mu.Unlock()
	b.logf("notify: muted until %s", until.Format("15:04:05"))
	b.poke()
}

// Unmute ends a Mute early and releases the backlog as a digest. It does
// not end configured quiet hours: while those are still active the
// backlog stays held until they end.
func (b *Bus) Unmute() {
	if b == nil {
		return
	}
	b.mu.Lock()
	wasMuted := !b.mutedUntil.IsZero()
	b.mutedUntil = time.Time{}
	b.mu.Unlock()
	if wasMuted {
		b.logf("notify: unmuted")
	}
	b.poke()
}

// MutedUntil returns when the current mute ends, or the zero time when the
// bus is not muted (for /status).
func (b *Bus) MutedUntil() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mutedUntil.IsZero() || !b.now().Before(b.mutedUntil) {
		return time.Time{}
	}
	return b.mutedUntil
}

// Held returns a copy of the events set aside by quiet hours or mute, in
// arrival order.
func (b *Bus) Held() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.held))
	copy(out, b.held)
	return out
}

// Close stops the bus. Already-queued events are still delivered and open
// aggregation windows are closed early for up to closeGrace; after that
// in-flight sends are cancelled. Events still held for a digest are
// dropped. Emit after Close drops. Safe to call more than once and on a
// nil bus.
func (b *Bus) Close() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		close(b.closing)
		t := time.NewTimer(closeGrace)
		defer t.Stop()
		select {
		case <-b.done:
		case <-t.C:
		}
		b.cancel()
		<-b.done
	})
}

// poke wakes the worker so it re-reads shared state and re-arms its timer.
func (b *Bus) poke() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// run is the worker: take one event, drain whatever else is queued, merge
// when backlogged, then push the head through the pipeline. Between events
// it sleeps on the nearest deadline and runs tick when it passes.
func (b *Bus) run() {
	defer close(b.done)
	var pending []Event
	for {
		if len(pending) == 0 {
			b.tick()
			timer := b.arm()
			select {
			case <-b.ctx.Done():
				return
			case ev := <-b.queue:
				pending = append(pending, ev)
			case <-timer:
				b.timer = nil
				continue
			case <-b.wake:
				continue
			case <-b.closing:
				if pending = b.drain(pending); len(pending) == 0 {
					b.shutdown()
					return
				}
			}
		}
		pending = b.drain(pending)
		if len(pending) > mergeThreshold {
			pending = mergeEvents(pending)
		}
		ev := pending[0]
		pending = pending[1:]
		if b.ctx.Err() != nil {
			return
		}
		b.process(ev)
	}
}

// drain appends everything currently buffered in the queue to pending.
func (b *Bus) drain(pending []Event) []Event {
	for {
		select {
		case ev := <-b.queue:
			pending = append(pending, ev)
		default:
			return pending
		}
	}
}

// process runs one event through the pipeline stages in design-doc order.
func (b *Bus) process(ev Event) {
	// Deadlines that passed while waiting go first, so a closed window's
	// summary or a finished quiet window's digest precedes newer events.
	b.tick()
	// 1. category filter (hot-reloaded config)
	if !b.config().Enabled(ev.Category, ev.Kind) {
		return
	}
	// 2. aggregation (security.unauthorized / unknown_chat 5-minute windows)
	ev, ok := b.aggregate(ev)
	if !ok {
		return
	}
	// 3. quiet hours / mute, then 4+5. throttle and send with retries
	b.pass(ev)
}

// pass runs ev through the quiet/mute stage and delivers it if allowed.
func (b *Bus) pass(ev Event) {
	if b.quiet(ev) {
		b.deliver(ev)
	}
}

// tick handles every deadline that has passed: it closes expired
// aggregation windows and, once neither quiet hours nor a mute is in force
// any more, releases the held backlog as a digest.
func (b *Bus) tick() {
	now := b.now()
	b.closeWindows(now, false)
	if b.heldCount() > 0 {
		if blocked, _ := b.blocked(now); !blocked {
			b.flushHeld(now)
		}
	}
}

// arm returns the timer channel for the nearest deadline, re-arming only
// when the deadline moved. nil means there is nothing to wait for.
func (b *Bus) arm() <-chan time.Time {
	at := b.nextDeadline()
	if at.IsZero() {
		b.timer, b.timerAt = nil, time.Time{}
		return nil
	}
	if b.timer == nil || !at.Equal(b.timerAt) {
		b.timer, b.timerAt = b.after(at.Sub(b.now())), at
	}
	return b.timer
}

// nextDeadline is the earliest moment tick has work to do, or zero.
func (b *Bus) nextDeadline() time.Time {
	var at time.Time
	for _, w := range b.windows {
		at = earliest(at, w.until)
	}
	if d := b.heldDeadline(b.now()); !d.IsZero() {
		at = earliest(at, d)
	}
	return at
}

// earliest returns the earlier of a and c, treating a zero a as "unset".
func earliest(a, c time.Time) time.Time {
	if a.IsZero() || c.Before(a) {
		return c
	}
	return a
}

// shutdown runs when Close has drained the queue: open aggregation
// windows are summarised now rather than lost; held events are dropped
// (a digest at shutdown would report a window that has not ended).
func (b *Bus) shutdown() {
	b.closeWindows(b.now(), true)
	if n := b.dropHeld(); n > 0 {
		b.logf("notify: bus closed with %d held event(s) dropped", n)
	}
}

// quiet reports whether ev may go out now. Always-pass events do; during
// quiet hours or a mute, security.* passes when SecurityBypass is set and
// everything else is set aside in held for the digest.
func (b *Bus) quiet(ev Event) bool {
	if alwaysPass[ev.Key()] {
		return true
	}
	now := b.now()
	blocked, q := b.blocked(now)
	if !blocked {
		return true
	}
	if ev.Category == "security" && q.SecurityBypass {
		return true
	}
	b.hold(ev, q, now)
	return false
}

// blocked reports whether a mute or the configured quiet hours are in
// force at now, and returns the live quiet-hours settings.
func (b *Bus) blocked(now time.Time) (bool, QuietHours) {
	var q QuietHours
	if b.opts.Quiet != nil {
		q = b.opts.Quiet()
	}
	return b.muted(now) || q.Active(now), q
}

func (b *Bus) muted(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.mutedUntil.IsZero() && now.Before(b.mutedUntil)
}

// deliver sends ev, spacing sends at least minSendGap apart and retrying
// failures with backoff (or the error's RetryAfter). It gives up after
// maxRetries retries or when the bus is cancelled.
func (b *Bus) deliver(ev Event) {
	if b.opts.Sink == nil {
		return
	}
	for attempt := 0; ; attempt++ {
		b.throttle()
		if b.ctx.Err() != nil {
			return
		}
		err := b.opts.Sink.Send(b.ctx, ev)
		b.lastSend = b.now()
		if err == nil {
			return
		}
		if b.ctx.Err() != nil {
			return
		}
		if attempt >= maxRetries {
			b.logf("notify: %s dropped after %d attempts: %v", ev.Key(), attempt+1, err)
			return
		}
		wait := backoff[attempt]
		var ra retryAfterer
		if errors.As(err, &ra) && ra.RetryAfter() > 0 {
			wait = ra.RetryAfter()
		}
		b.logf("notify: %s send failed (attempt %d/%d), retrying in %s: %v", ev.Key(), attempt+1, maxRetries+1, wait, err)
		b.sleep(wait)
	}
}

// throttle waits until minSendGap has passed since the previous send.
func (b *Bus) throttle() {
	if b.lastSend.IsZero() {
		return
	}
	if wait := minSendGap - b.now().Sub(b.lastSend); wait > 0 {
		b.sleep(wait)
	}
}

// mergeEvents collapses same-Category.Kind events into the newest one,
// summing their counts into Fields["count"]. Relative order of the
// surviving events is kept.
func mergeEvents(evs []Event) []Event {
	counts := make(map[string]int)
	for _, ev := range evs {
		counts[ev.Key()] += eventCount(ev)
	}
	seen := make(map[string]bool, len(counts))
	out := make([]Event, 0, len(counts))
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		key := ev.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		if n := counts[key]; n > 1 {
			ev.Fields = withFields(ev.Fields, map[string]string{"count": strconv.Itoa(n)})
		}
		out = append(out, ev)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// eventCount is how many occurrences ev already stands for (its "count"
// field, or 1).
func eventCount(ev Event) int {
	if s, ok := ev.Fields["count"]; ok {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// withFields returns a copy of fields with extra merged in; the input map
// (which may belong to the emitter) is never modified.
func withFields(fields, extra map[string]string) map[string]string {
	out := make(map[string]string, len(fields)+len(extra))
	for k, v := range fields {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (b *Bus) config() Config {
	if b.opts.Config != nil {
		return b.opts.Config()
	}
	return nil // nil Config = all defaults
}

func (b *Bus) now() time.Time {
	if b.opts.Now != nil {
		return b.opts.Now()
	}
	return time.Now()
}

// after is the deadline timer source (Options.After or time.After).
func (b *Bus) after(d time.Duration) <-chan time.Time {
	if d < 0 {
		d = 0
	}
	if b.opts.After != nil {
		return b.opts.After(d)
	}
	return time.After(d)
}

// sleep waits d, or until Close cancels the bus.
func (b *Bus) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if b.opts.Sleep != nil {
		b.opts.Sleep(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-b.ctx.Done():
	}
}

func (b *Bus) logf(format string, args ...any) {
	if b.opts.Log != nil {
		b.opts.Log(format, args...)
		return
	}
	pkgLogMu.RLock()
	fn := pkgLog
	pkgLogMu.RUnlock()
	if fn != nil {
		fn(format, args...)
	}
}
