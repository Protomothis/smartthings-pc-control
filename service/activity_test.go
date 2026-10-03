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

func watch(slot int, process, label string) ActivityWatch {
	return ActivityWatch{Slot: slot, Process: process, Label: label}
}

// ---- config ----------------------------------------------------------------

func TestConfigAPIValidatesActivity(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001, Activity: ActivityConfig{
		Watch: []ActivityWatch{watch(1, "steam.exe", "Steam")},
	}})

	w := httptest.NewRecorder()
	webAPI(w, localPostJSON(t, "/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[{"slot":1,"process":"C:\\bad.exe","label":"x"}]}}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a path was accepted: %d %s", w.Code, w.Body.String())
	}
	if getConfig().Activity.Enabled {
		t.Error("a rejected save changed the live config")
	}

	// Six programs are refused at save time.
	var list []string
	for i := 0; i < 6; i++ {
		list = append(list, `{"slot":`+string(rune('1'+i))+`,"process":"app`+string(rune('a'+i))+`.exe"}`)
	}
	w = httptest.NewRecorder()
	webAPI(w, localPostJSON(t, "/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[`+strings.Join(list, ",")+`]}}`))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "at most 5") {
		t.Errorf("six programs: %d %s", w.Code, w.Body.String())
	}

	// A save names every slot: none, one out of range or one used twice is
	// refused (only a load gives slots out).
	for body, want := range map[string]string{
		`[{"process":"obs64.exe"}]`:                                       "must be 1-5 (got 0)",
		`[{"slot":6,"process":"obs64.exe"}]`:                              "must be 1-5 (got 6)",
		`[{"slot":2,"process":"obs64.exe"},{"slot":2,"process":"a.exe"}]`: "slot 2 is used twice",
	} {
		w = httptest.NewRecorder()
		webAPI(w, localPostJSON(t, "/api/config", `{"port":5001,"activity":{"enabled":true,"watch":`+body+`}}`))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}

	// The WebUI page sends only the switch: the list must survive, and the
	// save wakes the scanner.
	select {
	case <-activityKick:
	default:
	}
	w = httptest.NewRecorder()
	webAPI(w, localPostJSON(t, "/api/config", `{"port":5001,"activity":{"enabled":true}}`))
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

	// A stray kind is accepted and dropped; the list is stored in slot
	// order whatever order it came in.
	w = httptest.NewRecorder()
	webAPI(w, localPostJSON(t, "/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[{"slot":4,"process":"obs64.exe","label":"OBS","kind":"stream"},{"slot":2,"process":"steam.exe","label":"Steam","kind":"game"}]}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("with kind: %d %s", w.Code, w.Body.String())
	}
	if got := getConfig().Activity.Watch; !slices.Equal(got, []ActivityWatch{watch(2, "steam.exe", "Steam"), watch(4, "obs64.exe", "OBS")}) {
		t.Errorf("list after save = %+v", got)
	}

	// An empty list is a real edit.
	w = httptest.NewRecorder()
	webAPI(w, localPostJSON(t, "/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[]}}`))
	if w.Code != http.StatusOK || len(getConfig().Activity.Watch) != 0 {
		t.Errorf("clearing the list: %d, %+v", w.Code, getConfig().Activity)
	}
}

// ---- matcher ---------------------------------------------------------------

// ---- scanner ---------------------------------------------------------------

// ---- status and push -------------------------------------------------------

// TestActivityChangePushes: a flip, a list change and the switch each push
// activity.changed with the status block as data; an unchanged scan does
// not push.
func TestActivityChangePushes(t *testing.T) {
	running := []string{"explorer.exe"}
	stubRunning(t, &running)

	cfg := Config{Port: 5001, Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch(3, "obs64.exe", "OBS")}}}
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
	expect("obs started", `{"enabled":true,"apps":[{"slot":3,"id":"obs64.exe","label":"OBS","running":true}],"top":"obs64.exe","scanned":true}`)
	if body := lastBody(); strings.Contains(body, "diary") {
		t.Errorf("push body leaks an unlisted process name: %s", body)
	}
	noPush("no change")

	// A list change (Steam added in slot 1, not running) pushes.
	cfg.Activity.Watch = []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(3, "obs64.exe", "OBS")}
	setConfig(cfg)
	activityTick(cfg.Activity)
	expect("list changed", `{"enabled":true,"apps":[{"slot":1,"id":"steam.exe","label":"Steam","running":false},{"slot":3,"id":"obs64.exe","label":"OBS","running":true}],"top":"obs64.exe","scanned":true}`)
	noPush("no change after the edit")

	// So does moving an entry to another slot, nothing else changed.
	cfg.Activity.Watch = []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(2, "obs64.exe", "OBS")}
	setConfig(cfg)
	activityTick(cfg.Activity)
	expect("slot changed", `{"enabled":true,"apps":[{"slot":1,"id":"steam.exe","label":"Steam","running":false},{"slot":2,"id":"obs64.exe","label":"OBS","running":true}],"top":"obs64.exe","scanned":true}`)

	// Disabling pushes the off block.
	cfg.Activity.Enabled = false
	setConfig(cfg)
	activityTick(cfg.Activity)
	expect("disabled", `{"enabled":false,"apps":[],"top":"","scanned":false}`)
	noPush("still off")
}

// TestActivityStatusScannedAcrossASave: the status block says scanned
// false between a config change and the scanner's next look at it.
func TestActivityStatusScannedAcrossASave(t *testing.T) {
	running := []string{"steam.exe"}
	stubRunning(t, &running)
	cfg := Config{Port: 5001, Activity: ActivityConfig{Enabled: false, Watch: []ActivityWatch{watch(1, "steam.exe", "Steam")}}}
	withLiveConfig(t, cfg)

	if got := stActivityStatus(getConfig()); got.Scanned || got.Enabled {
		t.Errorf("off: %+v", got)
	}
	cfg.Activity.Enabled = true
	setConfig(cfg)
	if got := stActivityStatus(getConfig()); got.Scanned {
		t.Errorf("enabled, not scanned yet: %+v", got)
	}
	activityTick(getConfig().Activity)
	if got := stActivityStatus(getConfig()); !got.Scanned || got.Top != "steam.exe" {
		t.Errorf("after the first scan: %+v", got)
	}
	cfg.Activity.Watch = append(cfg.Activity.Watch, watch(2, "obs64.exe", "OBS"))
	setConfig(cfg)
	if got := stActivityStatus(getConfig()); got.Scanned || got.Apps[0].Running {
		t.Errorf("list edited, not rescanned: %+v", got)
	}
	activityTick(getConfig().Activity)
	if got := stActivityStatus(getConfig()); !got.Scanned || !got.Apps[0].Running {
		t.Errorf("after the rescan: %+v", got)
	}
}

// ---- /api/processes ----------------------------------------------------------

func TestProcessesAPI(t *testing.T) {
	stubProcesses(t, "svchost.exe", "System", "[System Process]", "Registry", "Steam.exe", "steam.exe", "explorer.exe", "  ", "Code.exe")
	withLiveConfig(t, Config{Port: 5001})

	// The desktop app (its local trusted session) gets the list.
	w := httptest.NewRecorder()
	webAPI(w, asLocalApp(t, httptest.NewRequest("GET", "/api/processes", nil)))
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

	// Not from another machine, even with the local session's cookie.
	r := asLocalApp(t, httptest.NewRequest("GET", "/api/processes", nil))
	r.RemoteAddr = "192.168.1.30:50000"
	w = httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "exe") {
		t.Errorf("LAN caller: %d %s", w.Code, w.Body.String())
	}

	// Loopback without the local session: no secret means no 401, but
	// still no list. The full matrix is in webui_localonly_test.go.
	r = httptest.NewRequest("GET", "/api/processes", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w = httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusForbidden || decodeBody(t, w)["error"] != "local_only" {
		t.Errorf("loopback without the local session: %d %s", w.Code, w.Body.String())
	}

	// A secret requires the session cookie first.
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
