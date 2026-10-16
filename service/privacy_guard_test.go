package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/privacy"
)

// privacyStub is the target user's consent store in the tests: one app
// per device, whose LastUsedTimeStart/Stop the test sets. It never touches
// the registry.
type privacyStub struct {
	mu       sync.Mutex
	cam, mic [2]uint64
}

// privacyFake is the stub of the running test (stubPrivacy), for the
// golden push trigger.
var privacyFake *privacyStub

// fileTimeOf is the FILETIME of at.
func fileTimeOf(at time.Time) uint64 { return uint64(at.UnixNano()/100) + 116444736000000000 }

// set makes a device in use since start (stop zero) or idle since stop.
func (s *privacyStub) set(device string, start, stop time.Time) {
	var v [2]uint64
	if !start.IsZero() {
		v[0] = fileTimeOf(start)
	}
	if !stop.IsZero() {
		v[1] = fileTimeOf(stop)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if device == privacy.Camera {
		s.cam = v
	} else {
		s.mic = v
	}
}

func (s *privacyStub) SubKeys(path string) ([]string, error) {
	if strings.HasSuffix(path, `\webcam`) || strings.HasSuffix(path, `\microphone`) {
		return []string{"app"}, nil
	}
	return nil, privacy.ErrNotFound
}

func (s *privacyStub) Times(path string) (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasSuffix(path, `\webcam\app`):
		return s.cam[0], s.cam[1], nil
	case strings.HasSuffix(path, `\microphone\app`):
		return s.mic[0], s.mic[1], nil
	}
	return 0, 0, privacy.ErrNotFound
}

func (s *privacyStub) Close() error { return nil }

// stubPrivacy points the monitor at a fresh stub (nothing ever used) and
// forgets the last reading; everything is restored afterwards.
func stubPrivacy(t *testing.T) *privacyStub {
	t.Helper()
	s := &privacyStub{}
	savedUser, savedOpen, savedZone := privacyMon.User, privacyMon.Open, privacyZone
	privacyMon.User = func() (string, error) { return "S-1-5-21-1000", nil }
	privacyMon.Open = func(string) (privacy.Hive, error) { return s, nil }
	privacyZone = func() *time.Location { return goldenZone }
	privacyFake = s
	resetPrivacy()
	t.Cleanup(func() {
		privacyMon.User, privacyMon.Open, privacyZone = savedUser, savedOpen, savedZone
		privacyFake = nil
		resetPrivacy()
	})
	return s
}

// withPrivacy sets the privacy settings on the live config.
func withPrivacy(t *testing.T, enabled bool, guard string) {
	t.Helper()
	saved := getConfig()
	t.Cleanup(func() { setConfig(saved) })
	cfg := saved
	cfg.Privacy.Enabled, cfg.Privacy.Guard = enabled, guard
	setConfig(cfg)
}

func TestPrivacyStatusBlock(t *testing.T) {
	s := stubPrivacy(t)
	withPrivacy(t, false, "warn")
	if p := stPrivacyStatus(getConfig()); p != nil {
		t.Fatalf("off: %+v", p)
	}
	withPrivacy(t, true, "warn")
	// Before the first scan: unknown.
	if p := stPrivacyStatus(getConfig()); p == nil || p.Camera != privacy.Unknown || p.Since != "" {
		t.Fatalf("before a scan: %+v", p)
	}
	s.set(privacy.Camera, goldenNow, time.Time{})
	refreshPrivacy(getConfig())
	p := stPrivacyStatus(getConfig())
	if p.Camera != privacy.InUse || p.Microphone != privacy.Idle || p.Since != "2026-10-01T21:00:00+09:00" {
		t.Fatalf("camera on: %+v", p)
	}
}

// privacy.changed reaches the push tap on a change, and when the option is
// turned on or off; not on a scan that changes nothing.
func TestPrivacyChangedEvents(t *testing.T) {
	s := stubPrivacy(t)
	stPushSetup(t, getConfig())
	cb := newCallbackServer(t)
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	subscribeTo(t, cb, 600)

	withPrivacy(t, true, "warn")
	refreshPrivacy(getConfig()) // turned on
	if body := cb.wait(t); body["type"] != "privacy.changed" {
		t.Fatalf("pushed %v", body["type"])
	}
	refreshPrivacy(getConfig()) // nothing new
	s.set(privacy.Microphone, goldenNow, time.Time{})
	refreshPrivacy(getConfig())
	body := cb.wait(t)
	data, _ := body["data"].(map[string]any)
	if body["type"] != "privacy.changed" || data["microphone"] != "in_use" {
		t.Fatalf("pushed %v %v", body["type"], body["data"])
	}
	withPrivacy(t, false, "warn")
	refreshPrivacy(getConfig()) // turned off: data null, no block
	body = cb.wait(t)
	st, _ := body["status"].(map[string]any)
	if body["type"] != "privacy.changed" || body["data"] != nil || st["privacy"] != nil {
		t.Fatalf("off push: %v data=%v privacy=%v", body["type"], body["data"], st["privacy"])
	}
	if feats, _ := st["features"].([]any); slices.Contains(feats, any("privacy")) {
		t.Fatalf("features still list privacy: %v", feats)
	}
}

func TestPrivacyGuardedCallers(t *testing.T) {
	cases := []struct {
		name, from string
		origin     scheduleOrigin
		want       bool
	}{
		{"shutdown", "10.0.0.5", originSmartThings, true},
		{"restart", "10.0.0.5", originRemote, true},
		{"suspend", "telegram", originTelegram, true},
		{"hibernate", "webui", originUI, true},
		{"shutdown", "app", originUI, false},
		{"shutdown", "tray", originUI, false},
		{"shutdown", "toast", originUI, false},
		{"lock", "10.0.0.5", originSmartThings, false},
		{"forceshutdown", "10.0.0.5", originSmartThings, false},
	}
	for _, c := range cases {
		if got := privacyGuarded(c.name, c.from, c.origin); got != c.want {
			t.Errorf("%s from %s (%s): guarded %v, want %v", c.name, c.from, c.origin, got, c.want)
		}
	}
}

// block: a remote power command is refused (not run, not recorded) and
// announced as privacy.blocked; the desktop app's own press still runs.
func TestPrivacyBlockRefusesRemote(t *testing.T) {
	s := stubPrivacy(t)
	s.set(privacy.Camera, goldenNow, time.Time{})
	executed := stubCommand(t, "shutdown")
	events := captureNotifications(t)
	withPrivacy(t, true, "block")
	cfg := getConfig()
	cfg.ShutdownGrace = false
	setConfig(cfg)
	lastRemoteMu.Lock()
	savedRemote := lastRemote
	lastRemote = remoteRecord{}
	lastRemoteMu.Unlock()
	t.Cleanup(func() {
		lastRemoteMu.Lock()
		lastRemote = savedRemote
		lastRemoteMu.Unlock()
	})

	_, err := dispatchCommand("shutdown", "10.0.0.5", originSmartThings, dispatchDefault)
	var inUse *privacy.DeviceInUseError
	if !errors.As(err, &inUse) || !slices.Equal(inUse.Devices, []string{"camera"}) {
		t.Fatalf("err = %v", err)
	}
	expectNotExecuted(t, executed, "a blocked shutdown")
	ev := expectNotification(t, events, "privacy.blocked")
	if ev.Fields["in_use"] != "camera" || ev.Fields["command"] != "shutdown" {
		t.Errorf("fields %v", ev.Fields)
	}
	if lr := getLastRemote(); lr.Command != "" {
		t.Errorf("a refused command was recorded: %+v", lr)
	}

	// The WebUI is guarded too; the app at the PC is not.
	if _, err := dispatchCommand("shutdown", "webui", originUI, dispatchImmediate); !errors.As(err, &inUse) {
		t.Fatalf("webui: err = %v", err)
	}
	expectNotification(t, events, "privacy.blocked")
	if _, err := dispatchCommand("shutdown", "app", originUI, dispatchImmediate); err != nil {
		t.Fatalf("app: %v", err)
	}
	expectExecuted(t, executed, "the app's shutdown")

	// Telegram: refused without a notification (the bot replies itself).
	if _, err := dispatchCommand("shutdown", "telegram", originTelegram, dispatchImmediate); !errors.As(err, &inUse) {
		t.Fatalf("telegram: err = %v", err)
	}
	expectNoNotification(t, events)

	// Nothing in use: runs.
	s.set(privacy.Camera, goldenNow.Add(-time.Hour), goldenNow)
	if _, err := dispatchCommand("shutdown", "10.0.0.5", originSmartThings, dispatchDefault); err != nil {
		t.Fatalf("idle: %v", err)
	}
	expectExecuted(t, executed, "shutdown with the camera off")
}

// warn: the command runs, and its notification says what is in use; with
// the grace period on, remote.grace_scheduled carries it and the schedule
// view lists it for the toast.
func TestPrivacyWarnAnnotates(t *testing.T) {
	s := stubPrivacy(t)
	s.set(privacy.Camera, goldenNow, time.Time{})
	s.set(privacy.Microphone, goldenNow, time.Time{})
	executed := stubCommand(t, "restart")
	stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	withPrivacy(t, true, "warn")
	cfg := getConfig()
	cfg.ShutdownGrace, cfg.GraceSeconds = false, 30
	setConfig(cfg)
	t.Cleanup(func() { cancelScheduleBy("api") })

	if _, err := dispatchCommand("restart", "10.0.0.5", originSmartThings, dispatchDefault); err != nil {
		t.Fatal(err)
	}
	expectExecuted(t, executed, "restart under warn")
	if ev := expectNotification(t, events, "remote.received"); ev.Fields["in_use"] != "camera,microphone" {
		t.Errorf("received fields %v", ev.Fields)
	}

	if d, err := dispatchCommand("restart", "10.0.0.5", originSmartThings, dispatchGrace); err != nil || d != 30*time.Second {
		t.Fatalf("grace: %v %v", d, err)
	}
	if ev := expectNotification(t, events, "remote.grace_scheduled"); ev.Fields["in_use"] != "camera,microphone" {
		t.Errorf("grace fields %v", ev.Fields)
	}
	sch := getSchedule()
	if in, _ := sch["in_use"].([]string); !slices.Equal(in, []string{"camera", "microphone"}) || sch["held"] != nil {
		t.Errorf("schedule view %v", sch)
	}
}

// The scheduler's Hold: warn pushes a schedule back once by the grace
// period, then lets it run; block drops it; nothing in use, or the guard
// off, runs it.
func TestPrivacyHold(t *testing.T) {
	s := stubPrivacy(t)
	s.set(privacy.Camera, goldenNow, time.Time{})
	events := captureNotifications(t)
	withPrivacy(t, true, "warn")
	cfg := getConfig()
	cfg.GraceSeconds = 45
	setConfig(cfg)

	task := power.Task{Command: "shutdown", Origin: originUI}
	if d, drop := privacyHold(task); drop || d != 45*time.Second {
		t.Fatalf("warn, first: %v %v", d, drop)
	}
	ev := expectNotification(t, events, "privacy.extended")
	if ev.Fields["in_use"] != "camera" || ev.Fields["delay"] != "45 sec" || ev.Fields["origin"] != "ui" {
		t.Errorf("extended fields %v", ev.Fields)
	}
	task.Held = 1
	if d, drop := privacyHold(task); drop || d != 0 {
		t.Fatalf("warn, held once: %v %v", d, drop)
	}
	expectNoNotification(t, events)

	withPrivacy(t, true, "block")
	if d, drop := privacyHold(task); !drop || d != 0 {
		t.Fatalf("block: %v %v", d, drop)
	}
	if ev := expectNotification(t, events, "privacy.blocked"); ev.Fields["from"] != "schedule" {
		t.Errorf("blocked fields %v", ev.Fields)
	}
	// Not a power command: never held.
	if d, drop := privacyHold(power.Task{Command: "lock"}); drop || d != 0 {
		t.Fatalf("lock held: %v %v", d, drop)
	}
	withPrivacy(t, true, "off")
	if d, drop := privacyHold(task); drop || d != 0 {
		t.Fatalf("guard off: %v %v", d, drop)
	}
	withPrivacy(t, false, "block")
	if d, drop := privacyHold(task); drop || d != 0 {
		t.Fatalf("detection off: %v %v", d, drop)
	}
}

// The WebUI's /api/command answers a refusal with 409 device_in_use (its
// page shows the reason); the desktop app's press runs. GET /api/privacy
// is the sharing tab's state line.
func TestPrivacyWebAPI(t *testing.T) {
	s := stubPrivacy(t)
	s.set(privacy.Camera, goldenNow, time.Time{})
	executed := stubCommand(t, "shutdown")
	saved := getConfig()
	t.Cleanup(func() { setConfig(saved) })
	setConfig(Config{Port: 5001, Privacy: config.PrivacyConfig{Enabled: true, Guard: config.PrivacyGuardBlock}})

	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/command", strings.NewReader(body))
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		w := httptest.NewRecorder()
		webAPI(w, r)
		return w
	}
	w := post(`{"command":"shutdown","by":"webui"}`)
	var reply map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if w.Code != http.StatusConflict || reply["error"] != "device_in_use" {
		t.Fatalf("webui: %d %s", w.Code, w.Body.String())
	}
	expectNotExecuted(t, executed, "the WebUI's blocked shutdown")
	if w := post(`{"command":"shutdown","by":"app"}`); w.Code != http.StatusOK {
		t.Fatalf("app: %d %s", w.Code, w.Body.String())
	}
	expectExecuted(t, executed, "the app's shutdown")
	if w := post(`{"command":"nope","by":"webui"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", w.Code)
	}

	refreshPrivacy(getConfig())
	w = httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("GET", "/api/privacy", nil))
	var body struct {
		Enabled bool
		Guard   string
		Privacy *struct{ Camera string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.Enabled || body.Guard != "block" || body.Privacy == nil || body.Privacy.Camera != "in_use" {
		t.Fatalf("GET /api/privacy: %d %s", w.Code, w.Body.String())
	}
}

// A schedule the guard drops at its time ends through the usual cancel
// path (by device_in_use) and never runs.
func TestPrivacyBlockDropsSchedule(t *testing.T) {
	s := stubPrivacy(t)
	s.set(privacy.Microphone, goldenNow, time.Time{})
	executed := stubCommand(t, "shutdown")
	events := captureNotifications(t)
	withPrivacy(t, true, "block")
	t.Cleanup(func() { cancelScheduleBy("api") })

	if err := setSchedule("shutdown", 20*time.Millisecond, originTelegram); err != nil {
		t.Fatal(err)
	}
	if ev := expectNotification(t, events, "privacy.blocked"); ev.Fields["in_use"] != "microphone" || ev.Fields["origin"] != "telegram" {
		t.Errorf("blocked fields %v", ev.Fields)
	}
	expectNotExecuted(t, executed, "a dropped schedule")
	if sch := getSchedule(); sch["active"] != false {
		t.Errorf("schedule still active: %v", sch)
	}
}
