package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

func TestLoadConfigDefaults(t *testing.T) {
	// loadConfig returns defaults when no file exists
	cfg := loadConfig()
	if cfg.Port != 5001 {
		t.Errorf("expected default port 5001, got %d", cfg.Port)
	}
	if cfg.Secret != "" {
		t.Errorf("expected empty default secret, got %q", cfg.Secret)
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	// Create a temp config file next to the executable
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	// Backup existing config if any
	origData, origErr := os.ReadFile(configPath)
	defer func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		} else {
			os.Remove(configPath)
		}
	}()

	// Write test config
	testCfg := `{"port": 9999, "secret": "test123"}`
	os.WriteFile(configPath, []byte(testCfg), 0644)

	cfg := loadConfig()
	if cfg.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Port)
	}
	if cfg.Secret != "test123" {
		t.Errorf("expected secret 'test123', got %q", cfg.Secret)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	origData, origErr := os.ReadFile(configPath)
	defer func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		} else {
			os.Remove(configPath)
		}
	}()

	// Write invalid JSON
	os.WriteFile(configPath, []byte("{invalid json!!!"), 0644)

	cfg := loadConfig()
	// Should fall back to defaults
	if cfg.Port != 5001 {
		t.Errorf("expected default port 5001 on invalid JSON, got %d", cfg.Port)
	}
}

func TestLoadConfigZeroPort(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	origData, origErr := os.ReadFile(configPath)
	defer func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		} else {
			os.Remove(configPath)
		}
	}()

	// Write config with port 0
	os.WriteFile(configPath, []byte(`{"port": 0, "secret": "abc"}`), 0644)

	cfg := loadConfig()
	if cfg.Port != 5001 {
		t.Errorf("expected port 5001 when configured as 0, got %d", cfg.Port)
	}
	if cfg.Secret != "abc" {
		t.Errorf("expected secret 'abc', got %q", cfg.Secret)
	}
}

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "(none)"},
		{"ab", "***"},
		{"abcd", "***"},
		{"abcde", "ab***de"},
		{"mysecretkey", "my***ey"},
	}

	for _, tt := range tests {
		result := maskSecret(tt.input)
		if result != tt.expected {
			t.Errorf("maskSecret(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestCommandsMapExists(t *testing.T) {
	// Verify all expected commands exist
	expected := []string{"ping", "shutdown", "forceshutdown", "restart", "hibernate", "suspend", "lock", "turnscreenoff"}
	for _, cmd := range expected {
		if _, ok := Commands[cmd]; !ok {
			t.Errorf("Commands map missing expected command: %s", cmd)
		}
	}
}

func TestCommandsMapPingNoExecute(t *testing.T) {
	cmd := Commands["ping"]
	if cmd.Execute != nil {
		t.Error("ping command should have nil Execute (no system action)")
	}
	if cmd.Response != "OK" {
		t.Errorf("ping response should be 'OK', got %q", cmd.Response)
	}
}

// Integration test for HTTP routing
func TestHTTPPingNoSecret(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: ""})

	handler := newCommandHandler()

	req := httptest.NewRequest("GET", "/ping", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /ping expected 200, got %d", w.Code)
	}
	if w.Body.String() != "OK" {
		t.Errorf("GET /ping body expected 'OK', got %q", w.Body.String())
	}
}

func TestHTTPPingWithSecret(t *testing.T) {
	setConfig(Config{Port: 5001, Secret: "mysecret"})

	handler := newCommandHandler()

	// Wrong secret -> 401
	req := httptest.NewRequest("GET", "/wrong/ping", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret expected 401, got %d", w.Code)
	}

	// Correct secret -> 200
	req = httptest.NewRequest("GET", "/mysecret/ping", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("correct secret expected 200, got %d", w.Code)
	}
}

func TestHTTPUnknownCommand(t *testing.T) {
	setConfig(Config{Port: 5001, Secret: ""})

	handler := newCommandHandler()

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown command expected 400, got %d", w.Code)
	}
}

func TestCheckCSRF(t *testing.T) {
	// GET requests should pass
	req := httptest.NewRequest("GET", "/", nil)
	if !checkCSRF(req) {
		t.Error("GET request should pass CSRF check")
	}

	// POST without header should fail
	req = httptest.NewRequest("POST", "/", nil)
	if checkCSRF(req) {
		t.Error("POST without X-Requested-With should fail CSRF check")
	}

	// POST with header should pass
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if !checkCSRF(req) {
		t.Error("POST with X-Requested-With should pass CSRF check")
	}
}

func TestCheckAuth(t *testing.T) {
	// No secret -> always authenticated
	req := httptest.NewRequest("GET", "/", nil)
	if !checkAuth(req, "") {
		t.Error("no secret should always authenticate")
	}

	// Secret set but no cookie -> not authenticated
	if checkAuth(req, "mysecret") {
		t.Error("missing cookie should not authenticate")
	}

	// Secret set with valid session
	sessionMu.Lock()
	sessionToken = "valid-token"
	sessionMu.Unlock()

	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "valid-token"})
	if !checkAuth(req, "mysecret") {
		t.Error("valid session cookie should authenticate")
	}

	// Wrong cookie value
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "wrong-token"})
	if checkAuth(req, "mysecret") {
		t.Error("wrong session cookie should not authenticate")
	}

	// Cleanup
	sessionMu.Lock()
	sessionToken = ""
	sessionMu.Unlock()
}

func TestSaveAndLoadConfig(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	// Backup
	origData, origErr := os.ReadFile(configPath)
	defer func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		} else {
			os.Remove(configPath)
		}
	}()

	// Save
	testCfg := Config{Port: 7777, Secret: "roundtrip"}
	err = saveConfig(testCfg)
	if err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	// Load back
	loaded := loadConfig()
	if loaded.Port != 7777 {
		t.Errorf("roundtrip port expected 7777, got %d", loaded.Port)
	}
	if loaded.Secret != "roundtrip" {
		t.Errorf("roundtrip secret expected 'roundtrip', got %q", loaded.Secret)
	}

	// Verify JSON format
	data, _ := os.ReadFile(configPath)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if raw["port"].(float64) != 7777 {
		t.Error("saved JSON port mismatch")
	}
}

// stubTrayLauncher swaps the tray-app launcher for a recorder so tests
// never spawn a process. Each launch attempt is reported on the returned
// channel; launchErr (may be nil) is what the stub returns.
func stubTrayLauncher(t *testing.T, launchErr error) <-chan struct{} {
	t.Helper()
	calls := make(chan struct{}, 8)
	orig := trayAppLauncher
	trayAppLauncher = func() error {
		calls <- struct{}{}
		return launchErr
	}
	t.Cleanup(func() { trayAppLauncher = orig })
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

func TestScheduleOriginWakesTrayApp(t *testing.T) {
	cases := []struct {
		origin scheduleOrigin
		want   bool
	}{
		{originUI, false},
		{originRemote, true},
		{originTelegram, false},
	}
	for _, c := range cases {
		if got := c.origin.wakesTrayApp(); got != c.want {
			t.Errorf("origin %d wakesTrayApp = %v, want %v", c.origin, got, c.want)
		}
	}
}

func TestSetScheduleUIDoesNotWakeTrayApp(t *testing.T) {
	initLogger()
	calls := stubTrayLauncher(t, nil)
	defer cancelSchedule()

	// Same path the app/WebUI /api/schedule endpoint takes.
	if err := setSchedule("lock", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	if s := getSchedule(); s["active"] != true {
		t.Fatal("expected an active schedule")
	}
	expectNoTrayLaunch(t, calls)
}

func TestSetScheduleRemoteWakesTrayApp(t *testing.T) {
	initLogger()
	calls := stubTrayLauncher(t, nil)
	defer cancelSchedule()

	if err := setSchedule("lock", 30*time.Minute, originRemote); err != nil {
		t.Fatal(err)
	}
	expectTrayLaunch(t, calls)
}

func TestSetScheduleUnknownCommandDoesNotWakeTrayApp(t *testing.T) {
	initLogger()
	calls := stubTrayLauncher(t, nil)

	if err := setSchedule("no-such-command", 5*time.Minute, originRemote); err == nil {
		cancelSchedule()
		t.Fatal("expected error for unknown command")
	}
	expectNoTrayLaunch(t, calls)
}

func TestTrayLaunchFailureKeepsSchedule(t *testing.T) {
	// No user logged in / token error must not cancel or fail the command.
	initLogger()
	calls := stubTrayLauncher(t, errors.New("no explorer.exe process found"))
	defer cancelSchedule()

	if err := setSchedule("lock", 30*time.Minute, originRemote); err != nil {
		t.Fatalf("setSchedule failed because the tray launch failed: %v", err)
	}
	expectTrayLaunch(t, calls)
	if s := getSchedule(); s["active"] != true {
		t.Fatal("schedule dropped after tray launch failure")
	}
}

func TestGraceDefersShutdown(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Secret: "", ShutdownGrace: true})
	launches := stubTrayLauncher(t, nil)
	defer cancelSchedule()

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
	if remaining, _ := s["remainingSec"].(int); remaining < defaultGraceSeconds-5 {
		t.Errorf("remainingSec = %v, want ~%d", s["remainingSec"], defaultGraceSeconds)
	}

	select {
	case <-executed:
		t.Fatal("shutdown executed immediately despite grace period")
	default:
	}

	// A remote grace schedule wakes the tray app so the toast is visible.
	expectTrayLaunch(t, launches)

	if !cancelSchedule() {
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
		cancelSchedule()
		t.Fatal("unexpected schedule created with grace disabled")
	}
	// Nothing was scheduled, so there is no toast to wake the tray app for.
	expectNoTrayLaunch(t, launches)
}

func TestGraceDefaultTrueFromConfig(t *testing.T) {
	// Missing key in an old config.json must keep the default (true).
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"port": 5001, "secret": ""}`), 0644)
	cfg := defaultConfig
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.ShutdownGrace {
		t.Error("shutdown_grace should default to true when missing from config.json")
	}
	if cfg.WebUIRemote {
		t.Error("webui_remote should default to false when missing from config.json")
	}
	if cfg.GraceSeconds != defaultGraceSeconds {
		t.Errorf("grace_seconds = %d, want default %d when missing from config.json", cfg.GraceSeconds, defaultGraceSeconds)
	}
}

func TestGraceUsesConfiguredSeconds(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 30})
	launches := stubTrayLauncher(t, nil)
	defer cancelSchedule()

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

func TestGraceDurationFallsBackWhenInvalid(t *testing.T) {
	def := time.Duration(defaultGraceSeconds) * time.Second
	cases := map[int]time.Duration{
		0:                   def, // key missing from config.json
		minGraceSeconds:     time.Duration(minGraceSeconds) * time.Second,
		maxGraceSeconds:     time.Duration(maxGraceSeconds) * time.Second,
		maxGraceSeconds + 1: def,
		-5:                  def,
	}
	for sec, want := range cases {
		if got := (Config{GraceSeconds: sec}).graceDuration(); got != want {
			t.Errorf("graceDuration(%d) = %s, want %s", sec, got, want)
		}
	}
}

func TestNormalizeConfigKeepsGraceWhenOmitted(t *testing.T) {
	current := Config{GraceSeconds: 60}
	// A client that predates grace_seconds sends 0 → keep the live value.
	if got := normalizeConfig(Config{ShutdownGrace: true}, current).GraceSeconds; got != 60 {
		t.Errorf("omitted grace_seconds → %d, want 60 (current)", got)
	}
	// Explicit values pass through untouched (validation happens later).
	if got := normalizeConfig(Config{GraceSeconds: 10}, current).GraceSeconds; got != 10 {
		t.Errorf("explicit grace_seconds → %d, want 10", got)
	}
	// Nothing to inherit → default.
	if got := normalizeConfig(Config{}, Config{}).GraceSeconds; got != defaultGraceSeconds {
		t.Errorf("no current value → %d, want default %d", got, defaultGraceSeconds)
	}
}

func TestScheduleTaskRejectsNonPositiveDelay(t *testing.T) {
	initLogger()
	if err := scheduleTask("lock", 0, originUI); err == nil {
		cancelSchedule()
		t.Fatal("zero delay accepted")
	}
	if err := scheduleTask("lock", -time.Second, originUI); err == nil {
		cancelSchedule()
		t.Fatal("negative delay accepted")
	}
}

func TestFormatDelay(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second: "10 sec",
		90 * time.Second: "90 sec",
		time.Minute:      "1 min",
		5 * time.Minute:  "5 min",
		30 * time.Minute: "30 min",
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
	defer cancelSchedule()

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

// withConfigFile swaps config.json next to the test binary for the test
// and restores whatever was there afterwards.
func withConfigFile(t *testing.T, content string) string {
	t.Helper()
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")
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

func TestLoadConfigFillsTelegramAndNotifyDefaults(t *testing.T) {
	// A v0.3.x config.json knows nothing about telegram/notify.
	configPath := withConfigFile(t, `{"port": 5001, "secret": "abc", "shutdown_grace": true, "grace_seconds": 300}`)

	cfg := loadConfig()
	tg := cfg.Telegram
	if tg.Enabled || tg.BotToken != "" || tg.ChatID != "" || tg.ControlEnabled {
		t.Errorf("telegram should be off by default, got %+v", tg)
	}
	if tg.Detail != "full" || tg.Lang != "ko" || tg.PCName != "" {
		t.Errorf("detail/lang/pc_name defaults wrong: %+v", tg)
	}
	if tg.AllowedChatIDs == nil || len(tg.AllowedChatIDs) != 0 {
		t.Errorf("allowed_chat_ids should be an empty list, got %#v", tg.AllowedChatIDs)
	}
	wantQH := notify.QuietHours{Enabled: false, Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true}
	if tg.QuietHours != wantQH {
		t.Errorf("quiet_hours = %+v, want %+v", tg.QuietHours, wantQH)
	}
	if !reflect.DeepEqual(cfg.Notify, notify.DefaultConfig()) {
		t.Errorf("notify should be the full default catalogue, got %v", cfg.Notify)
	}
	if !cfg.Notify.Enabled("remote", "received") || cfg.Notify.Enabled("schedule", "created") {
		t.Error("notify defaults not applied")
	}

	// Change a few values, save, reload: everything must survive JSON,
	// including explicit falses that differ from the defaults.
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "123:abc"
	cfg.Telegram.ChatID = "42"
	cfg.Telegram.AllowedChatIDs = []string{"42", "43"}
	cfg.Telegram.Lang = "en"
	cfg.Telegram.QuietHours.Enabled = true
	cfg.Telegram.QuietHours.SecurityBypass = false
	cfg.Notify["remote"]["received"] = false
	cfg.Notify["schedule"]["created"] = true
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	loaded := loadConfig()
	if !reflect.DeepEqual(loaded.Telegram, cfg.Telegram) {
		t.Errorf("telegram round trip:\n got %+v\nwant %+v", loaded.Telegram, cfg.Telegram)
	}
	if !reflect.DeepEqual(loaded.Notify, cfg.Notify) {
		t.Errorf("notify round trip:\n got %v\nwant %v", loaded.Notify, cfg.Notify)
	}
	if loaded.Secret != "abc" || loaded.GraceSeconds != 300 {
		t.Errorf("existing keys damaged: %+v", loaded)
	}

	// The file uses the documented key names.
	data, _ := os.ReadFile(configPath)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"telegram", "notify"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("saved config.json lacks %q", key)
		}
	}
	var rawTG map[string]json.RawMessage
	json.Unmarshal(raw["telegram"], &rawTG)
	for _, key := range []string{"enabled", "bot_token", "chat_id", "control_enabled", "allowed_chat_ids", "detail", "lang", "pc_name", "quiet_hours"} {
		if _, ok := rawTG[key]; !ok {
			t.Errorf("saved telegram object lacks %q", key)
		}
	}
}

func TestLoadConfigMissingFileHasDefaults(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")
	origData, origErr := os.ReadFile(configPath)
	defer func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		}
	}()
	os.Remove(configPath)

	cfg := loadConfig()
	if cfg.Telegram.Detail != "full" || cfg.Notify == nil {
		t.Errorf("defaults not filled without a config file: %+v", cfg)
	}
}

func TestNormalizeConfigKeepsBotTokenAndOmittedTelegram(t *testing.T) {
	current := Config{GraceSeconds: 60, Telegram: TelegramConfig{
		Enabled: true, BotToken: "keep-me", ChatID: "1", AllowedChatIDs: []string{"1", "2"}, Detail: "simple", Lang: "en",
		QuietHours: notify.QuietHours{Enabled: true, Start: "23:00", End: "06:00", SecurityBypass: true, Digest: true},
	}, Notify: notify.Config{"remote": {"received": false}}.WithDefaults()}

	// Old GUI: the body has no telegram/notify keys at all.
	body := `{"port": 5001, "secret": "s", "webui_remote": false, "shutdown_grace": true, "grace_seconds": 60}`
	newCfg := current.forUpdate()
	if err := json.Unmarshal([]byte(body), &newCfg); err != nil {
		t.Fatal(err)
	}
	got := normalizeConfig(newCfg, current)
	if !reflect.DeepEqual(got.Telegram, current.Telegram) {
		t.Errorf("omitted telegram changed:\n got %+v\nwant %+v", got.Telegram, current.Telegram)
	}
	if !reflect.DeepEqual(got.Notify, current.Notify) {
		t.Errorf("omitted notify changed: %v", got.Notify)
	}

	// New GUI sends telegram with an empty token (masked value comes with
	// #63): the token is kept, the rest is replaced.
	body = `{"port": 5001, "telegram": {"enabled": false, "bot_token": "", "chat_id": "9", "allowed_chat_ids": [], "detail": "full", "lang": "ko",
		"quiet_hours": {"enabled": false, "start": "22:00", "end": "07:00", "security_bypass": false, "digest": true}},
		"notify": {"remote": {"received": true}}}`
	newCfg = current.forUpdate()
	if err := json.Unmarshal([]byte(body), &newCfg); err != nil {
		t.Fatal(err)
	}
	got = normalizeConfig(newCfg, current)
	if got.Telegram.BotToken != "keep-me" {
		t.Errorf("empty bot_token replaced the stored one: %q", got.Telegram.BotToken)
	}
	if got.Telegram.Enabled || got.Telegram.ChatID != "9" || got.Telegram.Detail != "full" || got.Telegram.Lang != "ko" {
		t.Errorf("explicit telegram values not applied: %+v", got.Telegram)
	}
	if len(got.Telegram.AllowedChatIDs) != 0 {
		t.Errorf("explicit empty allowed_chat_ids not applied: %v", got.Telegram.AllowedChatIDs)
	}
	if got.Telegram.QuietHours.SecurityBypass || got.Telegram.QuietHours.Enabled {
		t.Errorf("explicit quiet_hours falses not applied: %+v", got.Telegram.QuietHours)
	}
	if !got.Notify.Enabled("remote", "received") || got.Notify["schedule"]["created"] {
		t.Errorf("notify not applied/filled: %v", got.Notify)
	}
	// A new token replaces the old one.
	newCfg = current.forUpdate()
	json.Unmarshal([]byte(`{"telegram": {"bot_token": "new"}}`), &newCfg)
	if got := normalizeConfig(newCfg, current); got.Telegram.BotToken != "new" || got.Telegram.ChatID != "1" {
		t.Errorf("new token / untouched chat_id: %+v", got.Telegram)
	}
	// Nothing above may have written into current's map or slice.
	if current.Notify["remote"]["received"] || len(current.Telegram.AllowedChatIDs) != 2 {
		t.Error("normalizeConfig aliased the live config")
	}
}

func TestConfigChangedKeys(t *testing.T) {
	old := defaultConfig.withDefaults()
	same := old
	if keys := configChangedKeys(old, same); len(keys) != 0 {
		t.Errorf("identical configs reported %v", keys)
	}
	changed := old
	changed.Secret = "s3cr3t-value"
	changed.ShutdownGrace = !old.ShutdownGrace // not security-relevant
	changed.Telegram.BotToken = "9999:ZZZZ"
	changed.Telegram.QuietHours.Enabled = true
	changed.Telegram.AllowedChatIDs = []string{"777"}
	got := strings.Join(configChangedKeys(old, changed), ",")
	want := "secret,telegram.bot_token,telegram.allowed_chat_ids,telegram.quiet_hours"
	if got != want {
		t.Errorf("changed keys = %s, want %s", got, want)
	}
	for _, v := range []string{"s3cr3t", "ZZZZ", "777"} {
		if strings.Contains(got, v) {
			t.Errorf("changed keys leaked the value %q", v)
		}
	}
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
	defer cancelSchedule()

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
	defer cancelSchedule()

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
