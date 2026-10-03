package service

// The service reads the audio and media state of the target session
// itself when no tray app reports from it (session_fill.go). Seen live
// (v1.2.0-rc15): console session 1 unlocked and targeted, the tray app in
// RDP session 2 (its heartbeats ignored) and none in session 1 — status
// said audio.available=false, so the driver refused mute and volume with
// "사용자 없음" although someone was logged in.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// fakeFill is the user session behind the fills: what `audio get` and
// `media info` answer, and the argument vectors they were run with.
type fakeFill struct {
	mu    sync.Mutex
	calls []string
	audio useraction.Audio
	np    useraction.NowPlaying
	err   error
	// pending are the readings sys.startFill was handed, not yet run.
	pending []func()
}

// fillTestSetup is nowPlayingSetup (media on, someone logged in, empty
// stores) with a movable clock, fresh gates, no target session, the
// session's answers faked and the background readings held until
// runPending.
func fillTestSetup(t *testing.T, cfg Config) (*fakeFill, *time.Time) {
	t.Helper()
	now := nowPlayingSetup(t, cfg)
	clock.audio = func() time.Time { return now }
	resetSessionFills()
	resetTargetSession()
	f := &fakeFill{
		audio: useraction.Audio{Volume: 30, Muted: false, Device: "스피커"},
		np:    spotifyTrack,
	}
	savedRun, savedStart := userRun.media, sys.startFill
	userRun.media = func(_ context.Context, args ...string) (UserActionResult, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, strings.Join(args, " "))
		if f.err != nil {
			return UserActionResult{}, f.err
		}
		if args[0] == "audio" {
			a := f.audio
			return UserActionResult{OK: true, Audio: &a}, nil
		}
		raw, _ := json.Marshal(f.np)
		return UserActionResult{OK: true, Fields: map[string]json.RawMessage{"media": raw}}, nil
	}
	sys.startFill = func(run func()) {
		f.mu.Lock()
		f.pending = append(f.pending, run)
		f.mu.Unlock()
	}
	t.Cleanup(func() {
		userRun.media, sys.startFill = savedRun, savedStart
		resetSessionFills()
		resetTargetSession()
	})
	return f, &now
}

// runPending runs the readings started so far and returns how many.
func (f *fakeFill) runPending() int {
	f.mu.Lock()
	runs := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, run := range runs {
		run()
	}
	return len(runs)
}

func (f *fakeFill) pendingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

func (f *fakeFill) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// tapDeviceEvents collects the device events the bus taps see.
func tapDeviceEvents(t *testing.T) func() []string {
	t.Helper()
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	var mu sync.Mutex
	var got []string
	busMu.RLock()
	bus.Tap(func(ev notify.Event) {
		mu.Lock()
		got = append(got, ev.Key())
		mu.Unlock()
	})
	busMu.RUnlock()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// No sample and someone logged in: the status read answers at once
// (unavailable) and starts one reading; once it lands the next read is
// available and audio.changed went out.
func TestFillAudioWhenNoSample(t *testing.T) {
	f, _ := fillTestSetup(t, mediaOn())
	events := tapDeviceEvents(t)

	if a := stAudioStatus(getConfig()); a.Available {
		t.Fatalf("audio = %+v before any reading", a)
	}
	if n := f.runPending(); n != 1 {
		t.Fatalf("%d readings started, want 1", n)
	}
	if got := f.Calls(); len(got) != 1 || got[0] != "audio get" {
		t.Errorf("runs = %q, want [audio get]", got)
	}
	a := stAudioStatus(getConfig())
	if !a.Available || a.Volume == nil || *a.Volume != 30 || *a.Device != "스피커" {
		t.Errorf("audio after the reading = %+v", a)
	}
	if got := events(); len(got) != 1 || got[0] != "audio.changed" {
		t.Errorf("events = %q, want one audio.changed", got)
	}
	// A fresh sample: no further reading.
	stAudioStatus(getConfig())
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings with a fresh sample", n)
	}
}

// One reading at a time, at most one every fillEvery; a failure leaves
// audio unavailable, is logged once, and is retried after the interval.
func TestFillRateLimitAndFailure(t *testing.T) {
	f, now := fillTestSetup(t, mediaOn())
	logs := captureLog(t)
	f.err = errors.New("no default playback device")

	stAudioStatus(getConfig())
	stAudioStatus(getConfig())
	if n := f.pendingCount(); n != 1 {
		t.Fatalf("%d readings started by two reads, want 1 (single flight)", n)
	}
	f.runPending()
	if a := stAudioStatus(getConfig()); a.Available {
		t.Errorf("audio = %+v after a failed reading", a)
	}
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings right after a failure, want 0 (interval)", n)
	}
	*now = now.Add(fillEvery - time.Second)
	stAudioStatus(getConfig())
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings within the interval", n)
	}
	*now = now.Add(2 * time.Second)
	stAudioStatus(getConfig())
	if n := f.runPending(); n != 1 {
		t.Fatalf("%d readings after the interval, want 1", n)
	}
	if c := strings.Count(logs.String(), "audio state from the user session"); c != 1 {
		t.Errorf("failure logged %d times, want once:\n%s", c, logs)
	}

	f.err = nil
	*now = now.Add(fillEvery)
	stAudioStatus(getConfig())
	f.runPending()
	if a := stAudioStatus(getConfig()); !a.Available {
		t.Errorf("audio = %+v after the session answered", a)
	}
	if !strings.Contains(logs.String(), "audio state read from the user session again") {
		t.Errorf("recovery not logged:\n%s", logs)
	}
}

// Nobody logged in, or media control off: no reading, nothing available.
func TestFillNeedsUserAndMedia(t *testing.T) {
	f, _ := fillTestSetup(t, mediaOn())
	sys.sessionPresent = func() bool { return false }
	if a := stAudioStatus(getConfig()); a.Available {
		t.Errorf("audio = %+v with nobody logged in", a)
	}
	if m := stMediaStatus(getConfig()); m.Status != useraction.MediaNone {
		t.Errorf("media = %+v with nobody logged in", m)
	}
	observeTargetSession(0)
	startSessionFillNow()
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings with nobody logged in", n)
	}

	sys.sessionPresent = func() bool { return true }
	setConfig(Config{Port: 5001})
	stAudioStatus(getConfig())
	stMediaStatus(getConfig())
	requestSessionFills(true)
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings with media control off", n)
	}
}

// startSessionFillNow is startSessionFill without the delay.
func startSessionFillNow() {
	if sys.sessionPresent() {
		requestSessionFills(false)
	}
}

// A tray app reporting from the target session stays the source: no
// reading even without an audio sample. Once it has been quiet for
// trayQuietAfter and the sample is old, the service reads again.
func TestFillNotWhileTrayReports(t *testing.T) {
	f, now := fillTestSetup(t, mediaOn())
	heartbeat.Idle(12)
	stAudioStatus(getConfig())
	stMediaStatus(getConfig())
	if n := f.pendingCount(); n != 0 {
		t.Fatalf("%d readings while the tray app reports", n)
	}

	heartbeat.Audio(useraction.Audio{Volume: 50, Device: "헤드셋"}, *now)
	*now = now.Add(fillStaleAfter - time.Second)
	stAudioStatus(getConfig())
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings with a fresh tray sample", n)
	}
	*now = now.Add(2 * time.Second)
	stAudioStatus(getConfig())
	if n := f.runPending(); n != 1 {
		t.Fatalf("%d readings once the tray app went quiet, want 1", n)
	}
	if a := stAudioStatus(getConfig()); a.Volume == nil || *a.Volume != 30 {
		t.Errorf("audio = %+v, want the session's reading", a)
	}
}

// Without a tray app the sample is read again once it is older than
// fillStaleAfter, not before.
func TestFillRefreshesOldSample(t *testing.T) {
	f, now := fillTestSetup(t, mediaOn())
	events := tapDeviceEvents(t)
	recordAudioSample(useraction.Audio{Volume: 10, Device: "스피커"}, now.Add(-fillStaleAfter+time.Second))
	stAudioStatus(getConfig())
	if n := f.pendingCount(); n != 0 {
		t.Fatalf("%d readings of a recent sample", n)
	}
	*now = now.Add(2 * time.Second)
	stAudioStatus(getConfig())
	if n := f.runPending(); n != 1 {
		t.Fatalf("%d readings of an old sample, want 1", n)
	}
	// 10 → 30 is a change; recordAudioSample pushed it, once.
	if got := events(); len(got) != 1 || got[0] != "audio.changed" {
		t.Errorf("events = %q, want one audio.changed", got)
	}
}

// A target change reads the new session at once, interval or not, and a
// reading still running for the old session is followed by one more.
func TestFillOnTargetChange(t *testing.T) {
	f, _ := fillTestSetup(t, mediaOn())
	observeTargetSession(1)
	stAudioStatus(getConfig())
	f.runPending()
	if a := stAudioStatus(getConfig()); !a.Available {
		t.Fatalf("audio = %+v after the first reading", a)
	}

	f.audio = useraction.Audio{Volume: 70, Device: "원격 오디오"}
	observeTargetSession(2) // within the interval
	if n := f.runPending(); n != 2 {
		t.Fatalf("%d readings after the target changed, want 2 (audio, media)", n)
	}
	if a := stAudioStatus(getConfig()); a.Volume == nil || *a.Volume != 70 {
		t.Errorf("audio = %+v, want session 2's", a)
	}

	// The target moves while a reading runs: one more follows it, although
	// the first one has just stored a sample.
	f.calls = nil
	resetSessionFills()
	resetAudioSample()
	stAudioStatus(getConfig())
	if f.pendingCount() != 1 {
		t.Fatal("no reading started")
	}
	run := f.pending[0]
	f.pending = nil
	observeTargetSession(1)
	if n := f.pendingCount(); n != 1 {
		t.Fatalf("%d readings started by the change, want 1 (media; audio is busy)", n)
	}
	f.pending = nil
	run()
	if got := f.Calls(); len(got) != 2 || got[0] != "audio get" || got[1] != "audio get" {
		t.Errorf("runs = %q, want the audio reading and its repeat", got)
	}
	if f.pendingCount() != 0 {
		t.Error("the repeat started a reading of its own")
	}

	observeTargetSession(0)
	if n := f.pendingCount(); n != 0 {
		t.Errorf("%d readings when everybody logged off", n)
	}
}

// The media block gets the same treatment, with the opt-in applied.
func TestFillMedia(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want useraction.NowPlaying
	}{
		{"opt-in", optIn(), spotifyTrack},
		{"status only", mediaOn(), useraction.NowPlaying{Status: "playing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := fillTestSetup(t, tc.cfg)
			events := tapDeviceEvents(t)
			if m := stMediaStatus(getConfig()); m.Status != useraction.MediaNone {
				t.Fatalf("media = %+v before any reading", m)
			}
			f.runPending()
			if got := f.Calls(); len(got) != 1 || got[0] != "media "+useraction.MediaInfo {
				t.Errorf("runs = %q", got)
			}
			m := stMediaStatus(getConfig())
			got := useraction.NowPlaying{Status: m.Status, Title: m.Title, Artist: m.Artist, Album: m.Album, App: m.App}
			if got != tc.want {
				t.Errorf("media = %+v, want %+v", got, tc.want)
			}
			if ev := events(); len(ev) != 1 || ev[0] != "media.changed" {
				t.Errorf("events = %q, want one media.changed", ev)
			}
		})
	}
}

// Nothing playing: stored, but "none" stays "none" and nothing is pushed.
func TestFillMediaNothingPlaying(t *testing.T) {
	f, _ := fillTestSetup(t, optIn())
	events := tapDeviceEvents(t)
	f.np = useraction.NowPlaying{Status: useraction.MediaNone}
	stMediaStatus(getConfig())
	f.runPending()
	if _, ok := currentMedia(); !ok {
		t.Error("the reading was not stored")
	}
	if ev := events(); len(ev) != 0 {
		t.Errorf("events = %q, want none", ev)
	}
}

// The rc15 machine end to end: console session 1 unlocked and targeted,
// the tray app in RDP session 2. Its heartbeats are ignored and do not
// count as a tray report; the service reads session 1 itself, and the
// status the driver polls says audio is available.
func TestFillTargetWithoutTray(t *testing.T) {
	f, _ := fillTestSetup(t, mediaOn())
	idleSetup(t)
	active := uint32(windows.WTSActive)
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}, {ID: 2, State: active}}, nil, map[uint32]error{1: nil, 2: nil})
	fakeLocks(t, map[uint32]bool{1: false})
	sys.sessionPresent = userSessionPresent
	savedTarget := heartbeat.target
	heartbeat.target = targetUserSession
	t.Cleanup(func() { heartbeat.target = savedTarget })

	if status, reason := heartbeatStatus(t, `{`+fullBeat+`,"session_id":2}`); status != "ignored" {
		t.Fatalf("session 2's heartbeat: %s/%s, want ignored", status, reason)
	}
	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	if a, _ := got["audio"].(map[string]any); a["available"] != false {
		t.Errorf("first status audio = %v, want unavailable (the reading runs in the background)", a)
	}
	if n := f.runPending(); n != 2 {
		t.Fatalf("%d readings, want 2 (audio, media)", n)
	}
	got = stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	a, _ := got["audio"].(map[string]any)
	if a["available"] != true || a["volume"] != float64(30) || a["device"] != "스피커" {
		t.Errorf("status audio = %v, want session 1's reading", a)
	}
	if m, _ := got["media"].(map[string]any); m["status"] != "playing" {
		t.Errorf("status media = %v", m)
	}
}
