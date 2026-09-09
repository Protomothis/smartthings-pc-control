package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	stubTrayLauncher(t, nil)
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
	stubTrayLauncher(t, nil)
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
}
