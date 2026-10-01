package service

// Tests for keep-awake (#111, docs/design/media-notify.md §12).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/webui"
)

// fakeAwake is the test harness around an awakeController: a settable
// clock, timers that fire only when the test says so, a recorder in place
// of SetThreadExecutionState and one for the change hook.
type fakeAwake struct {
	ctl *power.Awake

	mu       sync.Mutex
	now      time.Time
	timers   []*fakeTimer
	calls    []uint32
	changes  []awakeView
	failNext error
	display  bool
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func newFakeAwake() *fakeAwake {
	fa := &fakeAwake{now: time.Date(2026, 9, 30, 14, 0, 0, 0, time.Local)}
	fa.ctl = power.NewAwake(power.AwakeHooks{
		Now: func() time.Time {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			return fa.now
		},
		AfterFunc: func(d time.Duration, f func()) func() bool {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			t := &fakeTimer{d: d, f: f}
			fa.timers = append(fa.timers, t)
			return func() bool { t.stopped = true; return true }
		},
		SetState: func(flags uint32) error {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			if err := fa.failNext; err != nil {
				fa.failNext = nil
				return err
			}
			fa.calls = append(fa.calls, flags)
			return nil
		},
		KeepDisplay: func() bool {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			return fa.display
		},
		OnChange: func(v awakeView) {
			fa.mu.Lock()
			fa.changes = append(fa.changes, v)
			fa.mu.Unlock()
		},
	})
	return fa
}

func (fa *fakeAwake) advance(d time.Duration) {
	fa.mu.Lock()
	fa.now = fa.now.Add(d)
	fa.mu.Unlock()
}

// stubAwake installs a fake controller as the service's for one test.
func stubAwake(t *testing.T) *fakeAwake {
	t.Helper()
	fa := newFakeAwake()
	awakeMu.Lock()
	orig := awake
	awake = fa.ctl
	awakeMu.Unlock()
	t.Cleanup(func() {
		awakeMu.Lock()
		awake = orig
		awakeMu.Unlock()
	})
	return fa
}

// The state machine itself is tested in service/power.

// ---- /st/v1 ----------------------------------------------------------------

func TestSTCommandAwake(t *testing.T) {
	stSetup(t, Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 300, Awake: AwakeConfig{DefaultMinutes: 45}})
	fa := stubAwake(t)
	events := captureNotifications(t)

	// No value: awake.default_minutes.
	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"awake"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("awake: %d %s", w.Code, w.Body.String())
	}
	got := stJSON(t, w)
	if got["accepted"] != true || got["executed"] != true {
		t.Errorf("reply = %v", got)
	}
	aw, _ := got["awake"].(map[string]any)
	if aw["on"] != true || aw["until"] != fa.now.Add(45*time.Minute).Format(time.RFC3339) {
		t.Errorf("reply awake = %v", aw)
	}
	// Not a power command: no grace schedule, no remote notification.
	if s := getSchedule(); s["active"] != false {
		t.Errorf("awake went through the grace period: %v", s)
	}
	expectNoNotification(t, events)

	// value 0: until turned off.
	got = stJSON(t, stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"awake","value":0}`))
	if aw, _ := got["awake"].(map[string]any); aw["on"] != true || aw["until"] != "" {
		t.Errorf("value 0: awake = %v", aw)
	}
	got = stJSON(t, stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"AWAKE","value":1440}`))
	if aw, _ := got["awake"].(map[string]any); aw["until"] != fa.now.Add(24*time.Hour).Format(time.RFC3339) {
		t.Errorf("value 1440: awake = %v", aw)
	}

	got = stJSON(t, stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"awakeoff"}`))
	if aw, _ := got["awake"].(map[string]any); aw["on"] != false {
		t.Errorf("awakeoff: awake = %v", aw)
	}
	if fa.ctl.View().On {
		t.Error("awakeoff left it on")
	}

	for _, tc := range []struct{ name, body string }{
		{"negative value", `{"command":"awake","value":-1}`},
		{"value too large", `{"command":"awake","value":1441}`},
		{"value not a number", `{"command":"awake","value":"60"}`},
		{"minutes instead of value", `{"command":"awake","minutes":30}`},
	} {
		if w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", tc.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", tc.name, w.Code)
		}
	}
	if fa.ctl.View().On {
		t.Error("a rejected command turned keep-awake on")
	}
}

func TestSTCommandAwakeSetterFailureIs500(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	fa := stubAwake(t)
	fa.failNext = errors.New("nope")
	if w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"awake","value":10}`); w.Code != http.StatusInternalServerError {
		t.Errorf("failure: %d, want 500", w.Code)
	}
}

// ---- /api/awake ------------------------------------------------------------

func awakeAPI(t *testing.T, method, body string) (int, webui.AwakeBody) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/awake", nil)
	} else {
		r = httptest.NewRequest(method, "/api/awake", strings.NewReader(body))
	}
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	webAPI(w, r)
	var out webui.AwakeBody
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAwakeAPI(t *testing.T) {
	initLogger()
	prev := getConfig()
	setConfig(Config{Port: 5001, Awake: AwakeConfig{DefaultMinutes: 60}})
	t.Cleanup(func() { setConfig(prev) })
	fa := stubAwake(t)

	code, v := awakeAPI(t, "GET", "")
	if code != http.StatusOK || v.On || v.DefaultMinutes != 60 || v.Status != "ok" {
		t.Fatalf("GET = %d %+v", code, v)
	}
	code, v = awakeAPI(t, "POST", `{"minutes":120}`)
	if code != http.StatusOK || !v.On || v.RemainingSeconds != 7200 {
		t.Fatalf("POST 120 = %d %+v", code, v)
	}
	fa.advance(30 * time.Minute)
	if _, v = awakeAPI(t, "GET", ""); v.RemainingSeconds != 5400 {
		t.Errorf("remaining after 30 min = %d", v.RemainingSeconds)
	}
	// No minutes: the configured default.
	if _, v = awakeAPI(t, "POST", `{}`); v.Until != fa.now.Add(time.Hour).Format(time.RFC3339) {
		t.Errorf("POST {} until = %q", v.Until)
	}
	if _, v = awakeAPI(t, "POST", `{"minutes":0}`); !v.On || v.Until != "" || v.RemainingSeconds != 0 {
		t.Errorf("POST 0 = %+v", v)
	}
	if code, _ := awakeAPI(t, "POST", `{"minutes":5000}`); code != http.StatusBadRequest {
		t.Errorf("POST 5000 = %d, want 400", code)
	}
	if code, v = awakeAPI(t, "DELETE", ""); code != http.StatusOK || v.On {
		t.Errorf("DELETE = %d %+v", code, v)
	}

	// CSRF header required for changes.
	r := httptest.NewRequest("POST", "/api/awake", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST without the CSRF header = %d", w.Code)
	}
}

// ---- Telegram --------------------------------------------------------------

func TestTelegramAwake(t *testing.T) {
	initLogger()
	prev := getConfig()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"}, Awake: AwakeConfig{DefaultMinutes: 60}})
	t.Cleanup(func() { setConfig(prev) })
	fa := stubAwake(t)
	h := tgCtl
	ctx := context.Background()

	reply, _, err := h.HandleCommand(ctx, "42", "awake", []string{"30"})
	if err != nil || !strings.Contains(reply, "잠들지 않기 켜짐 · 14:30까지") {
		t.Errorf("/awake 30 = %q, %v", reply, err)
	}
	status, _, _ := h.HandleCommand(ctx, "42", "status", nil)
	if !strings.Contains(status, "잠들지 않기: 켜짐 · 14:30까지") {
		t.Errorf("/status lacks the awake line:\n%s", status)
	}

	// Across midnight the day is named.
	reply, _, _ = h.HandleCommand(ctx, "42", "awake", []string{"720"})
	if !strings.Contains(reply, "내일 02:00까지") {
		t.Errorf("/awake 720 = %q", reply)
	}
	reply, _, _ = h.HandleCommand(ctx, "42", "awake", []string{"0"})
	if !strings.Contains(reply, "끌 때까지") {
		t.Errorf("/awake 0 = %q", reply)
	}
	reply, _, _ = h.HandleCommand(ctx, "42", "awake", nil)
	if !strings.Contains(reply, "15:00까지") || !fa.ctl.View().On {
		t.Errorf("/awake (default 60) = %q", reply)
	}

	reply, _, _ = h.HandleCommand(ctx, "42", "awake", []string{"off"})
	if !strings.Contains(reply, "잠들지 않기 꺼짐") || fa.ctl.View().On {
		t.Errorf("/awake off = %q", reply)
	}
	reply, _, _ = h.HandleCommand(ctx, "42", "awake", []string{"off"})
	if !strings.Contains(reply, "이미 꺼져") {
		t.Errorf("/awake off again = %q", reply)
	}
	status, _, _ = h.HandleCommand(ctx, "42", "status", nil)
	if !strings.Contains(status, "잠들지 않기: 꺼짐") {
		t.Errorf("/status off:\n%s", status)
	}

	reply, _, err = h.HandleCommand(ctx, "42", "awake", []string{"soon"})
	if err == nil || !strings.Contains(reply, "/awake off") {
		t.Errorf("/awake soon = %q, %v", reply, err)
	}

	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "en"}})
	fa.ctl.TurnOn(30)
	status, _, _ = h.HandleCommand(ctx, "42", "status", nil)
	if !strings.Contains(status, "Keep awake: on · until 14:30") {
		t.Errorf("en /status:\n%s", status)
	}
}
