package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func night() *quietBox {
	return &quietBox{q: QuietHours{Enabled: true, Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true}}
}

// noQuiet has quiet hours off but the digest on, as config.json defaults
// leave it: only Mute can hold events.
func noQuiet() *quietBox { return &quietBox{q: QuietHours{Digest: true}} }

func expectDigest(t *testing.T, got Event, count, period string, items ...string) {
	t.Helper()
	if got.Key() != "system.digest" {
		t.Fatalf("delivered %s %v, want system.digest", got.Key(), got.Fields)
	}
	if got.Fields["count"] != count || got.Fields["period"] != period {
		t.Errorf("digest count/period = %q/%q, want %q/%q", got.Fields["count"], got.Fields["period"], count, period)
	}
	if want := strings.Join(items, "\n"); got.Fields["items"] != want {
		t.Errorf("digest items:\n%s\nwant:\n%s", got.Fields["items"], want)
	}
}

func TestQuietHoursHoldThenDigestAtEnd(t *testing.T) {
	clock := newFakeClock(at("23:30"))
	sink := newFakeSink(clock)
	quiet := night()
	b := newTestBus(t, sink, clock, nil, quiet)

	b.Emit(Event{Category: "remote", Kind: "received", Fields: map[string]string{"command": "lock", "from": "10.0.0.7"}})
	b.Emit(ev("schedule", "executed"))
	clock.advance(20 * time.Minute) // 23:50
	b.Emit(Event{Category: "power", Kind: "started", Fields: map[string]string{"boot": "cold"}})
	b.Emit(Event{Category: "remote", Kind: "received", Fields: map[string]string{"command": "sleep"}})
	// The digest kind itself is never held.
	b.Emit(ev("system", "digest"))
	if got := waitFor(t, sink); got.Key() != "system.digest" {
		t.Fatalf("delivered %s, want the manual system.digest to pass", got.Key())
	}
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 4 {
		t.Fatalf("held %d events, want 4", len(held))
	}

	// Well before 07:00 nothing moves ...
	clock.advance(3 * time.Hour) // 02:50
	expectNothing(t, sink)
	// ... at 07:00 the digest goes out with the newest three items.
	clock.advance(4*time.Hour + 10*time.Minute) // 07:00:01
	expectDigest(t, waitFor(t, sink), "4", "22:00–07:00",
		"[23:30] schedule.executed",
		"[23:50] power.started — boot=cold",
		"[23:50] remote.received — command=sleep",
	)
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 0 {
		t.Errorf("held not cleared after the digest: %v", held)
	}

	// The next window starts empty: no leak across digests.
	b.Emit(ev("remote", "received"))
	if got := waitFor(t, sink); got.Key() != "remote.received" {
		t.Errorf("delivered %s after quiet hours, want remote.received", got.Key())
	}
	clock.advance(15 * time.Hour) // 22:00:01, quiet again
	b.Emit(ev("remote", "received"))
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 1 {
		t.Errorf("held %d events in the next window, want 1", len(held))
	}
}

func TestQuietHoursDigestDisabledDrops(t *testing.T) {
	clock := newFakeClock(at("23:30"))
	sink := newFakeSink(clock)
	quiet := night()
	quiet.set(func(q *QuietHours) { q.Digest = false })
	b := newTestBus(t, sink, clock, nil, quiet)

	b.Emit(ev("remote", "received"))
	b.Emit(ev("schedule", "executed"))
	marker(t, b, sink)
	if held := b.Held(); len(held) != 2 {
		t.Fatalf("held %d events, want 2", len(held))
	}
	clock.advance(8 * time.Hour)
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 0 {
		t.Errorf("held not dropped when digest is off: %v", held)
	}
}

func TestMuteHoldsAndUnmuteFlushesDigest(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, noQuiet())

	b.Mute(2 * time.Hour)
	if until := b.MutedUntil(); !until.Equal(at("14:00")) {
		t.Errorf("MutedUntil = %s, want 14:00", until)
	}
	clock.advance(10 * time.Minute)
	b.Emit(Event{Category: "remote", Kind: "received", Fields: map[string]string{"command": "lock"}})
	b.Emit(ev("remote", "grace_scheduled")) // always-pass
	b.Emit(Event{Category: "security", Kind: "login_limited", Fields: map[string]string{"from": "10.0.0.9"}})
	if got := waitFor(t, sink); got.Key() != "remote.grace_scheduled" {
		t.Fatalf("delivered %s during mute, want remote.grace_scheduled", got.Key())
	}
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 2 {
		t.Fatalf("held %d events, want 2", len(held))
	}

	clock.advance(20 * time.Minute) // 12:30:01
	b.Unmute()
	if !b.MutedUntil().IsZero() {
		t.Error("MutedUntil should be zero after Unmute")
	}
	expectDigest(t, waitFor(t, sink), "2", "12:00–12:30",
		"[12:10] remote.received — command=lock",
		"[12:10] security.login_limited — from=10.0.0.9",
	)
	expectNothing(t, sink)
	// Unmute with nothing muted or held is a no-op.
	b.Unmute()
	expectNothing(t, sink)
}

func TestMuteExpiresIntoDigest(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, noQuiet())

	b.Mute(time.Hour)
	b.Emit(ev("schedule", "executed"))
	marker(t, b, sink)
	clock.advance(30 * time.Minute)
	expectNothing(t, sink)
	clock.advance(30 * time.Minute) // 13:00:01
	expectDigest(t, waitFor(t, sink), "1", "12:00–13:00", "[12:00] schedule.executed")
	if !b.MutedUntil().IsZero() {
		t.Error("MutedUntil should be zero once the mute expired")
	}
}

func TestMuteReplacesAndZeroUnmutes(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, noQuiet())

	b.Mute(2 * time.Hour)
	b.Mute(30 * time.Minute)
	if until := b.MutedUntil(); !until.Equal(at("12:30")) {
		t.Errorf("MutedUntil after re-Mute = %s, want 12:30", until)
	}
	b.Emit(ev("schedule", "executed"))
	marker(t, b, sink)
	b.Mute(0) // == Unmute
	if !b.MutedUntil().IsZero() {
		t.Error("Mute(0) should unmute")
	}
	expectDigest(t, waitFor(t, sink), "1", "12:00–12:00", "[12:00] schedule.executed")

	var nilBus *Bus
	nilBus.Mute(time.Hour) // must not panic
	nilBus.Unmute()
}

func TestUnmuteKeepsQuietHours(t *testing.T) {
	clock := newFakeClock(at("23:30"))
	sink := newFakeSink(clock)
	quiet := night()
	b := newTestBus(t, sink, clock, nil, quiet)

	b.Mute(time.Hour)
	b.Emit(ev("remote", "received"))
	marker(t, b, sink)
	b.Unmute()
	expectNothing(t, sink) // quiet hours still hold the backlog
	if held := b.Held(); len(held) != 1 {
		t.Fatalf("held %d events after Unmute inside quiet hours, want 1", len(held))
	}

	// Quiet hours switched off in config: noticed within the recheck
	// interval without any new event.
	quiet.set(func(q *QuietHours) { q.Enabled = false })
	clock.advance(holdRecheck)
	expectDigest(t, waitFor(t, sink), "1", "22:00–07:00", "[23:30] remote.received")
}

func TestSecurityBypassAndAlwaysPassDuringMute(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	quiet := noQuiet()
	quiet.set(func(q *QuietHours) { q.SecurityBypass = true })
	b := newTestBus(t, sink, clock, nil, quiet)

	b.Mute(time.Hour)
	b.Emit(ev("power", "started"))          // held
	b.Emit(unauthorized("10.0.0.5"))        // bypass
	b.Emit(ev("remote", "grace_scheduled")) // always-pass
	b.Emit(ev("remote", "force"))           // always-pass
	for _, key := range []string{"security.unauthorized", "remote.grace_scheduled", "remote.force"} {
		if got := waitFor(t, sink); got.Key() != key {
			t.Errorf("delivered %s, want %s", got.Key(), key)
		}
	}
	expectNothing(t, sink)
	if held := b.Held(); len(held) != 1 || held[0].Key() != "power.started" {
		t.Errorf("held = %v, want just power.started", held)
	}
}

func TestHeldCapKeepsNewestAndDigestCountsAll(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	b := newTestBus(t, sink, clock, nil, noQuiet())

	b.Mute(time.Hour)
	// Emit in batches below mergeThreshold so nothing is merged away.
	for i := 0; i < 105; i++ {
		b.Emit(Event{Category: "remote", Kind: "received", Fields: map[string]string{"n": fmt.Sprintf("%03d", i)}})
		if i%40 == 39 {
			marker(t, b, sink)
		}
	}
	marker(t, b, sink)
	held := b.Held()
	if len(held) != heldCapacity || held[0].Fields["n"] != "005" || held[99].Fields["n"] != "104" {
		t.Fatalf("held %d events, first %v last %v; want the newest 100", len(held), held[0].Fields, held[len(held)-1].Fields)
	}
	b.Unmute()
	expectDigest(t, waitFor(t, sink), "105", "12:00–12:00",
		"[12:00] remote.received — n=102",
		"[12:00] remote.received — n=103",
		"[12:00] remote.received — n=104",
	)
	if len(b.Held()) != 0 {
		t.Error("held not cleared after the digest")
	}
}

func TestCloseDropsHeld(t *testing.T) {
	clock := newFakeClock(at("12:00"))
	sink := newFakeSink(clock)
	q := noQuiet()
	b := New(Options{Sink: sink, Now: clock.now, Sleep: clock.sleep, After: clock.after, Quiet: q.get, Log: t.Logf})
	b.Mute(time.Hour)
	b.Emit(ev("remote", "received"))
	b.Close()
	if n := sink.calls(); n != 0 {
		t.Errorf("Close delivered %d events, want none (held events are dropped)", n)
	}
}

func TestDigestLine(t *testing.T) {
	e := Event{Category: "remote", Kind: "received", At: at("09:05"), Fields: map[string]string{"from": "10.0.0.7", "command": "lock"}}
	if got, want := digestLine(e), "[09:05] remote.received — command=lock, from=10.0.0.7"; got != want {
		t.Errorf("digestLine = %q, want %q", got, want)
	}
	if got, want := digestLine(Event{Category: "power", Kind: "started", At: at("09:05")}), "[09:05] power.started"; got != want {
		t.Errorf("digestLine without fields = %q, want %q", got, want)
	}
}
