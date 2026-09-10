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
	// heldCapacity bounds the quiet-hours/mute backlog until #59 drains it.
	heldCapacity = 100
	// closeGrace is how long Close waits for queued events to be delivered
	// before cancelling in-flight sends.
	closeGrace = 5 * time.Second
)

// backoff between attempts (attempt n waits backoff[n]) unless the error
// carries a RetryAfter.
var backoff = [maxRetries]time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// alwaysPass events skip quiet hours and mute: they are the user's chance
// to cancel something.
var alwaysPass = map[string]bool{
	"remote.grace_scheduled": true,
	"remote.force":           true,
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
// defaults are DefaultConfig, no quiet hours, time.Now and a real sleep.
type Options struct {
	Sink   Sink
	Config func() Config     // hot reload: called for every event
	Quiet  func() QuietHours // hot reload: called for every event
	Now    func() time.Time  // test clock; nil means time.Now
	// Sleep replaces the throttle/backoff waits; nil means a real,
	// Close-aware sleep. Tests pass a fake that advances Now.
	Sleep func(time.Duration)
	Log   func(string, ...any)
}

// Bus is the single in-process event pipeline: Emit → filter → aggregate
// → quiet/mute → throttle → Sink.Send (with retries). One worker goroutine
// drains the queue, so events reach the sink in emit order.
type Bus struct {
	opts    Options
	queue   chan Event
	closing chan struct{} // closed by Close: finish the queue, then exit
	done    chan struct{} // closed when the worker has exited
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once

	mu         sync.Mutex
	mutedUntil time.Time
	held       []Event // set aside by quiet hours/mute; #59 turns these into system.digest

	lastSend time.Time // worker-only
}

// New starts a bus and its worker goroutine.
func New(opts Options) *Bus {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bus{
		opts:    opts,
		queue:   make(chan Event, queueCapacity),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
	}
	go b.run()
	return b
}

// Emit queues ev without blocking. When the queue is full or the bus is
// closed the event is dropped and logged. A zero At is set to now.
func (b *Bus) Emit(ev Event) {
	if b == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = b.now()
	}
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
// shares the quiet-hours backlog; Unmute releases it.
func (b *Bus) Mute(d time.Duration) {
	until := b.now().Add(d)
	b.mu.Lock()
	b.mutedUntil = until
	b.mu.Unlock()
	b.logf("notify: muted until %s", until.Format("15:04:05"))
}

// Unmute ends a Mute early. Issue #59: also flush held as system.digest.
func (b *Bus) Unmute() {
	b.mu.Lock()
	b.mutedUntil = time.Time{}
	b.mu.Unlock()
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

// Close stops the bus. Already-queued events are still delivered for up
// to closeGrace; after that in-flight sends are cancelled. Emit after
// Close drops. Safe to call more than once and on a nil bus.
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

// run is the worker: take one event, drain whatever else is queued, merge
// when backlogged, then push the head through the pipeline.
func (b *Bus) run() {
	defer close(b.done)
	var pending []Event
	for {
		if len(pending) == 0 {
			select {
			case <-b.ctx.Done():
				return
			case ev := <-b.queue:
				pending = append(pending, ev)
			case <-b.closing:
				if pending = b.drain(pending); len(pending) == 0 {
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
	// 1. category filter (hot-reloaded config)
	if !b.config().Enabled(ev.Category, ev.Kind) {
		return
	}
	// 2. aggregation (security.unauthorized 5-minute windows)
	ev, ok := b.aggregate(ev)
	if !ok {
		return
	}
	// 3. quiet hours / mute
	if !b.quiet(ev) {
		return
	}
	// 4+5. throttle and send with retries
	b.deliver(ev)
}

// aggregate is the hook for issue #58. It returns the event to continue
// with (possibly a merged one) and false when the event was absorbed into
// a pending window. No-op for now.
func (b *Bus) aggregate(ev Event) (Event, bool) {
	return ev, true
}

// quiet reports whether ev may go out now. Always-pass events do; during
// quiet hours or a mute, security.* passes when SecurityBypass is set and
// everything else is set aside in held for the digest (#59).
func (b *Bus) quiet(ev Event) bool {
	if alwaysPass[ev.Key()] {
		return true
	}
	now := b.now()
	var q QuietHours
	if b.opts.Quiet != nil {
		q = b.opts.Quiet()
	}
	if !b.muted(now) && !q.Active(now) {
		return true
	}
	if ev.Category == "security" && q.SecurityBypass {
		return true
	}
	b.hold(ev)
	return false
}

func (b *Bus) muted(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.mutedUntil.IsZero() && now.Before(b.mutedUntil)
}

func (b *Bus) hold(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.held) >= heldCapacity {
		b.held = append(b.held[:0], b.held[1:]...)
	}
	b.held = append(b.held, ev)
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
			fields := make(map[string]string, len(ev.Fields)+1)
			for k, v := range ev.Fields {
				fields[k] = v
			}
			fields["count"] = strconv.Itoa(n)
			ev.Fields = fields
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
