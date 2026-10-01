package service

// Tests for keep-awake (#111, docs/design/media-notify.md §12).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/webui"
)

// fakeAwake is the test harness around an awakeController: a settable
// clock, timers that fire only when the test says so, a recorder in place
// of SetThreadExecutionState and one for the change hook.
type fakeAwake struct {
	ctl *awakeController

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
	fa.ctl = &awakeController{
		now: func() time.Time {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			return fa.now
		},
		afterFunc: func(d time.Duration, f func()) func() bool {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			t := &fakeTimer{d: d, f: f}
			fa.timers = append(fa.timers, t)
			return func() bool { t.stopped = true; return true }
		},
		setState: func(flags uint32) error {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			if err := fa.failNext; err != nil {
				fa.failNext = nil
				return err
			}
			fa.calls = append(fa.calls, flags)
			return nil
		},
		keepDisplay: func() bool {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			return fa.display
		},
		onChange: func(v awakeView) {
			fa.mu.Lock()
			fa.changes = append(fa.changes, v)
			fa.mu.Unlock()
		},
		reqs: make(chan awakeReq),
	}
	return fa
}

func (fa *fakeAwake) advance(d time.Duration) {
	fa.mu.Lock()
	fa.now = fa.now.Add(d)
	fa.mu.Unlock()
}

// fireLast runs the newest timer's callback, as time.AfterFunc would.
func (fa *fakeAwake) fireLast() {
	fa.mu.Lock()
	t := fa.timers[len(fa.timers)-1]
	fa.mu.Unlock()
	t.f()
}

func (fa *fakeAwake) callLog() []uint32 {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]uint32(nil), fa.calls...)
}

func (fa *fakeAwake) changeLog() []awakeView {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]awakeView(nil), fa.changes...)
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

const (
	flagsOn        = esContinuous | esSystemRequired
	flagsOnDisplay = esContinuous | esSystemRequired | esDisplayRequired
)

func equalFlags(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- state machine ---------------------------------------------------------

func TestAwakeOnExtendOffAndExpiry(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl

	if v := c.View(); v.On {
		t.Fatalf("starts on: %+v", v)
	}

	// On for an hour: the owner thread holds SYSTEM_REQUIRED.
	v, err := c.TurnOn(60)
	if err != nil {
		t.Fatal(err)
	}
	wantUntil := fa.now.Add(time.Hour)
	if !v.On || !v.Until.Equal(wantUntil) {
		t.Fatalf("TurnOn(60) = %+v, want on until %v", v, wantUntil)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn}) {
		t.Fatalf("setState calls = %#x", got)
	}
	if len(fa.timers) != 1 || fa.timers[0].d != time.Hour {
		t.Fatalf("timers = %+v", fa.timers)
	}

	// Extend: a new period from now, the old timer stopped, no second
	// SetThreadExecutionState (the flags did not change).
	fa.advance(10 * time.Minute)
	v, _ = c.TurnOn(120)
	if !v.Until.Equal(fa.now.Add(2 * time.Hour)) {
		t.Errorf("extend: until = %v", v.Until)
	}
	if !fa.timers[0].stopped {
		t.Error("the first period's timer was not stopped")
	}
	if got := fa.callLog(); len(got) != 1 {
		t.Errorf("extend re-applied the same flags: %#x", got)
	}
	// The stale timer firing late does nothing.
	fa.timers[0].f()
	if !c.View().On {
		t.Fatal("a stale timer turned keep-awake off")
	}

	// Expiry: the current timer fires once its end has passed.
	fa.advance(2 * time.Hour)
	fa.fireLast()
	if v := c.View(); v.On {
		t.Fatalf("still on after expiry: %+v", v)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("expiry did not clear with ES_CONTINUOUS: %#x", got)
	}
	changes := fa.changeLog()
	if len(changes) != 3 || changes[0].On != true || changes[1].On != true || changes[2].On != false {
		t.Errorf("changes = %+v, want on, on (extend), off", changes)
	}

	// Off while off is not a change.
	if _, wasOn, err := c.TurnOff(); wasOn || err != nil {
		t.Errorf("TurnOff while off: wasOn=%v err=%v", wasOn, err)
	}
	if len(fa.changeLog()) != 3 {
		t.Error("TurnOff while off emitted a change")
	}
}

func TestAwakeIndefiniteAndExplicitOff(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl

	v, err := c.TurnOn(0)
	if err != nil || !v.On || !v.Until.IsZero() {
		t.Fatalf("TurnOn(0) = %+v, %v; want on with no end", v, err)
	}
	if len(fa.timers) != 0 {
		t.Errorf("an indefinite period armed a timer: %+v", fa.timers)
	}
	if w := v.Wire(); w.On != true || w.Until != "" {
		t.Errorf("wire = %+v, want {on:true until:\"\"}", w)
	}
	// A day later it is still on.
	fa.advance(48 * time.Hour)
	c.Tick()
	if !c.View().On {
		t.Fatal("an indefinite period ended on its own")
	}

	_, wasOn, err := c.TurnOff()
	if !wasOn || err != nil {
		t.Fatalf("TurnOff: wasOn=%v err=%v", wasOn, err)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("setState calls = %#x", got)
	}
	if w := c.View().Wire(); w.On || w.Until != "" {
		t.Errorf("off wire = %+v", w)
	}
}

// The monotonic timer stops while the PC sleeps; the wall clock does not.
// A period that ran out during a manual suspend ends on the next tick or
// status read, not an hour late.
func TestAwakeWallClockExpiry(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl
	c.TurnOn(30)

	fa.advance(31 * time.Minute) // the timer has not fired
	if v := c.View(); v.On {
		t.Fatalf("View reported a period that is over: %+v", v)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("setState calls = %#x", got)
	}
	// The timer firing afterwards finds nothing to do.
	fa.fireLast()
	if got := fa.changeLog(); len(got) != 2 {
		t.Errorf("changes = %+v, want on then one off", got)
	}

	c.TurnOn(30)
	fa.advance(45 * time.Minute)
	c.Tick()
	if c.View().On {
		t.Error("Tick did not end an expired period")
	}
}

// A timer that fires before the wall-clock end (the clock was set back)
// re-arms for the rest instead of ending early.
func TestAwakeEarlyTimerRearms(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl
	c.TurnOn(60)
	fa.advance(50 * time.Minute)
	fa.fireLast()
	if !c.View().On {
		t.Fatal("an early timer ended the period")
	}
	if last := fa.timers[len(fa.timers)-1]; last.d != 10*time.Minute {
		t.Errorf("re-armed for %v, want 10m", last.d)
	}
}

func TestAwakeKeepDisplayFlag(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	fa.display = true
	c := fa.ctl
	c.TurnOn(0)
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOnDisplay}) {
		t.Fatalf("keep_display: setState calls = %#x", got)
	}
	// Turning keep_display off in the settings reaches a running period on
	// the next tick.
	fa.mu.Lock()
	fa.display = false
	fa.mu.Unlock()
	c.Tick()
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOnDisplay, flagsOn}) {
		t.Errorf("after the settings change: setState calls = %#x", got)
	}
	c.Tick()
	if got := fa.callLog(); len(got) != 2 {
		t.Errorf("an unchanged tick re-applied: %#x", got)
	}
}

func TestAwakeRejectsBadMinutesAndSetterFailure(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl
	for _, m := range []int{-1, config.AwakeMaxMinutes + 1} {
		if _, err := c.TurnOn(m); err == nil {
			t.Errorf("TurnOn(%d) accepted", m)
		}
	}
	if _, err := c.TurnOn(config.AwakeMaxMinutes); err != nil {
		t.Errorf("TurnOn(%d) rejected: %v", config.AwakeMaxMinutes, err)
	}
	c.TurnOff()

	fa.mu.Lock()
	fa.failNext = errors.New("access denied")
	fa.mu.Unlock()
	if _, err := c.TurnOn(30); err == nil {
		t.Fatal("a failing SetThreadExecutionState was not reported")
	}
	if c.View().On {
		t.Error("state says on although Windows refused the request")
	}
}

// Shutdown clears the request without an event; nothing carries over.
func TestAwakeShutdownReleases(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl
	c.TurnOn(60)
	c.Shutdown()
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("setState calls = %#x", got)
	}
	if c.View().On {
		t.Error("still on after Shutdown")
	}
	if got := fa.changeLog(); len(got) != 1 {
		t.Errorf("Shutdown emitted a change: %+v", got)
	}
}

// Every SetThreadExecutionState call happens on one goroutine locked to
// its thread; the controller never calls the setter itself.
func TestAwakeSetterRunsOnOwnerThread(t *testing.T) {
	initLogger()
	fa := newFakeAwake()
	c := fa.ctl
	var mu sync.Mutex
	owners := map[string]bool{}
	inner := c.setState
	c.setState = func(flags uint32) error {
		mu.Lock()
		owners[goroutineID()] = true
		mu.Unlock()
		return inner(flags)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				c.TurnOn(10)
			} else {
				c.TurnOff()
			}
		}(i)
	}
	wg.Wait()
	c.TurnOn(10)
	c.TurnOff()
	if len(owners) != 1 {
		t.Errorf("setState ran on %d goroutines, want 1", len(owners))
	}
}

// goroutineID parses "goroutine N [" from the stack header.
func goroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	s := strings.TrimPrefix(string(buf), "goroutine ")
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// ---- /st/v1 ----------------------------------------------------------------

func TestSTStatusAwakeAndFeatures(t *testing.T) {
	stSetup(t, Config{Port: 5001, Awake: AwakeConfig{DefaultMinutes: 60}})
	fa := stubAwake(t)

	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	aw, ok := got["awake"].(map[string]any)
	if !ok || aw["on"] != false || aw["until"] != "" {
		t.Errorf("awake = %v, want {on:false until:\"\"}", got["awake"])
	}
	features, ok := got["features"].([]any)
	if !ok {
		t.Fatalf("features = %v, want an array", got["features"])
	}
	has := false
	for _, f := range features {
		has = has || f == "awake"
	}
	if !has {
		t.Errorf("features = %v, want awake in it", features)
	}

	fa.ctl.TurnOn(30)
	got = stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	aw = got["awake"].(map[string]any)
	want := fa.now.Add(30 * time.Minute).Format(time.RFC3339)
	if aw["on"] != true || aw["until"] != want {
		t.Errorf("awake = %v, want on until %s", aw, want)
	}
}

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

func TestAwakeChangedIsPushed(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	fa := stubAwake(t)
	fa.ctl.onChange = emitAwakeChanged
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	fa.ctl.TurnOn(0)
	got := cb.wait(t)
	if got["type"] != "awake.changed" {
		t.Fatalf("type = %v", got["type"])
	}
	if data, _ := got["data"].(map[string]any); data["on"] != "true" || data["until"] != "" {
		t.Errorf("data = %v", got["data"])
	}
	status, _ := got["status"].(map[string]any)
	if aw, _ := status["awake"].(map[string]any); aw["on"] != true {
		t.Errorf("status.awake = %v", status["awake"])
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

func TestParseAwakeArg(t *testing.T) {
	prev := getConfig()
	setConfig(Config{Awake: AwakeConfig{DefaultMinutes: 90}})
	t.Cleanup(func() { setConfig(prev) })
	for _, tc := range []struct {
		args    []string
		minutes int
		off, ok bool
	}{
		{nil, 90, false, true},
		{[]string{"off"}, 0, true, true},
		{[]string{"OFF"}, 0, true, true},
		{[]string{"30"}, 30, false, true},
		{[]string{"0"}, 0, false, true},
		{[]string{"1440"}, 1440, false, true},
		{[]string{"1441"}, 0, false, false},
		{[]string{"-1"}, 0, false, false},
		{[]string{"2h"}, 0, false, false},
		{[]string{"on"}, 0, false, false},
	} {
		m, off, ok := parseAwakeArg(tc.args)
		if m != tc.minutes || off != tc.off || ok != tc.ok {
			t.Errorf("parseAwakeArg(%q) = %d,%v,%v want %d,%v,%v", tc.args, m, off, ok, tc.minutes, tc.off, tc.ok)
		}
	}
}

func TestTelegramAwake(t *testing.T) {
	initLogger()
	prev := getConfig()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"}, Awake: AwakeConfig{DefaultMinutes: 60}})
	t.Cleanup(func() { setConfig(prev) })
	fa := stubAwake(t)
	var h telegramControl
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
