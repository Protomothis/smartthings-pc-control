package service

// Tests for the opt-in running-app detection (media-notify doc §11, #110,
// #123).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubProcesses replaces the Toolhelp lister for one test. The returned
// counter says how often the process list was read.
func stubProcesses(t *testing.T, names ...string) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	orig := sys.processes
	sys.processes = func() ([]string, error) {
		calls.Add(1)
		return slices.Clone(names), nil
	}
	activityScan.Reset()
	t.Cleanup(func() {
		sys.processes = orig
		activityScan.Reset()
	})
	return calls
}

// stubRunning drives the lister from a variable the test changes.
func stubRunning(t *testing.T, running *[]string) {
	t.Helper()
	orig := sys.processes
	sys.processes = func() ([]string, error) { return slices.Clone(*running), nil }
	activityScan.Reset()
	t.Cleanup(func() { sys.processes = orig; activityScan.Reset() })
}

func watch(process, label string) ActivityWatch {
	return ActivityWatch{Process: process, Label: label}
}

// ---- config ----------------------------------------------------------------

func TestConfigAPIValidatesActivity(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001, Activity: ActivityConfig{
		Watch: []ActivityWatch{watch("steam.exe", "Steam")},
	}})

	w := httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[{"process":"C:\\bad.exe","label":"x"}]}}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a path was accepted: %d %s", w.Code, w.Body.String())
	}
	if getConfig().Activity.Enabled {
		t.Error("a rejected save changed the live config")
	}

	// Eleven programs are refused at save time.
	var list []string
	for i := 0; i < 11; i++ {
		list = append(list, `{"process":"app`+string(rune('a'+i))+`.exe"}`)
	}
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[`+strings.Join(list, ",")+`]}}`))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "at most 10") {
		t.Errorf("eleven programs: %d %s", w.Code, w.Body.String())
	}

	// The WebUI page sends only the switch: the list must survive, and the
	// save wakes the scanner.
	select {
	case <-activityKick:
	default:
	}
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("toggle only: %d %s", w.Code, w.Body.String())
	}
	got := getConfig().Activity
	if !got.Enabled || len(got.Watch) != 1 || got.Watch[0].Process != "steam.exe" {
		t.Errorf("after toggle-only save: %+v", got)
	}
	select {
	case <-activityKick:
	default:
		t.Error("the save did not ask for an immediate rescan")
	}

	// An old client still sending kind is accepted; the key is dropped.
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[{"process":"obs64.exe","label":"OBS","kind":"stream"},{"process":"steam.exe","label":"Steam","kind":"game"}]}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("with kind: %d %s", w.Code, w.Body.String())
	}
	if got := getConfig().Activity.Watch; !slices.Equal(got, []ActivityWatch{watch("obs64.exe", "OBS"), watch("steam.exe", "Steam")}) {
		t.Errorf("order after save = %+v", got)
	}

	// An empty list is a real edit.
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[]}}`))
	if w.Code != http.StatusOK || len(getConfig().Activity.Watch) != 0 {
		t.Errorf("clearing the list: %d, %+v", w.Code, getConfig().Activity)
	}
}

// ---- matcher ---------------------------------------------------------------

// ---- scanner ---------------------------------------------------------------

// ---- status and push -------------------------------------------------------

func TestSTStatusActivityBlock(t *testing.T) {
	stubProcesses(t, "steam.exe", "private.exe")
	stSetup(t, Config{Port: 5001})

	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	act, _ := got["activity"].(map[string]any)
	if act["enabled"] != false || act["top"] != "" {
		t.Errorf("disabled activity = %v", got["activity"])
	}
	if apps, ok := act["apps"].([]any); !ok || len(apps) != 0 {
		t.Errorf("disabled apps = %v (want [])", act["apps"])
	}
	if _, ok := act["kind"]; ok {
		t.Errorf("the block still has kind: %v", act)
	}
	if f, ok := got["features"].([]any); !ok || slices.Contains(f, any("activity")) {
		t.Errorf("features while off = %v", got["features"])
	}

	cfg := Config{Port: 5001, Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("code.exe", "VS Code"), watch("Steam.exe", "Steam")}}}
	setConfig(cfg)
	activityScan.Scan(cfg.Activity)
	stSrv.ResetRateLimit()
	w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", "")
	var status struct {
		Activity json.RawMessage `json:"activity"`
		Features []string        `json:"features"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	want := `{"enabled":true,"apps":[{"id":"code.exe","label":"VS Code","running":false},{"id":"steam.exe","label":"Steam","running":true}],"top":"steam.exe"}`
	if string(status.Activity) != want {
		t.Errorf("activity = %s\nwant       %s", status.Activity, want)
	}
	if !slices.Contains(status.Features, "activity") {
		t.Errorf("features = %v", status.Features)
	}
	if body := w.Body.String(); strings.Contains(body, "private") {
		t.Errorf("status leaks an unlisted process name: %s", body)
	}
}

// TestActivityChangePushes: a flip, a list change and the switch each push
// activity.changed with the status block as data; an unchanged scan does
// not push.
func TestActivityChangePushes(t *testing.T) {
	running := []string{"explorer.exe"}
	stubRunning(t, &running)

	cfg := Config{Port: 5001, Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("obs64.exe", "OBS")}}}
	stPushSetup(t, cfg)
	startNotifier(nil) // a bus with the push tap, no notification sink
	t.Cleanup(stopNotifier)
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	lastBody := func() string {
		cb.mu.Lock()
		defer cb.mu.Unlock()
		return string(cb.bodies[len(cb.bodies)-1])
	}
	// expect waits for one push and checks that data equals
	// status.activity and has the wanted shape.
	expect := func(step, wantData string) {
		t.Helper()
		got := cb.wait(t)
		if got["type"] != "activity.changed" {
			t.Fatalf("%s: type = %v", step, got["type"])
		}
		var body struct {
			Data   json.RawMessage `json:"data"`
			Status struct {
				Activity json.RawMessage `json:"activity"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(lastBody()), &body); err != nil {
			t.Fatal(err)
		}
		if string(body.Data) != string(body.Status.Activity) {
			t.Errorf("%s: data %s != status.activity %s", step, body.Data, body.Status.Activity)
		}
		if string(body.Data) != wantData {
			t.Errorf("%s: data = %s\nwant %s", step, body.Data, wantData)
		}
	}
	noPush := func(step string) {
		t.Helper()
		before := cb.hits.Load()
		activityTick(getConfig().Activity)
		time.Sleep(200 * time.Millisecond)
		if after := cb.hits.Load(); after != before {
			t.Errorf("%s: an unchanged scan pushed (%d → %d)", step, before, after)
		}
	}

	activityTick(cfg.Activity) // baseline, no push
	running = []string{"explorer.exe", "obs64.exe", "diary.exe"}
	activityTick(cfg.Activity)
	expect("obs started", `{"enabled":true,"apps":[{"id":"obs64.exe","label":"OBS","running":true}],"top":"obs64.exe"}`)
	if body := lastBody(); strings.Contains(body, "diary") {
		t.Errorf("push body leaks an unlisted process name: %s", body)
	}
	noPush("no change")

	// A list change (Steam added on top, not running) pushes.
	cfg.Activity.Watch = []ActivityWatch{watch("steam.exe", "Steam"), watch("obs64.exe", "OBS")}
	setConfig(cfg)
	activityTick(cfg.Activity)
	expect("list changed", `{"enabled":true,"apps":[{"id":"steam.exe","label":"Steam","running":false},{"id":"obs64.exe","label":"OBS","running":true}],"top":"obs64.exe"}`)
	noPush("no change after the edit")

	// Disabling pushes the off block.
	cfg.Activity.Enabled = false
	setConfig(cfg)
	activityTick(cfg.Activity)
	expect("disabled", `{"enabled":false,"apps":[],"top":""}`)
	noPush("still off")
}

// ---- /api/processes ----------------------------------------------------------

func TestProcessesAPI(t *testing.T) {
	stubProcesses(t, "svchost.exe", "System", "[System Process]", "Registry", "Steam.exe", "steam.exe", "explorer.exe", "  ", "Code.exe")
	withLiveConfig(t, Config{Port: 5001})

	r := httptest.NewRequest("GET", "/api/processes", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	list, _ := decodeBody(t, w)["processes"].([]any)
	var got []string
	for _, v := range list {
		got = append(got, v.(string))
	}
	want := []string{"Code.exe", "explorer.exe", "Steam.exe", "svchost.exe"}
	if !slices.Equal(got, want) {
		t.Errorf("processes = %v, want %v", got, want)
	}

	// Not from another machine, even with a valid session.
	r = httptest.NewRequest("GET", "/api/processes", nil)
	r.RemoteAddr = "192.168.1.30:50000"
	w = httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "exe") {
		t.Errorf("LAN caller: %d %s", w.Code, w.Body.String())
	}

	// A secret requires the session cookie.
	withLiveConfig(t, Config{Port: 5001, Secret: "s3cr3t"})
	r = httptest.NewRequest("GET", "/api/processes", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w = httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", w.Code)
	}
}

// ---- Telegram ----------------------------------------------------------------

func TestTelegramStatusShowsActivity(t *testing.T) {
	initLogger()
	stubProcesses(t, "steam.exe", "obs64.exe")
	cfg := Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"},
		Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam"), watch("obs64.exe", "OBS"), watch("code.exe", "VS Code")}}}
	setConfig(cfg)
	t.Cleanup(func() { setConfig(Config{Port: 5001}) })
	activityScan.Scan(cfg.Activity)
	if got := tgStatus(t); !strings.Contains(got, "\n활동: Steam 실행 중 · 외 1개") {
		t.Errorf("/status lacks the activity line:\n%s", got)
	}
	cfg.Activity.Enabled = false
	setConfig(cfg)
	if got := tgStatus(t); strings.Contains(got, "활동") {
		t.Errorf("/status shows activity while the option is off:\n%s", got)
	}
}
