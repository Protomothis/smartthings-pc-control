package service

// Tests for the /st/v1 SmartThings protocol (edge-driver doc §3, #67).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/stapi"
	"github.com/Protomothis/smartthings-pc-control/useraction"
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
	r.Header.Set("User-Agent", stapi.DriverAgent+"/1.0.0")
	w := httptest.NewRecorder()
	stSrv.Handler().ServeHTTP(w, r)
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
	orig := sources.wolScan
	sources.wolScan = func() WoLStatus { return status }
	stSrv.ResetWoLCache()
	t.Cleanup(func() {
		sources.wolScan = orig
		stSrv.ResetWoLCache()
	})
}

// stSetup gives one test a known config, an empty rate limiter, a stubbed
// adapter scan and no leftover schedule.
func stSetup(t *testing.T, cfg Config) {
	t.Helper()
	initLogger()
	setConfig(cfg)
	stSrv.ResetRateLimit()
	stubWoL(t, WoLStatus{Ready: true, Adapters: []WoLAdapter{
		{Name: "Ethernet", MacAddress: "AA-BB-CC-DD-EE-FF", Status: "Up", WoLEnabled: true, WoLCapable: true},
	}})
	cancelScheduleBy("api")
	t.Cleanup(func() {
		cancelScheduleBy("api")
		stSrv.ResetRateLimit()
	})
}

// Authentication, the allow-list, the rate limit, /st/v1/description and
// the subscriptions are tested in service/stapi (http_test.go) on a fresh
// Server; these tests drive the assembled handler against the real stores.

// ---- status (§3.2) ---------------------------------------------------------

// The status document with every option on is status.full.json, with every
// option at its default status.off.json (TestContractStatusFull/Off). In
// between, the readings the service holds are only reported while the
// user's opt-ins and session allow it.
func TestSTStatusGatesUserData(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	stamp := at.Format(time.RFC3339)
	for _, tc := range []struct {
		name     string
		cfg      Config
		loggedIn bool
		audio    string // the audio block as JSON
		media    string // the media block as JSON
		features string // fmt.Sprint of the features array
	}{
		{
			name: "everything on", cfg: optIn(), loggedIn: true,
			audio:    `{"available":true,"volume":42,"muted":true,"device":"","updated_at":"` + stamp + `"}`,
			media:    `{"status":"playing","title":"Hype Boy","artist":"NewJeans","album":"New Jeans","app":"Spotify","updated_at":"` + stamp + `"}`,
			features: "[awake audio media nowplaying notify presets]",
		},
		{
			// The readings are still stored, but nobody is there to own them.
			name: "nobody logged in", cfg: optIn(), loggedIn: false,
			audio: `{"available":false}`, media: `{"status":"none"}`,
			features: "[awake audio media nowplaying notify presets]",
		},
		{
			// A title stored a moment ago is not shown once the opt-in is off.
			name: "now playing not opted in", cfg: mediaOn(), loggedIn: true,
			audio:    `{"available":true,"volume":42,"muted":true,"device":"","updated_at":"` + stamp + `"}`,
			media:    `{"status":"playing","updated_at":"` + stamp + `"}`,
			features: "[awake audio media notify presets]",
		},
		{
			name: "media off", cfg: Config{Port: 5001, Media: MediaConfig{NowPlaying: true}, NotifyPC: NotifyPCConfig{Enabled: true}}, loggedIn: true,
			audio: `{"available":false}`, media: `{"status":"none"}`,
			features: "[awake notify presets]",
		},
		{
			// Still listed while off: the driver sends, gets 403
			// notify_disabled and says "PC 알림 꺼짐" rather than "not supported".
			name: "PC notifications off", cfg: Config{Port: 5001}, loggedIn: true,
			audio: `{"available":false}`, media: `{"status":"none"}`,
			features: "[awake notify presets]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.Media.Enabled {
				tc.cfg.NotifyPC.Enabled = true
			}
			mediaSetup(t, tc.cfg)
			stubAwake(t)
			resetMediaSample()
			t.Cleanup(resetMediaSample)
			clock.audio = func() time.Time { return at }
			noteAudioSample(useraction.Audio{Volume: 42, Muted: true}, at)
			noteMediaSampleChange(spotifyTrack, at)
			sys.sessionPresent = func() bool { return tc.loggedIn }

			var got struct {
				Audio    json.RawMessage `json:"audio"`
				Media    json.RawMessage `json:"media"`
				Features []string        `json:"features"`
			}
			if err := json.Unmarshal(stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", "").Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if string(got.Audio) != tc.audio {
				t.Errorf("audio = %s\nwant    %s", got.Audio, tc.audio)
			}
			if string(got.Media) != tc.media {
				t.Errorf("media = %s\nwant    %s", got.Media, tc.media)
			}
			if f := fmt.Sprint(got.Features); f != tc.features {
				t.Errorf("features = %s, want %s", f, tc.features)
			}
		})
	}
}

// A pinned wol_mac shows up as source "manual" and moves both ready and
// the selected flag to that adapter, without a restart — the status body
// reads the config on every call (§3.7).
func TestSTStatusWoLSelectedManual(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	stubWoL(t, WoLStatus{Adapters: []WoLAdapter{
		{Name: "Ethernet", MacAddress: "AA-BB-CC-DD-EE-FF", Status: "Up", WoLEnabled: true, WoLCapable: true},
		{Name: "Wi-Fi", MacAddress: "11-22-33-44-55-66", IPs: []string{"192.168.1.9", "fe80::1"}, Status: "Up"},
	}})

	// Lower case with colons, as a user would paste it from ipconfig.
	setConfig(Config{Port: 5001, SmartThings: SmartThingsConfig{WoLMAC: "11:22:33:44:55:66"}.WithDefaults()})
	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	wol := got["wol"].(map[string]any)
	sel := wol["selected"].(map[string]any)
	if sel["name"] != "Wi-Fi" || sel["mac"] != "11-22-33-44-55-66" || sel["source"] != "manual" {
		t.Errorf("wol.selected = %v, want the pinned Wi-Fi adapter", sel)
	}
	if sel["ip"] != "192.168.1.9" {
		t.Errorf("wol.selected.ip = %v, want the adapter's first IPv4", sel["ip"])
	}
	// ready follows the selected adapter, not "any adapter has WoL on".
	if wol["ready"] != false {
		t.Errorf("wol.ready = %v, want false: the pinned adapter has WoL off", wol["ready"])
	}
	adapters := wol["adapters"].([]any)
	if a := adapters[0].(map[string]any); a["selected"] != false {
		t.Errorf("Ethernet is still marked selected: %v", a)
	}
	if a := adapters[1].(map[string]any); a["selected"] != true || a["ip"] != "192.168.1.9" {
		t.Errorf("Wi-Fi row = %v", a)
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

func TestSTCommandAcceptsTheThreeDayCeiling(t *testing.T) {
	// #89: the driver's preset list ends at 4320 minutes, so the API has to
	// take that value - the cloud validates the argument against the
	// definition, but nothing validates the definition against the service.
	stSetup(t, Config{Port: 5001})
	stubTrayLauncher(t, nil)
	defer cancelScheduleBy("api")

	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "",
		`{"command":"shutdown","minutes":4320}`)
	if w.Code != http.StatusOK {
		t.Fatalf("4320 minutes: %d (%s)", w.Code, w.Body.String())
	}
	s := getSchedule()
	if s["active"] != true || s["command"] != "shutdown" {
		t.Fatalf("schedule = %v", s)
	}
	if rem, _ := s["remainingSec"].(int); rem < 4320*60-5 || rem > 4320*60 {
		t.Errorf("remainingSec = %v, want ~%d", rem, 4320*60)
	}
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
		// #89: the ceiling is 4320 (three days), so one minute past it is out.
		{"minutes too large", `{"command":"lock","minutes":4321}`},
		{"minutes far too large", `{"command":"lock","minutes":5000}`},
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
	// What counts as a hub contact is stapi's (TestAuth); this is the app's
	// view of it.
	stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s3cr3t", "")

	// The WebUI endpoint is behind the normal session auth.
	w := httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("GET", "/api/st/hub", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/api/st/hub without a session: %d, want 401", w.Code)
	}
	setConfig(Config{Port: 5001}) // no secret configured: no login needed
	w = httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("GET", "/api/st/hub", nil))
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

// TestSTHubAPIDiagnostics covers the #95 additions: the full machine id and
// the responder's state, which describe this PC and must therefore be
// present even before a hub has ever called.
func TestSTHubAPIDiagnostics(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	stSrv.ResetSSDPLastSearch()
	prevOK := ssdpFirewallRuleOK()
	ssdpFirewallOK.Store(true)
	t.Cleanup(func() {
		stSrv.ResetSSDPLastSearch()
		ssdpFirewallOK.Store(prevOK)
	})

	hub := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		webAPI(w, httptest.NewRequest("GET", "/api/st/hub", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("/api/st/hub: %d", w.Code)
		}
		return stJSON(t, w)
	}

	got := hub()
	if id, _ := got["machine_id"].(string); id == "" || id != machineID() {
		t.Errorf("machine_id = %v, want the full %q", got["machine_id"], machineID())
	}
	ssdp, ok := got["ssdp"].(map[string]any)
	if !ok {
		t.Fatalf("no ssdp object: %v", got)
	}
	if ssdp["running"] != stSrv.SSDPRunning() {
		t.Errorf("ssdp.running = %v, want %v", ssdp["running"], stSrv.SSDPRunning())
	}
	if ssdp["firewall_rule"] != true {
		t.Errorf("ssdp.firewall_rule = %v, want true", ssdp["firewall_rule"])
	}
	// No search yet is null, not a zero-time object the app would render
	// as "1970".
	if v, present := ssdp["last_search"]; !present || v != nil {
		t.Errorf("ssdp.last_search = %v, want null", v)
	}

	stSrv.NoteSSDPSearch("192.168.1.105")
	ssdp, _ = hub()["ssdp"].(map[string]any)
	last, ok := ssdp["last_search"].(map[string]any)
	if !ok {
		t.Fatalf("last_search after a search = %v", ssdp["last_search"])
	}
	if last["ip"] != "192.168.1.105" {
		t.Errorf("last_search.ip = %v", last["ip"])
	}
	at, _ := last["at"].(string)
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("last_search.at = %q: %v", at, err)
	}

	// The existing fields are untouched by the additions.
	for _, key := range []string{"connected", "ip", "driver_version", "last_seen"} {
		if _, present := got[key]; !present {
			t.Errorf("%s is missing from /api/st/hub", key)
		}
	}
}

// ---- turnscreenon (§3.3) ---------------------------------------------------

func TestTurnScreenOnCommand(t *testing.T) {
	if _, ok := Commands["turnscreenon"]; !ok {
		t.Fatal("turnscreenon is missing from the command registry")
	}
	var gotArgs [][]string
	var runErr error
	saved := userRun.screen
	userRun.screen = func(_ context.Context, args ...string) (UserActionResult, error) {
		gotArgs = append(gotArgs, args)
		return UserActionResult{OK: runErr == nil}, runErr
	}
	t.Cleanup(func() { userRun.screen = saved; setDisplayState("unknown") })
	setDisplayState("unknown")

	// The screen commands go through user-action (#121), not a shell.
	Commands["turnscreenon"].Execute()
	Commands["turnscreenoff"].Execute()
	if want := [][]string{{"screen", "on"}, {"screen", "off"}}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("user-action args = %q, want %q", gotArgs, want)
	}
	if getDisplayState() != "off" {
		t.Errorf("display state = %q, want off", getDisplayState())
	}

	// A timed-out broadcast still counts; nobody logged in does not.
	runErr = fmt.Errorf("%w after 3s", errUserActionTimeout)
	Commands["turnscreenon"].Execute()
	if getDisplayState() != "on" {
		t.Errorf("after a timeout: display state = %q, want on", getDisplayState())
	}
	runErr = errNoUserSession
	Commands["turnscreenoff"].Execute()
	if getDisplayState() != "on" {
		t.Errorf("without a user session: display state = %q, want on (unchanged)", getDisplayState())
	}
}

// ---- config (§3.7) ---------------------------------------------------------

func TestConfigAPIRoundTripsSmartThings(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001, SmartThings: SmartThingsConfig{
		AllowedHubs: []string{"192.168.1.20"}, ExposeSession: true,
	}})

	// GET hands the GUI every §3.7 key.
	w := httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("GET", "/api/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	st, ok := decodeBody(t, w)["smartthings"].(map[string]any)
	if !ok {
		t.Fatalf("no smartthings object: %s", w.Body.String())
	}
	if st["expose_session"] != true || st["expose_session_user"] != false {
		t.Errorf("smartthings = %v", st)
	}
	// #95: the retired key is not offered to a client any more.
	if _, ok := st["discovery"]; ok {
		t.Errorf("GET still exposes smartthings.discovery: %v", st)
	}
	hubs, _ := st["allowed_hubs"].([]any)
	if len(hubs) != 1 || hubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v", st["allowed_hubs"])
	}

	// POST writes them back and the live config follows without a restart.
	// The body still carries the retired discovery key, the way an older
	// WebUI page would send it: it must be ignored, not rejected (#95).
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"smartthings":{"discovery":false,"allowed_hubs":["10.0.0.7"],"expose_session":false,"expose_session_user":true}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", w.Code, w.Body.String())
	}
	got := getConfig().SmartThings
	if got.ExposeSession || !got.ExposeSessionUser {
		t.Errorf("live config = %+v", got)
	}
	if len(got.AllowedHubs) != 1 || got.AllowedHubs[0] != "10.0.0.7" {
		t.Errorf("allowed_hubs = %v", got.AllowedHubs)
	}

	// A request made right afterwards sees the saved values (no restart).
	stSrv.ResetRateLimit()
	d := stDo(t, http.MethodGet, "/st/v1/description", "10.0.0.7", "", "")
	if d.Code != http.StatusOK {
		t.Fatalf("description after save: status %d", d.Code)
	}
	if stJSON(t, d)["port"] != float64(5001) {
		t.Errorf("description port = %v", stJSON(t, d)["port"])
	}
}
