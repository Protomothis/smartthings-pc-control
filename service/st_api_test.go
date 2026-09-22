package service

// Tests for the /st/v1 SmartThings protocol (edge-driver doc §3, #67).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// stDo sends one /st/v1 request through the real handler tree. from is the
// source IP (no port), secret goes into X-PC-Secret when non-empty.
func stDo(t *testing.T, method, path, from, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.RemoteAddr = from + ":51234"
	if secret != "" {
		r.Header.Set("X-PC-Secret", secret)
	}
	r.Header.Set("User-Agent", stDriverAgent+"/1.0.0")
	w := httptest.NewRecorder()
	stHandler().ServeHTTP(w, r)
	return w
}

// stJSON decodes a recorded response body.
func stJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

// stubWoL replaces the adapter scan (it shells out to PowerShell) and
// clears the status cache before and after the test.
func stubWoL(t *testing.T, status WoLStatus) {
	t.Helper()
	clear := func() {
		wolCacheMu.Lock()
		wolCached, wolCachedAt = WoLStatus{}, time.Time{}
		wolCacheMu.Unlock()
	}
	orig := stWoLProvider
	stWoLProvider = func() WoLStatus { return status }
	clear()
	t.Cleanup(func() {
		stWoLProvider = orig
		clear()
	})
}

// stSetup gives one test a known config, an empty rate limiter, a stubbed
// adapter scan and no leftover schedule.
func stSetup(t *testing.T, cfg Config) {
	t.Helper()
	initLogger()
	setConfig(cfg)
	resetSTRateLimit()
	stubWoL(t, WoLStatus{Ready: true, Adapters: []WoLAdapter{
		{Name: "Ethernet", MacAddress: "AA-BB-CC-DD-EE-FF", Status: "Up", WoLEnabled: true, WoLCapable: true},
	}})
	cancelSchedule()
	t.Cleanup(func() {
		cancelSchedule()
		resetSTRateLimit()
	})
}

// ---- auth (§3.1) -----------------------------------------------------------

func TestSTHeaderSecretAuth(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	events := captureNotifications(t)

	if w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s3cr3t", ""); w.Code != http.StatusOK {
		t.Fatalf("with the right header: %d, want 200 (%s)", w.Code, w.Body.String())
	}

	// Each case uses its own source IP: security.unauthorized is
	// aggregated per source, so a second rejection from the same address
	// would only be counted, not delivered.
	for _, tc := range []struct{ name, secret, from string }{
		{"missing", "", "192.168.1.20"},
		{"wrong", "guessed", "192.168.1.21"},
	} {
		w := stDo(t, "GET", "/st/v1/status", tc.from, tc.secret, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s secret: %d, want 401", tc.name, w.Code)
			continue
		}
		ev := expectNotification(t, events, "security.unauthorized")
		if ev.Fields["from"] != tc.from {
			t.Errorf("from = %q, want %q", ev.Fields["from"], tc.from)
		}
		if ev.Fields["path"] != "/st/v1/status" {
			t.Errorf("path = %q", ev.Fields["path"])
		}
		for _, leak := range []string{"s3cr3t", "guessed"} {
			if strings.Contains(w.Body.String()+fmt.Sprint(ev.Fields), leak) {
				t.Errorf("the secret %q leaked into the response or the event", leak)
			}
		}
	}
}

func TestSTNoSecretNeedsNoHeader(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: ""})
	if w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""); w.Code != http.StatusOK {
		t.Fatalf("without a configured secret: %d, want 200", w.Code)
	}
}

func TestSTAllowedHubsRejectsOtherSources(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t", SmartThings: SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}}})

	if w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s3cr3t", ""); w.Code != http.StatusOK {
		t.Errorf("allowed hub: %d, want 200", w.Code)
	}
	if w := stDo(t, "GET", "/st/v1/status", "10.0.0.9", "s3cr3t", ""); w.Code != http.StatusForbidden {
		t.Errorf("other source: %d, want 403", w.Code)
	}
	// An empty list allows everyone (the default).
	setConfig(Config{Port: 5001, Secret: "s3cr3t"})
	resetSTRateLimit()
	if w := stDo(t, "GET", "/st/v1/status", "10.0.0.9", "s3cr3t", ""); w.Code != http.StatusOK {
		t.Errorf("empty allow-list: %d, want 200", w.Code)
	}
}

func TestSTRateLimitPerSourceIP(t *testing.T) {
	stSetup(t, Config{Port: 5001})

	for i := 1; i <= stRatePerSecond; i++ {
		if w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d, want 200", i, w.Code)
		}
	}
	w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", "")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("request %d: %d, want 429", stRatePerSecond+1, w.Code)
	}
	// The bucket is per source: another hub is unaffected.
	if w := stDo(t, "GET", "/st/v1/status", "192.168.1.21", "", ""); w.Code != http.StatusOK {
		t.Errorf("other source IP: %d, want 200", w.Code)
	}
}

// ---- status (§3.2) ---------------------------------------------------------

func TestSTStatusShape(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t", ShutdownGrace: true, GraceSeconds: 300})
	setDisplayState("off")
	t.Cleanup(func() { setDisplayState("unknown") })

	w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s3cr3t", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
	got := stJSON(t, w)

	// Required keys and their JSON types (§3.2).
	wantTypes := map[string]string{
		"protocol": "number", "service_version": "string", "machine_id": "string",
		"hostname": "string", "power": "string", "uptime_seconds": "number",
		"last_shutdown_clean": "bool", "secret_set": "bool", "grace": "object",
		"schedule": "object", "update": "object", "wol": "object",
		"display": "string", "session": "object",
	}
	for key, kind := range wantTypes {
		v, ok := got[key]
		if !ok {
			t.Errorf("status is missing %q", key)
			continue
		}
		if jsonKind(v) != kind {
			t.Errorf("%s is %s, want %s", key, jsonKind(v), kind)
		}
	}
	if _, ok := got["last_command"]; !ok {
		t.Error("status is missing last_command (null is fine, the key is not)")
	}
	if got["protocol"] != float64(stProtocol) || got["power"] != "on" {
		t.Errorf("protocol/power = %v/%v", got["protocol"], got["power"])
	}
	if got["secret_set"] != true || got["display"] != "off" {
		t.Errorf("secret_set/display = %v/%v", got["secret_set"], got["display"])
	}
	if got["machine_id"] == "" {
		t.Error("machine_id is empty")
	}

	grace := got["grace"].(map[string]any)
	if grace["enabled"] != true || grace["seconds"] != float64(300) {
		t.Errorf("grace = %v", grace)
	}
	if sched := got["schedule"].(map[string]any); sched["active"] != false {
		t.Errorf("schedule = %v, want {active:false}", sched)
	}
	if sess := got["session"].(map[string]any); sess["exposed"] != false || len(sess) != 1 {
		t.Errorf("session = %v, want only {exposed:false} while the opt-in is off", sess)
	}
	update := got["update"].(map[string]any)
	if _, ok := update["available"].(bool); !ok {
		t.Errorf("update.available = %v", update["available"])
	}
	wol := got["wol"].(map[string]any)
	if wol["ready"] != true {
		t.Errorf("wol.ready = %v", wol["ready"])
	}
	adapters, _ := wol["adapters"].([]any)
	if len(adapters) != 1 {
		t.Fatalf("wol.adapters = %v", wol["adapters"])
	}
	a := adapters[0].(map[string]any)
	for _, key := range []string{"name", "mac", "wol_enabled", "wol_capable"} {
		if _, ok := a[key]; !ok {
			t.Errorf("adapter is missing %q: %v", key, a)
		}
	}
	if a["mac"] != "AA-BB-CC-DD-EE-FF" || a["wol_enabled"] != true {
		t.Errorf("adapter = %v", a)
	}
}

// jsonKind names the Go type json.Unmarshal produced for a value.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func TestSTStatusReportsScheduleAndSession(t *testing.T) {
	stSetup(t, Config{Port: 5001, SmartThings: SmartThingsConfig{ExposeSession: false}})
	stubTrayLauncher(t, nil)

	if err := setSchedule("shutdown", 30*time.Minute, originSmartThings); err != nil {
		t.Fatal(err)
	}
	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	sched, _ := got["schedule"].(map[string]any)
	if sched["active"] != true || sched["command"] != "shutdown" || sched["origin"] != "smartthings" {
		t.Fatalf("schedule = %v", sched)
	}
	remaining, ok := sched["remaining_seconds"].(float64)
	if !ok || remaining < 1700 || remaining > 1800 {
		t.Errorf("remaining_seconds = %v, want ~1800", sched["remaining_seconds"])
	}
	at, ok := sched["execute_at"].(string)
	if !ok {
		t.Fatalf("execute_at = %v", sched["execute_at"])
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("execute_at = %q, want RFC3339 with the local offset: %v", at, err)
	}
	// The old /api/schedule key names must not appear in the ST shape.
	for _, gone := range []string{"executeAt", "remainingSec"} {
		if _, has := sched[gone]; has {
			t.Errorf("schedule still carries the WebUI key %q", gone)
		}
	}
}

// ---- command (§3.3) --------------------------------------------------------

func TestSTCommandSchedulesWithSmartThingsOrigin(t *testing.T) {
	stSetup(t, Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 300})
	launches := stubTrayLauncher(t, nil)

	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"shutdown","minutes":30}`)
	if w.Code != http.StatusOK {
		t.Fatalf("command: %d (%s)", w.Code, w.Body.String())
	}
	got := stJSON(t, w)
	if got["accepted"] != true || got["executed"] != false {
		t.Errorf("accepted/executed = %v/%v", got["accepted"], got["executed"])
	}
	sched, _ := got["schedule"].(map[string]any)
	if sched["active"] != true || sched["command"] != "shutdown" || sched["origin"] != "smartthings" {
		t.Errorf("schedule = %v", sched)
	}
	if s := getSchedule(); s["origin"] != "smartthings" || s["command"] != "shutdown" {
		t.Errorf("live schedule = %v", s)
	}
	// The user acted from their phone: no tray toast (like Telegram).
	expectNoTrayLaunch(t, launches)
}

func TestSTCommandGraceDefersWithToast(t *testing.T) {
	stSetup(t, Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 300})
	launches := stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	executed := stubCommand(t, "shutdown")

	got := stJSON(t, stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"shutdown","mode":"default"}`))
	if got["executed"] != false {
		t.Errorf("executed = %v, want false while the grace period runs", got["executed"])
	}
	expectNotExecuted(t, executed, "shutdown")
	// The legacy event keeps its shape so Telegram still offers the buttons.
	ev := expectNotification(t, events, "remote.grace_scheduled")
	if ev.Fields["command"] != "shutdown" || ev.Fields["from"] != "192.168.1.20" || ev.Fields["delay"] != "5 min" {
		t.Errorf("grace_scheduled fields = %v", ev.Fields)
	}
	if len(ev.Actions) != 2 {
		t.Errorf("grace_scheduled actions = %v, want run-now and cancel", ev.Actions)
	}
	// A grace deferral keeps origin "remote": the toast is the point.
	if s := getSchedule(); s["origin"] != "remote" {
		t.Errorf("schedule origin = %v, want remote", s["origin"])
	}
	expectTrayLaunch(t, launches)
}

func TestSTCommandModes(t *testing.T) {
	cases := []struct {
		name         string
		grace        bool
		body         string
		wantExecuted bool
	}{
		{"immediate overrides the configured grace", true, `{"command":"shutdown","mode":"immediate"}`, true},
		{"grace forces a deferral although grace is off", false, `{"command":"shutdown","mode":"grace"}`, false},
		{"default runs at once when grace is off", false, `{"command":"shutdown","mode":"default"}`, true},
		{"forceshutdown is always immediate", true, `{"command":"forceshutdown","mode":"grace"}`, true},
		{"harmless commands are never deferred", true, `{"command":"lock"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stSetup(t, Config{Port: 5001, ShutdownGrace: tc.grace, GraceSeconds: 300})
			stubTrayLauncher(t, nil)
			stubCommand(t, "shutdown")
			stubCommand(t, "forceshutdown")
			stubCommand(t, "lock")

			got := stJSON(t, stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", tc.body))
			if got["executed"] != tc.wantExecuted {
				t.Errorf("executed = %v, want %v", got["executed"], tc.wantExecuted)
			}
			if got["accepted"] != true {
				t.Errorf("accepted = %v", got["accepted"])
			}
		})
	}
}

func TestSTCommandEmitsRemoteEvents(t *testing.T) {
	stSetup(t, Config{Port: 5001, ShutdownGrace: false})
	stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	executed := stubCommand(t, "lock")

	stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"lock"}`)
	expectExecuted(t, executed, "lock")
	ev := expectNotification(t, events, "remote.received")
	if ev.Fields["command"] != "lock" || ev.Fields["from"] != "192.168.1.20" {
		t.Errorf("remote.received fields = %v", ev.Fields)
	}
	// /status in Telegram shows the last remote command and where it came from.
	if lr := getLastRemote(); lr.Command != "lock" || lr.Origin != "smartthings" {
		t.Errorf("last remote = %+v", lr)
	}

	// ping is never notified.
	stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"ping"}`)
	expectNoNotification(t, events)
}

func TestSTCommandRejectsBadInput(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	events := captureNotifications(t)

	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"frobnicate"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown command: %d, want 400", w.Code)
	}
	ev := expectNotification(t, events, "security.unknown_command")
	if ev.Fields["command"] != "frobnicate" {
		t.Errorf("unknown_command fields = %v", ev.Fields)
	}

	for _, tc := range []struct{ name, body string }{
		{"unknown mode", `{"command":"lock","mode":"whenever"}`},
		{"minutes too large", `{"command":"lock","minutes":5000}`},
		{"negative minutes", `{"command":"lock","minutes":-1}`},
		{"not JSON", `nope`},
	} {
		if w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", tc.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", tc.name, w.Code)
		}
	}
	if w := stDo(t, "GET", "/st/v1/command", "192.168.1.20", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /st/v1/command: %d, want 405", w.Code)
	}
}

// ---- schedule (§3.4) -------------------------------------------------------

func TestSTScheduleDelete(t *testing.T) {
	// schedule.created/cancelled are off in the catalogue by default.
	stSetup(t, Config{Port: 5001, Notify: notify.Config{"schedule": {"created": true, "cancelled": true}}})
	stubTrayLauncher(t, nil)
	events := captureNotifications(t)

	// Nothing scheduled.
	got := stJSON(t, stDo(t, "DELETE", "/st/v1/schedule", "192.168.1.20", "", ""))
	if got["cancelled"] != false {
		t.Errorf("cancelled = %v, want false with no schedule", got["cancelled"])
	}

	if err := setSchedule("shutdown", 30*time.Minute, originSmartThings); err != nil {
		t.Fatal(err)
	}
	expectNotification(t, events, "schedule.created")
	got = stJSON(t, stDo(t, "DELETE", "/st/v1/schedule", "192.168.1.20", "", ""))
	if got["cancelled"] != true {
		t.Errorf("cancelled = %v, want true", got["cancelled"])
	}
	ev := expectNotification(t, events, "schedule.cancelled")
	if ev.Fields["by"] != "smartthings" || ev.Fields["origin"] != "smartthings" {
		t.Errorf("schedule.cancelled fields = %v", ev.Fields)
	}
	if s := getSchedule(); s["active"] != false {
		t.Errorf("schedule still active: %v", s)
	}
}

// ---- hub tracking ----------------------------------------------------------

func TestSTHubLastSeen(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})

	// An unauthenticated request must not count as a hub contact.
	before, _ := hubLastSeenInfo()
	stDo(t, "GET", "/st/v1/status", "10.0.0.9", "wrong", "")
	if after, _ := hubLastSeenInfo(); after != before {
		t.Errorf("a rejected request updated hubLastSeen: %+v", after)
	}

	stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s3cr3t", "")
	seen, ok := hubLastSeenInfo()
	if !ok {
		t.Fatal("hubLastSeenInfo reports nothing after an authenticated request")
	}
	if seen.IP != "192.168.1.20" || seen.DriverVersion != "1.0.0" {
		t.Errorf("hub last seen = %+v", seen)
	}

	// The WebUI endpoint is behind the normal session auth.
	w := httptest.NewRecorder()
	handleSTHubAPI(w, httptest.NewRequest("GET", "/api/st/hub", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/api/st/hub without a session: %d, want 401", w.Code)
	}
	setConfig(Config{Port: 5001}) // no secret configured: no login needed
	w = httptest.NewRecorder()
	handleSTHubAPI(w, httptest.NewRequest("GET", "/api/st/hub", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/api/st/hub: %d", w.Code)
	}
	got := stJSON(t, w)
	if got["connected"] != true || got["ip"] != "192.168.1.20" || got["driver_version"] != "1.0.0" {
		t.Errorf("/api/st/hub = %v", got)
	}
	if _, err := time.Parse(time.RFC3339, got["last_seen"].(string)); err != nil {
		t.Errorf("last_seen = %v: %v", got["last_seen"], err)
	}
}

func TestDriverVersionOf(t *testing.T) {
	cases := map[string]string{
		stDriverAgent + "/1.0.0": "1.0.0",
		"curl/8.4.0":             "curl/8.4.0",
		"":                       "",
	}
	for ua, want := range cases {
		if got := driverVersionOf(ua); got != want {
			t.Errorf("driverVersionOf(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestSTUnknownRouteIs404(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	// Unknown paths must not reach the legacy /{secret}/{command} handler.
	// /st/v1/description (#69) and /st/v1/subscribe (#68) have their own
	// tests in st_ssdp_test.go and st_push_test.go.
	for _, path := range []string{"/st/v1/nope", "/st/v1/"} {
		if w := stDo(t, "GET", path, "192.168.1.20", "", ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
}

// ---- turnscreenon (§3.3) ---------------------------------------------------

func TestTurnScreenOnCommand(t *testing.T) {
	if _, ok := Commands["turnscreenon"]; !ok {
		t.Fatal("turnscreenon is missing from the command registry")
	}
	if got := screenPowerScript(-1); !strings.Contains(got, "SendMessage(-1,0x0112,0xF170,-1)") {
		t.Errorf("screen-on script = %q", got)
	}
	if got := screenPowerScript(2); !strings.Contains(got, "SendMessage(-1,0x0112,0xF170,2)") {
		t.Errorf("screen-off script = %q", got)
	}
	setDisplayState("on")
	t.Cleanup(func() { setDisplayState("unknown") })
	if getDisplayState() != "on" {
		t.Errorf("display state = %q", getDisplayState())
	}
}

// ---- config (§3.7) ---------------------------------------------------------

func TestSmartThingsConfigDefaults(t *testing.T) {
	// A config.json that predates v1.1.0 keeps discovery on and the rest off.
	withConfigFile(t, `{"port": 5001, "secret": "abc"}`)
	cfg := loadConfig()
	if !cfg.SmartThings.Discovery {
		t.Error("discovery must default to true when the key is absent")
	}
	if cfg.SmartThings.AllowedHubs == nil || len(cfg.SmartThings.AllowedHubs) != 0 {
		t.Errorf("allowed_hubs = %v, want []", cfg.SmartThings.AllowedHubs)
	}
	if cfg.SmartThings.ExposeSession || cfg.SmartThings.ExposeSessionUser {
		t.Error("session exposure must default to off")
	}

	// An explicit false survives the load.
	withConfigFile(t, `{"port": 5001, "smartthings": {"discovery": false, "allowed_hubs": ["192.168.1.20"]}}`)
	cfg = loadConfig()
	if cfg.SmartThings.Discovery {
		t.Error("discovery: false was overwritten by the default")
	}
	if len(cfg.SmartThings.AllowedHubs) != 1 || cfg.SmartThings.AllowedHubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v", cfg.SmartThings.AllowedHubs)
	}
}

func TestNormalizeConfigKeepsSmartThingsHubsWhenOmitted(t *testing.T) {
	current := defaultConfig.withDefaults()
	current.SmartThings.AllowedHubs = []string{"192.168.1.20"}
	current.SmartThings.ExposeSession = true

	posted := current.forUpdate()
	if err := json.Unmarshal([]byte(`{"port":5001}`), &posted); err != nil {
		t.Fatal(err)
	}
	got := normalizeConfig(posted, current)
	if len(got.SmartThings.AllowedHubs) != 1 || got.SmartThings.AllowedHubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v, want the live list kept", got.SmartThings.AllowedHubs)
	}
	if !got.SmartThings.ExposeSession {
		t.Error("expose_session was reset by a body that omitted it")
	}
}

func TestSTHubAllowedMatching(t *testing.T) {
	hubs := []string{" 192.168.1.20 ", ""}
	if !stHubAllowed(hubs, "192.168.1.20") {
		t.Error("a padded allow-list entry must still match")
	}
	if stHubAllowed(hubs, "192.168.1.21") {
		t.Error("an unlisted source matched")
	}
	if !stHubAllowed([]string{"192.168.1.20"}, "::ffff:192.168.1.20") {
		t.Error("the IPv4-mapped form of an allowed hub must match")
	}
}
