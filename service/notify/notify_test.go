package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSink records every Send and can fail the first few calls.
type fakeSink struct {
	mu    sync.Mutex
	sent  []Event
	at    []time.Time // clock reading at each Send
	fails []error     // consumed one per call; nil entries mean success
	clock *fakeClock
	gate  chan struct{} // when set, the first Send blocks until it is closed
	got   chan Event
}

func newFakeSink(clock *fakeClock) *fakeSink {
	return &fakeSink{clock: clock, got: make(chan Event, 512)}
}

func (s *fakeSink) Send(_ context.Context, ev Event) error {
	s.mu.Lock()
	if s.gate != nil {
		gate := s.gate
		s.gate = nil
		s.mu.Unlock()
		<-gate
		s.mu.Lock()
	}
	s.sent = append(s.sent, ev)
	if s.clock != nil {
		s.at = append(s.at, s.clock.now())
	}
	var err error
	if len(s.fails) > 0 {
		err, s.fails = s.fails[0], s.fails[1:]
	}
	s.mu.Unlock()
	if err == nil {
		s.got <- ev
	}
	return err
}

func (s *fakeSink) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// fakeClock is an injectable Now/Sleep/After set: Sleep advances Now
// instantly and records what was requested, After registers a timer that
// fires as soon as the clock (via sleep or advance) reaches its deadline.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	c.fire()
}

// advance moves the clock forward as the test's "time passes".
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	c.fire()
}

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{at: c.t.Add(d), ch: ch})
	c.fire()
	return ch
}

// fire delivers every timer whose deadline has passed. Caller holds mu.
func (c *fakeClock) fire() {
	kept := c.timers[:0]
	for _, tm := range c.timers {
		if tm.at.After(c.t) {
			kept = append(kept, tm)
			continue
		}
		tm.ch <- c.t
	}
	c.timers = kept
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// quietBox is a hot-reloadable QuietHours the test can change while the
// worker reads it (the worker also reads it from its timer ticks).
type quietBox struct {
	mu sync.Mutex
	q  QuietHours
}

func (qb *quietBox) get() QuietHours {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	return qb.q
}

func (qb *quietBox) set(fn func(*QuietHours)) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	fn(&qb.q)
}

// newTestBus wires a fake sink and clock; cfg/quiet may be nil.
func newTestBus(t *testing.T, sink *fakeSink, clock *fakeClock, cfg Config, quiet *quietBox) *Bus {
	t.Helper()
	opts := Options{
		Sink:   sink,
		Now:    clock.now,
		Sleep:  clock.sleep,
		After:  clock.after,
		Config: func() Config { return cfg },
		Log:    t.Logf,
	}
	if quiet != nil {
		opts.Quiet = quiet.get
	}
	b := New(opts)
	t.Cleanup(b.Close)
	return b
}

func ev(cat, kind string) Event { return Event{Category: cat, Kind: kind} }

// waitFor returns the next delivered event or fails after a real timeout.
func waitFor(t *testing.T, sink *fakeSink) Event {
	t.Helper()
	select {
	case e := <-sink.got:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the sink")
		return Event{}
	}
}

// expectNothing fails if the sink receives anything for a short while.
func expectNothing(t *testing.T, sink *fakeSink) {
	t.Helper()
	select {
	case e := <-sink.got:
		t.Fatalf("unexpected delivery: %s", e.Key())
	case <-time.After(150 * time.Millisecond):
	}
}

func at(hhmm string) time.Time {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return time.Date(2026, 9, 10, h, m, 0, 0, time.Local)
}

func TestDefaultConfigCatalogue(t *testing.T) {
	def := DefaultConfig()
	on := map[string]bool{
		"remote.received":         true,
		"remote.grace_scheduled":  true,
		"schedule.created":        false,
		"schedule.cancelled":      false,
		"schedule.executed":       true,
		"power.started":           true,
		"power.stopping":          false,
		"security.unauthorized":   true,
		"security.unknown_chat":   true,
		"system.update_available": true,
		"system.tray_wake_failed": true,
	}
	for key, want := range on {
		cat, kind, _ := strings.Cut(key, ".")
		if got, ok := def[cat][kind]; !ok || got != want {
			t.Errorf("DefaultConfig()[%s][%s] = %v,%v want %v", cat, kind, got, ok, want)
		}
	}
	if len(def) != 5 {
		t.Errorf("expected 5 categories, got %d", len(def))
	}
	// Each call returns a fresh map.
	def["remote"]["received"] = false
	if !DefaultConfig()["remote"]["received"] {
		t.Error("DefaultConfig returned a shared map")
	}
}

func TestConfigEnabledFallsBackToDefaults(t *testing.T) {
	var nilCfg Config
	if !nilCfg.Enabled("remote", "received") {
		t.Error("nil config must use defaults (remote.received on)")
	}
	if nilCfg.Enabled("schedule", "created") {
		t.Error("nil config must use defaults (schedule.created off)")
	}
	cfg := Config{"remote": {"received": false}, "schedule": {"created": true}}
	if cfg.Enabled("remote", "received") {
		t.Error("explicit false ignored")
	}
	if !cfg.Enabled("schedule", "created") {
		t.Error("explicit true ignored")
	}
	if !cfg.Enabled("remote", "force") {
		t.Error("missing kind in a present category must fall back to default (true)")
	}
	if cfg.Enabled("power", "stopping") {
		t.Error("missing category must fall back to default (power.stopping off)")
	}
	// Not in the catalogue: never swallowed.
	if !cfg.Enabled("system", "test") || !nilCfg.Enabled("system", "digest") {
		t.Error("unknown kinds must be delivered")
	}
}

func TestConfigJSONRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg["remote"]["received"] = false
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, back) {
		t.Errorf("round trip changed config:\n%v\n%v", cfg, back)
	}
	// Partial JSON as an older config.json would have it.
	var partial Config
	if err := json.Unmarshal([]byte(`{"remote":{"received":false}}`), &partial); err != nil {
		t.Fatal(err)
	}
	full := partial.WithDefaults()
	if full["remote"]["received"] {
		t.Error("WithDefaults dropped the explicit value")
	}
	if !full["remote"]["force"] || full["schedule"]["created"] {
		t.Error("WithDefaults did not fill missing entries with defaults")
	}
	if len(partial) != 1 || len(partial["remote"]) != 1 {
		t.Error("WithDefaults modified its receiver")
	}
	// Over keeps base entries the overlay does not mention and never aliases.
	base := Config{"remote": {"received": true, "force": true}, "x": {"y": true}}
	over := Config{"remote": {"received": false}}.Over(base)
	over["x"]["y"] = false
	if !over["remote"]["force"] || over["remote"]["received"] || !base["x"]["y"] {
		t.Errorf("Over = %v (base now %v)", over, base)
	}
}

func TestBusFiltersByConfig(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	cfg := Config{"remote": {"received": false}}
	b := newTestBus(t, sink, clock, cfg, nil)

	b.Emit(ev("remote", "received"))        // explicitly off
	b.Emit(ev("schedule", "created"))       // default off
	b.Emit(ev("remote", "grace_scheduled")) // default on

	if got := waitFor(t, sink); got.Key() != "remote.grace_scheduled" {
		t.Errorf("delivered %s, want remote.grace_scheduled", got.Key())
	}
	expectNothing(t, sink)

	// Hot reload: the Config func is consulted per event.
	cfg["remote"]["received"] = true
	b.Emit(ev("remote", "received"))
	if got := waitFor(t, sink); got.Key() != "remote.received" {
		t.Errorf("delivered %s after enabling, want remote.received", got.Key())
	}
}

func TestBusEmitSetsAtWhenZero(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, nil)
	b.Emit(ev("remote", "received"))
	if got := waitFor(t, sink); !got.At.Equal(at("12:00")) {
		t.Errorf("At = %s, want the bus clock %s", got.At, at("12:00"))
	}
}

func TestBusQuietHoursAndMute(t *testing.T) {
	clock := newFakeClock(at("23:30")) // inside 22:00–07:00
	sink := newFakeSink(clock)
	quiet := &quietBox{q: QuietHours{Enabled: true, Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true}}
	b := newTestBus(t, sink, clock, nil, quiet)

	b.Emit(ev("remote", "received"))        // held
	b.Emit(ev("remote", "grace_scheduled")) // always-pass
	b.Emit(ev("security", "unauthorized"))  // bypass
	b.Emit(ev("remote", "force"))           // always-pass

	want := []string{"remote.grace_scheduled", "security.unauthorized", "remote.force"}
	for _, key := range want {
		if got := waitFor(t, sink); got.Key() != key {
			t.Errorf("delivered %s, want %s", got.Key(), key)
		}
	}
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 1 || held[0].Key() != "remote.received" {
		t.Errorf("held = %v, want just remote.received", held)
	}

	// Without the security bypass, security events are held too.
	quiet.set(func(q *QuietHours) { q.SecurityBypass = false })
	b.Emit(ev("security", "login_limited"))
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 2 {
		t.Errorf("held %d events, want 2", len(held))
	}

	// Outside quiet hours the backlog goes out as a digest, then everything
	// flows again ...
	quiet.set(func(q *QuietHours) { q.Enabled = false })
	b.Emit(ev("remote", "received"))
	if got := waitFor(t, sink); got.Key() != "system.digest" || got.Fields["count"] != "2" {
		t.Errorf("delivered %s %v, want system.digest count=2", got.Key(), got.Fields)
	}
	if got := waitFor(t, sink); got.Key() != "remote.received" {
		t.Errorf("delivered %s, want remote.received", got.Key())
	}

	// ... until a mute, which honours the same rules.
	b.Mute(2 * time.Hour)
	if b.MutedUntil().IsZero() {
		t.Error("MutedUntil should report the active mute")
	}
	b.Emit(ev("schedule", "executed"))
	b.Emit(ev("remote", "force"))
	if got := waitFor(t, sink); got.Key() != "remote.force" {
		t.Errorf("delivered %s during mute, want remote.force", got.Key())
	}
	expectNothing(t, sink)
	b.Unmute()
	if !b.MutedUntil().IsZero() {
		t.Error("MutedUntil should be zero after Unmute")
	}
	if got := waitFor(t, sink); got.Key() != "system.digest" || got.Fields["count"] != "1" {
		t.Errorf("delivered %s %v after unmute, want system.digest count=1", got.Key(), got.Fields)
	}
	b.Emit(ev("schedule", "executed"))
	if got := waitFor(t, sink); got.Key() != "schedule.executed" {
		t.Errorf("delivered %s after unmute, want schedule.executed", got.Key())
	}
}

func TestBusThrottlesOneSendPerSecond(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, nil)

	for i := 0; i < 3; i++ {
		b.Emit(ev("remote", "received"))
	}
	for i := 0; i < 3; i++ {
		waitFor(t, sink)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for i := 1; i < len(sink.at); i++ {
		if gap := sink.at[i].Sub(sink.at[i-1]); gap < time.Second {
			t.Errorf("send %d only %s after the previous one", i, gap)
		}
	}
	if slept := clock.slept(); len(slept) != 2 || slept[0] != time.Second || slept[1] != time.Second {
		t.Errorf("throttle sleeps = %v, want [1s 1s]", slept)
	}
}

func TestBusRetriesWithBackoff(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	sink.fails = []error{errors.New("boom"), errors.New("boom again")}
	b := newTestBus(t, sink, clock, nil, nil)

	b.Emit(ev("system", "exec_failed"))
	waitFor(t, sink)
	if n := sink.calls(); n != 3 {
		t.Errorf("Send called %d times, want 3 (2 failures + success)", n)
	}
	if slept := clock.slept(); !reflect.DeepEqual(slept, []time.Duration{time.Second, 4 * time.Second}) {
		t.Errorf("backoff sleeps = %v, want [1s 4s]", slept)
	}
}

type rateLimited struct{ after time.Duration }

func (e rateLimited) Error() string             { return "429 Too Many Requests" }
func (e rateLimited) RetryAfter() time.Duration { return e.after }
func (e rateLimited) wrapped() error            { return fmt.Errorf("telegram: %w", e) }
func TestBusHonoursRetryAfter(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	sink.fails = []error{rateLimited{7 * time.Second}.wrapped()}
	b := newTestBus(t, sink, clock, nil, nil)

	b.Emit(ev("remote", "received"))
	waitFor(t, sink)
	if slept := clock.slept(); !reflect.DeepEqual(slept, []time.Duration{7 * time.Second}) {
		t.Errorf("sleeps = %v, want [7s] from RetryAfter (wrapped error)", slept)
	}
}

func TestBusGivesUpAfterThreeRetries(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	fail := errors.New("down")
	sink.fails = []error{fail, fail, fail, fail}
	b := newTestBus(t, sink, clock, nil, nil)

	b.Emit(ev("remote", "received"))
	b.Emit(ev("remote", "force")) // proves the worker moved on
	if got := waitFor(t, sink); got.Key() != "remote.force" {
		t.Errorf("delivered %s, want remote.force after the first event was dropped", got.Key())
	}
	if n := sink.calls(); n != 5 {
		t.Errorf("Send called %d times, want 4 failed attempts + 1", n)
	}
	want := []time.Duration{time.Second, 4 * time.Second, 16 * time.Second, time.Second}
	if slept := clock.slept(); !reflect.DeepEqual(slept, want) {
		t.Errorf("sleeps = %v, want %v (backoff then throttle gap)", slept, want)
	}
}

func TestBusMergesBacklog(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	gate := make(chan struct{})
	sink.gate = gate
	b := newTestBus(t, sink, clock, nil, nil)

	// The worker takes this one and blocks inside Send ...
	b.Emit(Event{Category: "remote", Kind: "received", Fields: map[string]string{"command": "first"}})
	deadline := time.Now().Add(2 * time.Second)
	for sink.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// ... while 65 more pile up: 60 of one kind, 5 of another.
	for i := 0; i < 60; i++ {
		b.Emit(Event{Category: "security", Kind: "unauthorized", Fields: map[string]string{"from": fmt.Sprintf("10.0.0.%d", i)}})
	}
	for i := 0; i < 5; i++ {
		b.Emit(ev("schedule", "executed"))
	}
	close(gate)

	if got := waitFor(t, sink); got.Fields["command"] != "first" {
		t.Fatalf("first delivery = %v", got)
	}
	got := waitFor(t, sink)
	if got.Key() != "security.unauthorized" || got.Fields["count"] != "60" || got.Fields["from"] != "10.0.0.59" {
		t.Errorf("merged unauthorized = %v, want newest with count 60", got.Fields)
	}
	got = waitFor(t, sink)
	if got.Key() != "schedule.executed" || got.Fields["count"] != "5" {
		t.Errorf("merged executed = %v, want count 5", got.Fields)
	}
	expectNothing(t, sink)
}

func TestMergeEventsKeepsSinglesUntouchedAndSumsCounts(t *testing.T) {
	in := []Event{
		{Category: "a", Kind: "x", Fields: map[string]string{"count": "3"}},
		{Category: "b", Kind: "y"},
		{Category: "a", Kind: "x", Fields: map[string]string{"n": "2"}},
	}
	out := mergeEvents(in)
	if len(out) != 2 {
		t.Fatalf("got %d events, want 2", len(out))
	}
	if out[0].Key() != "b.y" || out[0].Fields != nil {
		t.Errorf("single event changed: %+v", out[0])
	}
	if out[1].Fields["count"] != "4" || out[1].Fields["n"] != "2" {
		t.Errorf("merged fields = %v, want count 4 on the newest", out[1].Fields)
	}
	if in[2].Fields["count"] != "" {
		t.Error("mergeEvents mutated the input's Fields map")
	}
}

func TestBusCloseDeliversQueuedThenDrops(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := New(Options{Sink: sink, Now: clock.now, Sleep: clock.sleep, Log: t.Logf})
	for i := 0; i < 3; i++ {
		b.Emit(ev("remote", "received"))
	}
	b.Close()
	if n := sink.calls(); n != 3 {
		t.Errorf("Close delivered %d of 3 queued events", n)
	}
	b.Emit(ev("remote", "received")) // must not panic or deliver
	b.Close()                        // idempotent
	if n := sink.calls(); n != 3 {
		t.Errorf("Emit after Close delivered (%d sends)", n)
	}
	var nilBus *Bus
	nilBus.Emit(ev("remote", "received"))
	nilBus.Close()
}

func TestQuietHoursActive(t *testing.T) {
	night := QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	day := QuietHours{Enabled: true, Start: "09:00", End: "17:00"}
	cases := []struct {
		q    QuietHours
		now  string
		want bool
	}{
		{night, "21:59", false},
		{night, "22:00", true}, // start inclusive
		{night, "23:59", true},
		{night, "00:00", true}, // across midnight
		{night, "03:30", true},
		{night, "06:59", true},
		{night, "07:00", false}, // end exclusive
		{night, "12:00", false},
		{day, "08:59", false},
		{day, "09:00", true},
		{day, "12:00", true},
		{day, "17:00", false},
		{day, "23:00", false},
		{QuietHours{Enabled: false, Start: "22:00", End: "07:00"}, "23:00", false},
		{QuietHours{Enabled: true, Start: "22:00", End: "22:00"}, "22:00", false}, // empty window
		{QuietHours{Enabled: true, Start: "25:00", End: "07:00"}, "23:00", false}, // malformed
		{QuietHours{Enabled: true, Start: "", End: ""}, "23:00", false},
	}
	for _, c := range cases {
		if got := c.q.Active(at(c.now)); got != c.want {
			t.Errorf("%s-%s (enabled %v) Active(%s) = %v, want %v", c.q.Start, c.q.End, c.q.Enabled, c.now, got, c.want)
		}
	}
}

func TestQuietHoursNextEnd(t *testing.T) {
	night := QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	// Before midnight the window ends tomorrow 07:00; after midnight, today.
	if got := night.NextEnd(at("23:00")); !got.Equal(at("07:00").AddDate(0, 0, 1)) {
		t.Errorf("NextEnd(23:00) = %s, want tomorrow 07:00", got)
	}
	if got := night.NextEnd(at("03:00")); !got.Equal(at("07:00")) {
		t.Errorf("NextEnd(03:00) = %s, want today 07:00", got)
	}
	if got := night.NextEnd(at("12:00")); !got.IsZero() {
		t.Errorf("NextEnd outside the window = %s, want zero", got)
	}
}
