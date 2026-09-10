package notify

import (
	"testing"
	"time"
)

func unauthorized(from string) Event {
	return Event{Category: "security", Kind: "unauthorized", Fields: map[string]string{"from": from, "path": "/***/lock"}}
}

func unknownChat(chatID string) Event {
	return Event{Category: "security", Kind: "unknown_chat", Fields: map[string]string{"chat_id": chatID, "username": "x", "text": "/lock"}}
}

// marker emits an always-pass event and waits for it: because the worker
// is sequential, its delivery proves everything emitted before it has been
// processed (absorbed into a window, held, ...). It costs one throttle
// second on the fake clock.
func marker(t *testing.T, b *Bus, sink *fakeSink) {
	t.Helper()
	b.Emit(ev("remote", "force"))
	if got := waitFor(t, sink); got.Key() != "remote.force" {
		t.Fatalf("marker: delivered %s first, want remote.force", got.Key())
	}
}

func expectAggregate(t *testing.T, got Event, key, field, source, count string) {
	t.Helper()
	if got.Key() != key {
		t.Fatalf("delivered %s, want %s", got.Key(), key)
	}
	if got.Fields[field] != source || got.Fields["count"] != count {
		t.Errorf("%s fields = %v, want %s=%s count=%s", key, got.Fields, field, source, count)
	}
	if got.Fields["window"] != "5m" || got.Fields["window_sec"] != "300" {
		t.Errorf("%s window fields = %q/%q, want 5m/300", key, got.Fields["window"], got.Fields["window_sec"])
	}
}

func TestAggregateFirstPassesThenSummarisesRepeats(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, nil)

	first := unauthorized("10.0.0.5")
	b.Emit(first)
	got := waitFor(t, sink)
	expectAggregate(t, got, "security.unauthorized", "from", "10.0.0.5", "1")
	if got.Fields["path"] != "/***/lock" {
		t.Errorf("first event lost its own fields: %v", got.Fields)
	}
	if _, ok := first.Fields["count"]; ok {
		t.Error("aggregate mutated the emitter's Fields map")
	}

	// Two repeats inside the window are absorbed ...
	b.Emit(unauthorized("10.0.0.5"))
	b.Emit(unauthorized("10.0.0.5"))
	marker(t, b, sink)
	expectNothing(t, sink)

	// ... and summarised once the window closes, total including the first.
	clock.advance(5 * time.Minute)
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "10.0.0.5", "3")
	expectNothing(t, sink)

	// After the window the source starts over: the next event passes at once.
	b.Emit(unauthorized("10.0.0.5"))
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "10.0.0.5", "1")
	// A window with no repeats produces no summary.
	clock.advance(6 * time.Minute)
	expectNothing(t, sink)
}

func TestAggregateWindowsArePerSource(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, nil)

	b.Emit(unauthorized("a"))
	b.Emit(unauthorized("b"))
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "a", "1")
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "b", "1")

	b.Emit(unauthorized("a"))
	b.Emit(unauthorized("b"))
	b.Emit(unauthorized("a"))
	b.Emit(unauthorized("c")) // new source: passes immediately
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "c", "1")
	expectNothing(t, sink)

	// Both windows close; the older one (a) is summarised first.
	clock.advance(5 * time.Minute)
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "a", "3")
	expectAggregate(t, waitFor(t, sink), "security.unauthorized", "from", "b", "2")
	expectNothing(t, sink) // c had no repeats
}

func TestAggregateUnknownChatKeyedByChatID(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, nil)

	b.Emit(unknownChat("111"))
	b.Emit(unknownChat("222"))
	b.Emit(unknownChat("111"))
	b.Emit(unknownChat("111"))
	expectAggregate(t, waitFor(t, sink), "security.unknown_chat", "chat_id", "111", "1")
	expectAggregate(t, waitFor(t, sink), "security.unknown_chat", "chat_id", "222", "1")
	expectNothing(t, sink)

	clock.advance(5 * time.Minute)
	got := waitFor(t, sink)
	expectAggregate(t, got, "security.unknown_chat", "chat_id", "111", "3")
	if got.Fields["username"] != "x" || got.Fields["text"] != "/lock" {
		t.Errorf("summary lost the newest event's fields: %v", got.Fields)
	}
	expectNothing(t, sink)
}

func TestAggregateLeavesOtherKindsAlone(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, nil)

	for i := 0; i < 2; i++ {
		b.Emit(Event{Category: "security", Kind: "login_limited", Fields: map[string]string{"from": "10.0.0.5"}})
	}
	for i := 0; i < 2; i++ {
		got := waitFor(t, sink)
		if got.Key() != "security.login_limited" {
			t.Fatalf("delivered %s, want security.login_limited", got.Key())
		}
		if _, ok := got.Fields["count"]; ok {
			t.Errorf("non-aggregated kind got aggregation fields: %v", got.Fields)
		}
	}
}

func TestAggregateCloseFlushesOpenWindows(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := New(Options{Sink: sink, Now: clock.now, Sleep: clock.sleep, After: clock.after, Log: t.Logf})

	b.Emit(unauthorized("10.0.0.5"))
	b.Emit(unauthorized("10.0.0.5"))
	b.Emit(unauthorized("10.0.0.5"))
	b.Close()

	if n := sink.calls(); n != 2 {
		t.Fatalf("Close delivered %d events, want first + summary", n)
	}
	expectAggregate(t, sink.sent[0], "security.unauthorized", "from", "10.0.0.5", "1")
	expectAggregate(t, sink.sent[1], "security.unauthorized", "from", "10.0.0.5", "3")
}

func TestAggregateSummaryRespectsQuietHours(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	quiet := &quietBox{q: QuietHours{Enabled: true, Start: "12:03", End: "13:00", SecurityBypass: false}}
	b := newTestBus(t, sink, clock, nil, quiet)

	b.Emit(unauthorized("10.0.0.5"))
	b.Emit(unauthorized("10.0.0.5"))
	waitFor(t, sink)
	marker(t, b, sink)

	// The window closes inside quiet hours without bypass: the summary is held.
	clock.advance(5 * time.Minute)
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 1 || held[0].Fields["count"] != "2" {
		t.Errorf("held = %v, want the count-2 summary", held)
	}
}

func TestFormatWindow(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Minute:               "5m",
		90 * time.Second:              "90s",
		time.Hour:                     "1h",
		time.Hour + 30*time.Minute:    "1h30m",
		2*time.Hour + 5*time.Minute:   "2h5m",
		2*time.Hour + 500*time.Second: "7700s",
	}
	for d, want := range cases {
		if got := formatWindow(d); got != want {
			t.Errorf("formatWindow(%s) = %q, want %q", d, got, want)
		}
	}
}
