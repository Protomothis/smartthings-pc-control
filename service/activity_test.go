package service

// Tests for the opt-in running-app detection (media-notify doc §11, #110,
// #123).

import (
	"encoding/json"
	"errors"
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
	orig := processLister
	processLister = func() ([]string, error) {
		calls.Add(1)
		return slices.Clone(names), nil
	}
	activityScan.reset()
	t.Cleanup(func() {
		processLister = orig
		activityScan.reset()
	})
	return calls
}

// stubRunning drives the lister from a variable the test changes.
func stubRunning(t *testing.T, running *[]string) {
	t.Helper()
	orig := processLister
	processLister = func() ([]string, error) { return slices.Clone(*running), nil }
	activityScan.reset()
	t.Cleanup(func() { processLister = orig; activityScan.reset() })
}

func watch(process, label string) ActivityWatch {
	return ActivityWatch{Process: process, Label: label}
}

func app(id, label string, running bool) stActivityApp {
	return stActivityApp{ID: id, Label: label, Running: running}
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

func TestMatchActivity(t *testing.T) {
	list := []ActivityWatch{
		watch("Steam.exe", "Steam"),
		watch("obs64.exe", "OBS"),
		watch("code.exe", "VS Code"),
	}
	for _, tc := range []struct {
		name    string
		running []string
		apps    []stActivityApp
		top     string
	}{
		{"nothing running", nil,
			[]stActivityApp{app("steam.exe", "Steam", false), app("obs64.exe", "OBS", false), app("code.exe", "VS Code", false)}, ""},
		{"nothing watched running", []string{"explorer.exe", "svchost.exe"},
			[]stActivityApp{app("steam.exe", "Steam", false), app("obs64.exe", "OBS", false), app("code.exe", "VS Code", false)}, ""},
		{"case-insensitive, id lower-cased", []string{"STEAM.EXE"},
			[]stActivityApp{app("steam.exe", "Steam", true), app("obs64.exe", "OBS", false), app("code.exe", "VS Code", false)}, "steam.exe"},
		{"many instances count once", []string{"code.exe", "Code.exe", "CODE.EXE"},
			[]stActivityApp{app("steam.exe", "Steam", false), app("obs64.exe", "OBS", false), app("code.exe", "VS Code", true)}, "code.exe"},
		{"top is the first running in list order", []string{"code.exe", "obs64.exe"},
			[]stActivityApp{app("steam.exe", "Steam", false), app("obs64.exe", "OBS", true), app("code.exe", "VS Code", true)}, "obs64.exe"},
		{"all running", []string{"code.exe", "obs64.exe", "steam.exe"},
			[]stActivityApp{app("steam.exe", "Steam", true), app("obs64.exe", "OBS", true), app("code.exe", "VS Code", true)}, "steam.exe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := matchActivity(list, tc.running)
			if !got.Enabled || got.Top != tc.top || !slices.Equal(got.Apps, tc.apps) {
				t.Errorf("got %+v, want apps %+v top %q", got, tc.apps, tc.top)
			}
		})
	}

	// Reordering the list moves the top.
	reordered := []ActivityWatch{list[2], list[1], list[0]}
	if got := matchActivity(reordered, []string{"steam.exe", "code.exe"}); got.Top != "code.exe" || got.Apps[0].ID != "code.exe" {
		t.Errorf("reordered = %+v", got)
	}
	if got := matchActivity(nil, []string{"steam.exe"}); got.Apps == nil || len(got.Apps) != 0 || got.Top != "" {
		t.Errorf("empty list = %+v", got)
	}
}

func TestMatchActivityNeverCarriesUnlistedNames(t *testing.T) {
	list := []ActivityWatch{watch("steam.exe", "Steam")}
	running := []string{"steam.exe", "secret-project.exe", "bank-client.exe"}
	raw, _ := json.Marshal(matchActivity(list, running))
	if s := string(raw); strings.Contains(s, "secret") || strings.Contains(s, "bank") {
		t.Errorf("result carries an unlisted name: %s", s)
	}
}

func TestActivityTopLabel(t *testing.T) {
	a := stActivity{Enabled: true, Top: "obs64.exe", Apps: []stActivityApp{
		app("steam.exe", "Steam", false), app("obs64.exe", "OBS", true), app("code.exe", "VS Code", true), app("x.exe", "X", true),
	}}
	if label, others := a.TopLabel(); label != "OBS" || others != 2 {
		t.Errorf("topLabel = %q, %d", label, others)
	}
	if label, others := activityOff().TopLabel(); label != "" || others != 0 {
		t.Errorf("off topLabel = %q, %d", label, others)
	}
}

// ---- scanner ---------------------------------------------------------------

func TestActivityScannerDisabledDoesNotReadProcesses(t *testing.T) {
	calls := stubProcesses(t, "steam.exe")
	cfg := ActivityConfig{Enabled: false, Watch: []ActivityWatch{watch("steam.exe", "Steam")}}
	got, changed := activityScan.scan(cfg)
	if calls.Load() != 0 {
		t.Errorf("the process list was read %d times while the option is off", calls.Load())
	}
	if changed || got.Enabled || got.Top != "" || got.Apps == nil || len(got.Apps) != 0 {
		t.Errorf("disabled scan = %+v changed=%v", got, changed)
	}
	if cur := activityScan.current(cfg); cur.Enabled || cur.Apps == nil || len(cur.Apps) != 0 {
		t.Errorf("disabled status = %+v", cur)
	}
}

// TestActivityScannerChangeDetection: a flip, a list edit (add, reorder,
// relabel) and the switch are changes; an unchanged scan is not.
func TestActivityScannerChangeDetection(t *testing.T) {
	running := []string{"explorer.exe"}
	stubRunning(t, &running)

	cfg := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam")}}
	if _, changed := activityScan.scan(cfg); changed {
		t.Error("the first scan reported a change (it only sets the baseline)")
	}
	if _, changed := activityScan.scan(cfg); changed {
		t.Error("an unchanged process list reported a change")
	}
	running = []string{"explorer.exe", "Steam.exe"}
	got, changed := activityScan.scan(cfg)
	if !changed || got.Top != "steam.exe" || !slices.Equal(got.Apps, []stActivityApp{app("steam.exe", "Steam", true)}) {
		t.Errorf("steam started: %+v changed=%v", got, changed)
	}
	if cur := activityScan.current(cfg); cur.Top != "steam.exe" {
		t.Errorf("status after the scan = %+v", cur)
	}
	// A second instance of the same name is no flip.
	running = []string{"explorer.exe", "Steam.exe", "steam.exe"}
	if _, changed := activityScan.scan(cfg); changed {
		t.Error("a second steam.exe reported a change")
	}

	steps := []struct {
		name string
		cfg  ActivityConfig
	}{
		{"an entry added (not running)", ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam"), watch("code.exe", "VS Code")}}},
		{"reordered", ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("code.exe", "VS Code"), watch("steam.exe", "Steam")}}},
		{"relabelled", ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("code.exe", "Code"), watch("steam.exe", "Steam")}}},
		{"disabled", ActivityConfig{Enabled: false, Watch: []ActivityWatch{watch("code.exe", "Code"), watch("steam.exe", "Steam")}}},
		{"enabled again", ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("code.exe", "Code"), watch("steam.exe", "Steam")}}},
	}
	for _, s := range steps {
		if _, changed := activityScan.scan(s.cfg); !changed {
			t.Errorf("%s: no change reported", s.name)
		}
		if _, changed := activityScan.scan(s.cfg); changed {
			t.Errorf("%s: the scan after it reported a change again", s.name)
		}
	}
	running = []string{"explorer.exe"}
	if got, changed := activityScan.scan(steps[len(steps)-1].cfg); !changed || got.Top != "" {
		t.Errorf("steam stopped: %+v changed=%v", got, changed)
	}
}

func TestActivityStatusIgnoresScanForAnotherList(t *testing.T) {
	stubProcesses(t, "steam.exe")
	cfg := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam")}}
	activityScan.scan(cfg)
	// The user edits the list: until the (immediate) rescan the apps are
	// listed, none running, and the removed one is gone at once.
	edited := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("obs64.exe", "OBS"), watch("steam.exe", "Steam")}}
	cur := activityScan.current(edited)
	want := []stActivityApp{app("obs64.exe", "OBS", false), app("steam.exe", "Steam", false)}
	if !cur.Enabled || cur.Top != "" || !slices.Equal(cur.Apps, want) {
		t.Errorf("status after an edit = %+v, want %+v until rescanned", cur, want)
	}
}

func TestActivityScannerListFailureKeepsLastResult(t *testing.T) {
	initLogger()
	fail := false
	orig := processLister
	processLister = func() ([]string, error) {
		if fail {
			return nil, errors.New("snapshot refused")
		}
		return []string{"steam.exe"}, nil
	}
	activityScan.reset()
	t.Cleanup(func() { processLister = orig; activityScan.reset() })

	// No earlier scan: listed, nothing running.
	fail = true
	cfg := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam")}}
	if got, _ := activityScan.scan(cfg); !got.Enabled || got.Top != "" || len(got.Apps) != 1 || got.Apps[0].Running {
		t.Errorf("failed first scan = %+v", got)
	}
	fail = false
	activityScan.scan(cfg)
	// A failure after a good scan keeps it: no fake "stopped".
	fail = true
	if got, changed := activityScan.scan(cfg); changed || got.Top != "steam.exe" {
		t.Errorf("failed scan after a good one = %+v changed=%v", got, changed)
	}
}

func TestToolhelpProcessNamesSeesThisProcess(t *testing.T) {
	names, err := toolhelpProcessNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 2 {
		t.Fatalf("only %d processes listed", len(names))
	}
	// The test binary itself is running, whatever its name.
	found := false
	for _, n := range names {
		if strings.HasSuffix(strings.ToLower(n), ".test.exe") || strings.HasSuffix(strings.ToLower(n), "service.test.exe") {
			found = true
		}
	}
	if !found {
		t.Errorf("the test binary is not in the list (%d names)", len(names))
	}
}

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
	activityScan.scan(cfg.Activity)
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

func TestTelegramActivityLine(t *testing.T) {
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"}})
	t.Cleanup(func() { setConfig(Config{Port: 5001}) })
	steam, obs, code := app("steam.exe", "Steam", true), app("obs64.exe", "OBS", true), app("code.exe", "<b>", true)
	for _, tc := range []struct {
		name string
		in   stActivity
		want string
	}{
		{"off", activityOff(), ""},
		{"nothing running", stActivity{Enabled: true, Apps: []stActivityApp{app("steam.exe", "Steam", false)}}, ""},
		{"one", stActivity{Enabled: true, Top: "steam.exe", Apps: []stActivityApp{steam}}, "활동: Steam 실행 중"},
		{"two", stActivity{Enabled: true, Top: "steam.exe", Apps: []stActivityApp{steam, obs}}, "활동: Steam 실행 중 · 외 1개"},
		{"top below a stopped one", stActivity{Enabled: true, Top: "obs64.exe",
			Apps: []stActivityApp{app("steam.exe", "Steam", false), obs, steam}}, "활동: OBS 실행 중 · 외 1개"},
		{"escaped", stActivity{Enabled: true, Top: "code.exe", Apps: []stActivityApp{code}}, "활동: &lt;b&gt; 실행 중"},
	} {
		if got := tgActivityLine(tc.in); got != tc.want {
			t.Errorf("%s: tgActivityLine = %q, want %q", tc.name, got, tc.want)
		}
	}
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "en"}})
	in := stActivity{Enabled: true, Top: "steam.exe", Apps: []stActivityApp{steam, obs, code}}
	if got := tgActivityLine(in); got != "Activity: Steam running · 2 more" {
		t.Errorf("en line = %q", got)
	}
}

func TestTelegramStatusShowsActivity(t *testing.T) {
	initLogger()
	stubProcesses(t, "steam.exe", "obs64.exe")
	cfg := Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"},
		Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam"), watch("obs64.exe", "OBS"), watch("code.exe", "VS Code")}}}
	setConfig(cfg)
	t.Cleanup(func() { setConfig(Config{Port: 5001}) })
	activityScan.scan(cfg.Activity)
	if got := tgStatusText(); !strings.Contains(got, "\n활동: Steam 실행 중 · 외 1개") {
		t.Errorf("/status lacks the activity line:\n%s", got)
	}
	cfg.Activity.Enabled = false
	setConfig(cfg)
	if got := tgStatusText(); strings.Contains(got, "활동") {
		t.Errorf("/status shows activity while the option is off:\n%s", got)
	}
}
