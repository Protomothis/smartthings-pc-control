package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// stubTrayLauncher swaps the tray-app launcher for a recorder so tests
// never spawn a process. Each launch attempt is reported on the returned
// channel; launchErr (may be nil) is what the stub returns.
func stubTrayLauncher(t *testing.T, launchErr error) <-chan struct{} {
	t.Helper()
	calls := make(chan struct{}, 8)
	orig := sys.trayLaunch
	sys.trayLaunch = func() error {
		calls <- struct{}{}
		return launchErr
	}
	t.Cleanup(func() { sys.trayLaunch = orig })
	return calls
}

// expectTrayLaunch fails unless the stub launcher was invoked shortly.
func expectTrayLaunch(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("tray app was not launched for a remote grace schedule")
	}
}

// expectNoTrayLaunch fails if the stub launcher fires within a short window.
func expectNoTrayLaunch(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
		t.Fatal("tray app launched although the schedule did not come from a remote command")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestLegacyCommandURL: GET /{command} (and /{secret}/{command}, see
// TestLegacyPathLocksOutSecretGuessing) is what existing automations call,
// so every catalogue name stays routable; ping answers without running
// anything and an unknown name is a 400.
func TestLegacyCommandURL(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001})
	for _, name := range []string{"ping", "shutdown", "forceshutdown", "restart", "hibernate", "suspend", "lock", "turnscreenoff", "turnscreenon"} {
		if _, ok := Commands[name]; !ok {
			t.Errorf("Commands has no %q", name)
		}
	}
	if Commands["ping"].Execute != nil {
		t.Error("ping runs something")
	}
	for path, want := range map[string]int{"/ping": http.StatusOK, "/nonexistent": http.StatusBadRequest} {
		w := httptest.NewRecorder()
		newCommandHandler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
		if want == http.StatusOK && w.Body.String() != "OK" {
			t.Errorf("GET %s body = %q, want OK", path, w.Body.String())
		}
	}
}

// TestScheduleWakesTrayAppOnlyForRemote: a remote grace schedule launches
// the tray app so its [Run now]/[Cancel] toast is visible; a schedule the
// user made in the app, the WebUI or Telegram does not, a refused schedule
// launches nothing, and a failed launch (nobody logged in) keeps the
// schedule.
func TestScheduleWakesTrayAppOnlyForRemote(t *testing.T) {
	initLogger()
	for _, tc := range []struct {
		name      string
		command   string
		origin    scheduleOrigin
		launchErr error
		wantErr   bool
		launches  bool
	}{
		{"app or WebUI", "lock", originUI, nil, false, false},
		{"Telegram", "lock", originTelegram, nil, false, false},
		{"remote", "lock", originRemote, nil, false, true},
		{"remote, unknown command", "no-such-command", originRemote, nil, true, false},
		{"remote, the launch fails", "lock", originRemote, errors.New("no explorer.exe process found"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubTrayLauncher(t, tc.launchErr)
			t.Cleanup(func() { cancelScheduleBy("api") })
			err := setSchedule(tc.command, 30*time.Minute, tc.origin)
			if (err != nil) != tc.wantErr {
				t.Fatalf("setSchedule: %v, want an error: %v", err, tc.wantErr)
			}
			if tc.launches {
				expectTrayLaunch(t, calls)
			} else {
				expectNoTrayLaunch(t, calls)
			}
			if active := getSchedule()["active"] == true; active == tc.wantErr {
				t.Errorf("schedule active = %v", active)
			}
		})
	}
}

func TestGraceDefersShutdown(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: "", ShutdownGrace: true})
	launches := stubTrayLauncher(t, nil)
	defer cancelScheduleBy("api")

	// Swap in a stub so a scheduling bug can't actually shut the box down.
	orig := Commands["shutdown"]
	executed := make(chan struct{}, 1)
	Commands["shutdown"] = Command{Response: orig.Response, Execute: func() { executed <- struct{}{} }}
	defer func() { Commands["shutdown"] = orig }()

	handler := newCommandHandler()
	req := httptest.NewRequest("GET", "/shutdown", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != orig.Response {
		t.Errorf("response body changed: %q", w.Body.String())
	}

	s := getSchedule()
	if s["active"] != true {
		t.Fatal("expected an active schedule (grace period), got none")
	}
	if s["command"] != "shutdown" {
		t.Errorf("scheduled command = %v, want shutdown", s["command"])
	}
	if remaining, _ := s["remainingSec"].(int); remaining < config.DefaultGraceSeconds-5 {
		t.Errorf("remainingSec = %v, want ~%d", s["remainingSec"], config.DefaultGraceSeconds)
	}

	select {
	case <-executed:
		t.Fatal("shutdown executed immediately despite grace period")
	default:
	}

	// A remote grace schedule wakes the tray app so the toast is visible.
	expectTrayLaunch(t, launches)

	if !cancelScheduleBy("api") {
		t.Fatal("cancelSchedule reported no active schedule")
	}
}

func TestGraceDisabledExecutesImmediately(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: "", ShutdownGrace: false})
	launches := stubTrayLauncher(t, nil)

	orig := Commands["shutdown"]
	executed := make(chan struct{}, 1)
	Commands["shutdown"] = Command{Response: orig.Response, Execute: func() { executed <- struct{}{} }}
	defer func() { Commands["shutdown"] = orig }()

	handler := newCommandHandler()
	req := httptest.NewRequest("GET", "/shutdown", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not execute with grace disabled")
	}
	if s := getSchedule(); s["active"] == true {
		cancelScheduleBy("api")
		t.Fatal("unexpected schedule created with grace disabled")
	}
	// Nothing was scheduled, so there is no toast to wake the tray app for.
	expectNoTrayLaunch(t, launches)
}

func TestGraceUsesConfiguredSeconds(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 30})
	launches := stubTrayLauncher(t, nil)
	defer cancelScheduleBy("api")

	orig := Commands["restart"]
	Commands["restart"] = Command{Response: orig.Response, Execute: func() {}}
	defer func() { Commands["restart"] = orig }()

	w := httptest.NewRecorder()
	newCommandHandler().ServeHTTP(w, httptest.NewRequest("GET", "/restart", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	s := getSchedule()
	if s["active"] != true {
		t.Fatal("expected an active schedule")
	}
	remaining, _ := s["remainingSec"].(int)
	if remaining < 25 || remaining > 30 {
		t.Errorf("remainingSec = %d, want ~30 (configured grace_seconds)", remaining)
	}
	expectTrayLaunch(t, launches)
}

func TestFormatDelay(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second: "10 sec",
		90 * time.Second: "90 sec",
		time.Minute:      "1 min",
		5 * time.Minute:  "5 min",
		30 * time.Minute: "30 min",
		// #89: from an hour on the units climb, because a schedule can now be
		// three days out and "4320 min" is not a number anyone reads as that.
		time.Hour:                    "1 h",
		90 * time.Minute:             "1 h 30 min",
		12 * time.Hour:               "12 h",
		24 * time.Hour:               "1 d",
		72 * time.Hour:               "3 d",
		27*time.Hour + 5*time.Minute: "1 d 3 h",
	}
	for d, want := range cases {
		if got := formatDelay(d); got != want {
			t.Errorf("formatDelay(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestScheduleExposesOriginAndReplacement(t *testing.T) {
	initLogger()
	launches := stubTrayLauncher(t, nil)
	defer cancelScheduleBy("api")

	if err := setSchedule("lock", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	s := getSchedule()
	if s["origin"] != "ui" {
		t.Errorf("origin = %v, want ui", s["origin"])
	}
	if _, has := s["replaced"]; has {
		t.Error("first schedule must not report a replacement")
	}

	// A remote grace deferral takes over the single slot and says so.
	if err := setSchedule("restart", 5*time.Minute, originRemote); err != nil {
		t.Fatal(err)
	}
	s = getSchedule()
	if s["origin"] != "remote" {
		t.Errorf("origin = %v, want remote", s["origin"])
	}
	rep, ok := s["replaced"].(*replacedSchedule)
	if !ok || rep == nil {
		t.Fatalf("replaced = %#v, want the displaced ui schedule", s["replaced"])
	}
	if rep.Command != "lock" || rep.Origin != "ui" {
		t.Errorf("replaced = %+v, want {lock ui}", *rep)
	}
	// Let the wake goroutine finish before Cleanup swaps the stub back.
	expectTrayLaunch(t, launches)
}

// --- notifications (#55) -------------------------------------------------

// fakeNotifySink hands every delivered event to the test.
type fakeNotifySink struct{ events chan notify.Event }

func (s *fakeNotifySink) Send(_ context.Context, ev notify.Event) error {
	s.events <- ev
	return nil
}

// captureNotifications installs a fake sink behind the real bus wiring
// for the duration of the test.
func captureNotifications(t *testing.T) <-chan notify.Event {
	t.Helper()
	sink := &fakeNotifySink{events: make(chan notify.Event, 32)}
	startNotifier(sink)
	t.Cleanup(stopNotifier)
	return sink.events
}

// expectNotification waits for the next event with key and fails if
// something else, or nothing, arrives first.
func expectNotification(t *testing.T, events <-chan notify.Event, key string) notify.Event {
	t.Helper()
	select {
	case ev := <-events:
		if ev.Key() != key {
			t.Fatalf("got %s event, want %s", ev.Key(), key)
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s event was delivered", key)
		return notify.Event{}
	}
}

func expectNoNotification(t *testing.T, events <-chan notify.Event) {
	t.Helper()
	select {
	case ev := <-events:
		t.Fatalf("unexpected %s event", ev.Key())
	case <-time.After(150 * time.Millisecond):
	}
}

// withConfigFile swaps config.json in the config folder (TestMain) for the test
// and restores whatever was there afterwards.
func withConfigFile(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(configDir(), "config.json")
	origData, origErr := os.ReadFile(configPath)
	t.Cleanup(func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		} else {
			os.Remove(configPath)
		}
	})
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestUnauthorizedRequestEmitsSecurityEvent(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: "mysecret"})
	events := captureNotifications(t)

	req := httptest.NewRequest("GET", "/wrong/shutdown", nil)
	req.RemoteAddr = "192.168.1.77:51234"
	w := httptest.NewRecorder()
	newCommandHandler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	ev := expectNotification(t, events, "security.unauthorized")
	if ev.Fields["from"] != "192.168.1.77" {
		t.Errorf("from = %q, want the client IP without port", ev.Fields["from"])
	}
	if ev.Fields["path"] != "/***/shutdown" {
		t.Errorf("path = %q, want the secret masked", ev.Fields["path"])
	}
	if strings.Contains(ev.Fields["path"], "wrong") {
		t.Error("the attempted secret leaked into the event")
	}
}

func TestUnknownCommandEmitsSecurityEvent(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: ""})
	events := captureNotifications(t)

	w := httptest.NewRecorder()
	newCommandHandler().ServeHTTP(w, httptest.NewRequest("GET", "/frobnicate", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	ev := expectNotification(t, events, "security.unknown_command")
	if ev.Fields["command"] != "frobnicate" {
		t.Errorf("command = %q", ev.Fields["command"])
	}
}

func TestPingIsNeverNotified(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: ""})
	events := captureNotifications(t)

	w := httptest.NewRecorder()
	newCommandHandler().ServeHTTP(w, httptest.NewRequest("GET", "/ping", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	expectNoNotification(t, events)
}

func TestRemoteGraceEmitsScheduledWithActions(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: "", ShutdownGrace: true, GraceSeconds: 300})
	launches := stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	defer cancelScheduleBy("api")

	orig := Commands["shutdown"]
	Commands["shutdown"] = Command{Response: orig.Response, Execute: func() {}}
	defer func() { Commands["shutdown"] = orig }()

	req := httptest.NewRequest("GET", "/shutdown", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	w := httptest.NewRecorder()
	newCommandHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ev := expectNotification(t, events, "remote.grace_scheduled")
	want := map[string]string{"command": "shutdown", "from": "10.0.0.5", "delay": "5 min"}
	for k, v := range want {
		if ev.Fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, ev.Fields[k], v)
		}
	}
	if _, err := time.Parse("15:04:05", ev.Fields["execute_at"]); err != nil {
		t.Errorf("execute_at = %q, want HH:MM:SS", ev.Fields["execute_at"])
	}
	wantActions := []notify.Action{{Label: "바로 실행", Data: "runnow:"}, {Label: "취소", Data: "cancel:"}}
	if !reflect.DeepEqual(ev.Actions, wantActions) {
		t.Errorf("actions = %+v, want %+v", ev.Actions, wantActions)
	}
	expectTrayLaunch(t, launches)

	// Cancelling the grace schedule from the API reports who did it.
	if !cancelScheduleBy("app") {
		t.Fatal("no schedule to cancel")
	}
	ev = expectNotification(t, events, "remote.grace_cancelled")
	if ev.Fields["command"] != "shutdown" || ev.Fields["by"] != "app" {
		t.Errorf("grace_cancelled fields = %v", ev.Fields)
	}
}

func TestGraceActionsFollowTelegramLang(t *testing.T) {
	setConfig(Config{Telegram: TelegramConfig{Lang: "en"}})
	got := graceActions()
	if got[0].Label != "Run now" || got[1].Label != "Cancel" || got[0].Data != "runnow:" || got[1].Data != "cancel:" {
		t.Errorf("en actions = %+v", got)
	}
	setConfig(Config{})
	if got := graceActions(); got[0].Label != "바로 실행" || got[1].Label != "취소" {
		t.Errorf("default (ko) actions = %+v", got)
	}
}

func TestUISchedulesEmitCreatedAndCancelled(t *testing.T) {
	initLogger()
	// schedule.created/cancelled are off by default; turn them on.
	setConfig(Config{Port: 5001, Notify: notify.Config{"schedule": {"created": true, "cancelled": true}}})
	stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	defer cancelScheduleBy("api")

	if err := setSchedule("lock", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	ev := expectNotification(t, events, "schedule.created")
	if ev.Fields["command"] != "lock" || ev.Fields["origin"] != "ui" || ev.Fields["delay"] != "30 min" {
		t.Errorf("schedule.created fields = %v", ev.Fields)
	}
	// Replacing it reports the displaced schedule ...
	if err := setSchedule("restart", 5*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	ev = expectNotification(t, events, "schedule.replaced")
	if ev.Fields["old_command"] != "lock" || ev.Fields["old_origin"] != "ui" || ev.Fields["command"] != "restart" {
		t.Errorf("schedule.replaced fields = %v", ev.Fields)
	}
	expectNotification(t, events, "schedule.created")
	// ... and cancelling says who did it.
	cancelScheduleBy("webui")
	ev = expectNotification(t, events, "schedule.cancelled")
	if ev.Fields["by"] != "webui" || ev.Fields["command"] != "restart" {
		t.Errorf("schedule.cancelled fields = %v", ev.Fields)
	}
}

func TestEmitWithoutBusIsNoop(t *testing.T) {
	stopNotifier()
	emit("remote", "received", map[string]string{"command": "lock"}) // must not panic
	stopNotifier()                                                   // idempotent
}
