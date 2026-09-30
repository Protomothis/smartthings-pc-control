package gui

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// TestIdleSecondsFrom covers the tick arithmetic behind the heartbeat
// (#77), including the ~49.7-day wrap of the 32-bit millisecond counters
// GetLastInputInfo and GetTickCount share.
func TestIdleSecondsFrom(t *testing.T) {
	const wrap = uint32(0xFFFFFFFF) // last tick before the counter wraps

	cases := []struct {
		name           string
		lastInput, now uint32
		want           int64
	}{
		{"just typed", 1_000_000, 1_000_000, 0},
		{"sub-second rounds down", 1_000_000, 1_000_999, 0},
		{"136 seconds", 1_000_000, 1_136_000, 136},
		{"idle for an hour", 0, 3_600_000, 3600},
		// The tick counter wrapped between the last input and now: the
		// uint32 subtraction must yield 9s, not ~49.7 days.
		{"wrapped", wrap - 4_000, 5_000, 9},
		{"wrapped exactly at zero", wrap - 2_999, 0, 3},
	}
	for _, tc := range cases {
		if got := idleSecondsFrom(tc.lastInput, tc.now); got != tc.want {
			t.Errorf("%s: idleSecondsFrom(%d, %d) = %d, want %d", tc.name, tc.lastInput, tc.now, got, tc.want)
		}
	}
}

// fakeHeartbeat replaces the samplers and switches behind buildHeartbeat.
func fakeHeartbeat(t *testing.T, expose, media bool, audio useraction.Audio, audioErr error) {
	t.Helper()
	savedExpose, savedMedia, savedIdle, savedAudio, savedNow := heartbeatExposeSession, heartbeatMediaEnabled, heartbeatIdle, heartbeatAudio, heartbeatNow
	heartbeatExposeSession = func() bool { return expose }
	heartbeatMediaEnabled = func() bool { return media }
	heartbeatIdle = func() (int64, bool) { return 42, true }
	heartbeatAudio = func() (useraction.Audio, error) { return audio, audioErr }
	heartbeatNow = func() time.Time { return time.Date(2026, 9, 30, 21, 0, 7, 250e6, time.FixedZone("KST", 9*3600)) }
	t.Cleanup(func() {
		heartbeatExposeSession, heartbeatMediaEnabled, heartbeatIdle, heartbeatAudio, heartbeatNow = savedExpose, savedMedia, savedIdle, savedAudio, savedNow
	})
}

// TestBuildHeartbeat covers the two independent parts of the heartbeat
// (#77 idle, #104 audio) and the wire form the service parses.
func TestBuildHeartbeat(t *testing.T) {
	spk := useraction.Audio{Volume: 30, Device: "스피커"}

	fakeHeartbeat(t, true, true, spk, nil)
	hb, ok := buildHeartbeat()
	b, _ := json.Marshal(hb)
	want := `{"idle_seconds":42,"audio":{"volume":30,"muted":false,"device":"스피커","sampled_at":"2026-09-30T21:00:07.25+09:00"}}`
	if !ok || string(b) != want {
		t.Errorf("both on: %v %s, want %s", ok, b, want)
	}

	// Audio without the session opt-in: the volume commands need it either way.
	fakeHeartbeat(t, false, true, spk, nil)
	hb, ok = buildHeartbeat()
	if !ok || hb.IdleSeconds != nil || hb.Audio == nil {
		t.Errorf("audio only: %v %+v", ok, hb)
	}

	// media.enabled off, or no playback device: no audio block.
	fakeHeartbeat(t, true, false, spk, nil)
	if hb, ok = buildHeartbeat(); !ok || hb.Audio != nil {
		t.Errorf("media off: %v %+v", ok, hb)
	}
	fakeHeartbeat(t, true, true, useraction.Audio{}, errors.New("unsupported"))
	if hb, ok = buildHeartbeat(); !ok || hb.Audio != nil || hb.IdleSeconds == nil {
		t.Errorf("no device: %v %+v", ok, hb)
	}

	// Nothing to say: no post at all.
	fakeHeartbeat(t, false, false, spk, nil)
	if _, ok = buildHeartbeat(); ok {
		t.Error("an empty heartbeat would be posted")
	}
}
