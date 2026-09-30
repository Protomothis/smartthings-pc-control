package service

// Tests for the opt-in running-app detection (media-notify doc §11, #110).

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
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

func watch(process, label, kind string) ActivityWatch {
	return ActivityWatch{Process: process, Label: label, Kind: kind}
}

// ---- config ----------------------------------------------------------------

func TestValidateActivity(t *testing.T) {
	twentyOne := make([]ActivityWatch, 21)
	for i := range twentyOne {
		twentyOne[i] = watch(strings.Repeat("a", i+1)+".exe", "x", "other")
	}
	for _, tc := range []struct {
		name  string
		watch []ActivityWatch
		want  string // substring of the message; "" = valid
	}{
		{"empty list", nil, ""},
		{"plain entry", []ActivityWatch{watch("steam.exe", "Steam", "game")}, ""},
		{"upper-case extension", []ActivityWatch{watch("OBS64.EXE", "OBS", "stream")}, ""},
		{"all kinds", []ActivityWatch{
			watch("a.exe", "A", "game"), watch("b.exe", "B", "stream"), watch("c.exe", "C", "media"),
			watch("d.exe", "D", "work"), watch("e.exe", "E", "other"),
		}, ""},
		{"label of 30 characters", []ActivityWatch{watch("x.exe", strings.Repeat("가", 30), "game")}, ""},
		{"twenty entries", twentyOne[:20], ""},
		{"twenty-one entries", twentyOne, "at most 20"},
		{"missing process", []ActivityWatch{watch("", "Steam", "game")}, "process is required"},
		{"a path", []ActivityWatch{watch(`C:\Games\steam.exe`, "Steam", "game")}, "without a path"},
		{"a forward-slash path", []ActivityWatch{watch("games/steam.exe", "Steam", "game")}, "without a path"},
		{"a wildcard", []ActivityWatch{watch("*.exe", "Any", "game")}, "without a path"},
		{"not an exe", []ActivityWatch{watch("steam.bat", "Steam", "game")}, "must end with .exe"},
		{"no extension", []ActivityWatch{watch("steam", "Steam", "game")}, "must end with .exe"},
		{"only the extension", []ActivityWatch{watch(".exe", "X", "game")}, "must end with .exe"},
		{"control character", []ActivityWatch{watch("st\x01eam.exe", "Steam", "game")}, "control characters"},
		{"label too long", []ActivityWatch{watch("x.exe", strings.Repeat("a", 31), "game")}, "at most 30"},
		{"label control character", []ActivityWatch{watch("x.exe", "a\nb", "game")}, "control characters"},
		{"unknown kind", []ActivityWatch{watch("x.exe", "X", "sleep")}, "kind for"},
		{"duplicate, other case", []ActivityWatch{watch("steam.exe", "Steam", "game"), watch("Steam.EXE", "S", "other")}, "listed twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ActivityConfig{Enabled: true, Watch: tc.watch}.withDefaults()
			got := validateActivity(a)
			switch {
			case tc.want == "" && got != "":
				t.Errorf("rejected: %s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("message = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

func TestActivityDefaultsFillLabelAndKind(t *testing.T) {
	a := ActivityConfig{Watch: []ActivityWatch{{Process: "  Discord.exe ", Label: " ", Kind: " "}}}.withDefaults()
	want := watch("Discord.exe", "Discord", "other")
	if a.Watch[0] != want {
		t.Errorf("normalized = %+v, want %+v", a.Watch[0], want)
	}
	if msg := validateActivity(a); msg != "" {
		t.Errorf("a normalized entry is invalid: %s", msg)
	}
	// Kind is case-insensitive on the way in and stored lower-case.
	if got := (ActivityConfig{Watch: []ActivityWatch{watch("x.exe", "X", "GAME")}}).withDefaults(); got.Watch[0].Kind != "game" {
		t.Errorf("kind = %q", got.Watch[0].Kind)
	}
	if (ActivityConfig{}).withDefaults().Watch == nil {
		t.Error("withDefaults left watch nil (would marshal as null)")
	}
}

func TestSanitizeActivityDropsInvalidEntries(t *testing.T) {
	initLogger()
	in := ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		watch("steam.exe", "Steam", "game"),
		watch(`C:\x\bad.exe`, "Bad", "game"),
		watch("STEAM.exe", "Again", "game"),
		watch("obs64.exe", "OBS", "stream"),
		watch("x.exe", "X", "nonsense"),
	}}
	for i := 0; i < 25; i++ {
		in.Watch = append(in.Watch, watch(strings.Repeat("p", i+1)+".exe", "P", "other"))
	}
	got := sanitizeActivity(in)
	if !got.Enabled {
		t.Error("sanitize turned the option off")
	}
	if len(got.Watch) != activityMaxWatch {
		t.Fatalf("kept %d entries, want the cap %d", len(got.Watch), activityMaxWatch)
	}
	if got.Watch[0].Process != "steam.exe" || got.Watch[1].Process != "obs64.exe" {
		t.Errorf("kept = %+v", got.Watch[:2])
	}
	if msg := validateActivity(got); msg != "" {
		t.Errorf("sanitized config is still invalid: %s", msg)
	}
}

func TestLoadConfigIgnoresInvalidActivityEntries(t *testing.T) {
	initLogger()
	withConfigFile(t, `{"port": 5001, "activity": {"enabled": true, "watch": [
		{"process": "steam.exe", "label": "Steam", "kind": "game"},
		{"process": "C:\\Windows\\notepad.exe", "label": "Notepad", "kind": "work"}
	]}}`)
	cfg := loadConfig()
	if !cfg.Activity.Enabled || len(cfg.Activity.Watch) != 1 || cfg.Activity.Watch[0].Label != "Steam" {
		t.Errorf("activity = %+v", cfg.Activity)
	}

	// An older config.json without the key: off, empty list.
	withConfigFile(t, `{"port": 5001}`)
	cfg = loadConfig()
	if cfg.Activity.Enabled || cfg.Activity.Watch == nil || len(cfg.Activity.Watch) != 0 {
		t.Errorf("default activity = %+v", cfg.Activity)
	}
}

func TestConfigAPIValidatesActivity(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001, Activity: ActivityConfig{
		Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")},
	}})

	w := httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[{"process":"C:\\bad.exe","label":"x","kind":"game"}]}}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a path was accepted: %d %s", w.Code, w.Body.String())
	}
	if getConfig().Activity.Enabled {
		t.Error("a rejected save changed the live config")
	}

	// The WebUI page sends only the switch: the list must survive.
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("toggle only: %d %s", w.Code, w.Body.String())
	}
	got := getConfig().Activity
	if !got.Enabled || len(got.Watch) != 1 || got.Watch[0].Process != "steam.exe" {
		t.Errorf("after toggle-only save: %+v", got)
	}

	// An empty list is a real edit.
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[]}}`))
	if w.Code != http.StatusOK || len(getConfig().Activity.Watch) != 0 {
		t.Errorf("clearing the list: %d, %+v", w.Code, getConfig().Activity)
	}
}

func TestConfigChangedKeysCoversActivity(t *testing.T) {
	old := defaultConfig.withDefaults()
	updated := old
	updated.Activity = ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}
	keys := configChangedKeys(old, updated)
	if !slices.Contains(keys, "activity.enabled") || !slices.Contains(keys, "activity.watch") {
		t.Errorf("configChangedKeys = %v", keys)
	}
	for _, k := range keys {
		if strings.Contains(k, "steam") {
			t.Errorf("a key leaked a value: %v", keys)
		}
	}
}

// ---- matcher ---------------------------------------------------------------

func TestMatchActivity(t *testing.T) {
	list := []ActivityWatch{
		watch("code.exe", "VS Code", "work"),
		watch("steam.exe", "Steam", "game"),
		watch("spotify.exe", "Spotify", "media"),
		watch("obs64.exe", "OBS", "stream"),
		watch("eldenring.exe", "Elden Ring", "game"),
		watch("steamwebhelper.exe", "Steam", "game"),
		watch("notes.exe", "Notes", "other"),
	}
	for _, tc := range []struct {
		name    string
		running []string
		kind    string
		labels  []string
	}{
		{"nothing running", nil, "none", []string{}},
		{"nothing watched running", []string{"explorer.exe", "svchost.exe"}, "none", []string{}},
		{"case-insensitive", []string{"STEAM.EXE"}, "game", []string{"Steam"}},
		{"many instances count once", []string{"Spotify.exe", "spotify.exe", "SPOTIFY.exe"}, "media", []string{"Spotify"}},
		{"same label deduplicated", []string{"steam.exe", "steamwebhelper.exe"}, "game", []string{"Steam"}},
		{"priority: game over the rest", []string{"code.exe", "spotify.exe", "obs64.exe", "steam.exe"},
			"game", []string{"Steam", "OBS", "Spotify", "VS Code"}},
		{"priority: stream over media", []string{"spotify.exe", "obs64.exe"}, "stream", []string{"OBS", "Spotify"}},
		{"priority: work over other", []string{"notes.exe", "code.exe"}, "work", []string{"VS Code", "Notes"}},
		{"list order within a kind", []string{"eldenring.exe", "steam.exe"}, "game", []string{"Steam", "Elden Ring"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := matchActivity(list, tc.running)
			if !got.Enabled || got.Kind != tc.kind || !slices.Equal(got.Labels, tc.labels) {
				t.Errorf("got %+v, want kind %s labels %v", got, tc.kind, tc.labels)
			}
		})
	}
}

func TestMatchActivityNeverCarriesUnlistedNames(t *testing.T) {
	list := []ActivityWatch{watch("steam.exe", "Steam", "game")}
	running := []string{"steam.exe", "secret-project.exe", "bank-client.exe"}
	got := matchActivity(list, running)
	for _, s := range append([]string{got.Kind}, got.Labels...) {
		if strings.Contains(s, "secret") || strings.Contains(s, "bank") || strings.HasSuffix(s, ".exe") {
			t.Errorf("result carries %q: %+v", s, got)
		}
	}
}

// ---- scanner ---------------------------------------------------------------

func TestActivityScannerDisabledDoesNotReadProcesses(t *testing.T) {
	calls := stubProcesses(t, "steam.exe")
	cfg := ActivityConfig{Enabled: false, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}
	got, changed := activityScan.scan(cfg)
	if calls.Load() != 0 {
		t.Errorf("the process list was read %d times while the option is off", calls.Load())
	}
	if changed || got.Enabled || got.Kind != "none" || got.Labels == nil || len(got.Labels) != 0 {
		t.Errorf("disabled scan = %+v changed=%v", got, changed)
	}
	if cur := activityScan.current(cfg); cur.Enabled || cur.Kind != "none" || cur.Labels == nil {
		t.Errorf("disabled status = %+v", cur)
	}
}

func TestActivityScannerChangeDetection(t *testing.T) {
	running := []string{"explorer.exe"}
	orig := processLister
	processLister = func() ([]string, error) { return slices.Clone(running), nil }
	activityScan.reset()
	t.Cleanup(func() { processLister = orig; activityScan.reset() })

	cfg := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}
	if _, changed := activityScan.scan(cfg); changed {
		t.Error("the first scan reported a change (it only sets the baseline)")
	}
	if _, changed := activityScan.scan(cfg); changed {
		t.Error("an unchanged process list reported a change")
	}
	running = []string{"explorer.exe", "Steam.exe"}
	got, changed := activityScan.scan(cfg)
	if !changed || got.Kind != "game" || !slices.Equal(got.Labels, []string{"Steam"}) {
		t.Errorf("steam started: %+v changed=%v", got, changed)
	}
	if cur := activityScan.current(cfg); cur.Kind != "game" {
		t.Errorf("status after the scan = %+v", cur)
	}
	// Disabling is a change the hub hears about.
	off := cfg
	off.Enabled = false
	if got, changed := activityScan.scan(off); !changed || got.Enabled {
		t.Errorf("disabling: %+v changed=%v", got, changed)
	}
}

func TestActivityStatusIgnoresScanForAnotherList(t *testing.T) {
	stubProcesses(t, "steam.exe")
	cfg := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}
	activityScan.scan(cfg)
	// The user removes the entry: its label must go at once, not on the
	// next tick.
	edited := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("obs64.exe", "OBS", "stream")}}
	if cur := activityScan.current(edited); cur.Kind != "none" || len(cur.Labels) != 0 {
		t.Errorf("status after an edit = %+v, want none until rescanned", cur)
	}
}

func TestActivityScannerListFailureReportsNone(t *testing.T) {
	initLogger()
	orig := processLister
	processLister = func() ([]string, error) { return nil, errors.New("snapshot refused") }
	activityScan.reset()
	t.Cleanup(func() { processLister = orig; activityScan.reset() })
	cfg := ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}
	if got, _ := activityScan.scan(cfg); !got.Enabled || got.Kind != "none" {
		t.Errorf("failed scan = %+v", got)
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
	if act["enabled"] != false || act["kind"] != "none" {
		t.Errorf("disabled activity = %v", got["activity"])
	}
	if labels, ok := act["labels"].([]any); !ok || len(labels) != 0 {
		t.Errorf("disabled labels = %v (want [])", act["labels"])
	}
	if f, ok := got["features"].([]any); !ok || slices.Contains(f, any("activity")) {
		t.Errorf("features while off = %v", got["features"])
	}

	cfg := Config{Port: 5001, Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}}
	setConfig(cfg)
	activityScan.scan(cfg.Activity)
	resetSTRateLimit()
	w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", "")
	got = stJSON(t, w)
	act, _ = got["activity"].(map[string]any)
	if act["enabled"] != true || act["kind"] != "game" {
		t.Errorf("activity = %v", got["activity"])
	}
	if labels, _ := act["labels"].([]any); len(labels) != 1 || labels[0] != "Steam" {
		t.Errorf("labels = %v", act["labels"])
	}
	if f, _ := got["features"].([]any); !slices.Contains(f, any("activity")) {
		t.Errorf("features = %v", got["features"])
	}
	if body := w.Body.String(); strings.Contains(body, "private") || strings.Contains(body, "steam.exe") {
		t.Errorf("status leaks a process name: %s", body)
	}
}

func TestActivityPushEventSelection(t *testing.T) {
	if typ, ok := stPushEventType(notify.Event{Category: "activity", Kind: "changed"}, SmartThingsConfig{}); !ok || typ != "activity.changed" {
		t.Errorf("activity.changed pushed = %v (%q)", ok, typ)
	}
	if _, ok := stPushEventType(notify.Event{Category: "activity", Kind: "other"}, SmartThingsConfig{}); ok {
		t.Error("an unknown activity kind is pushed")
	}
}

func TestActivityChangePushes(t *testing.T) {
	running := []string{"explorer.exe"}
	orig := processLister
	processLister = func() ([]string, error) { return slices.Clone(running), nil }
	activityScan.reset()
	t.Cleanup(func() { processLister = orig; activityScan.reset() })

	cfg := Config{Port: 5001, Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("obs64.exe", "OBS", "stream")}}}
	stPushSetup(t, cfg)
	startNotifier(nil) // a bus with the push tap, no notification sink
	t.Cleanup(stopNotifier)
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	activityTick(cfg.Activity) // baseline, no push
	running = []string{"explorer.exe", "obs64.exe", "diary.exe"}
	activityTick(cfg.Activity)
	got := cb.wait(t)
	if got["type"] != "activity.changed" {
		t.Fatalf("type = %v", got["type"])
	}
	data, _ := got["data"].(map[string]any)
	if data["kind"] != "stream" || data["labels"] != "OBS" || data["enabled"] != "true" {
		t.Errorf("data = %v", data)
	}
	status, _ := got["status"].(map[string]any)
	if act, _ := status["activity"].(map[string]any); act["kind"] != "stream" {
		t.Errorf("status.activity = %v", status["activity"])
	}
	cb.mu.Lock()
	body := string(cb.bodies[len(cb.bodies)-1])
	cb.mu.Unlock()
	if strings.Contains(body, "diary") || strings.Contains(body, "obs64") {
		t.Errorf("push body leaks a process name: %s", body)
	}

	// No change, no push.
	before := cb.hits.Load()
	activityTick(cfg.Activity)
	time.Sleep(200 * time.Millisecond)
	if after := cb.hits.Load(); after != before {
		t.Errorf("an unchanged scan pushed again (%d → %d)", before, after)
	}
}

// ---- /api/processes ----------------------------------------------------------

func TestProcessesAPI(t *testing.T) {
	stubProcesses(t, "svchost.exe", "System", "[System Process]", "Registry", "Steam.exe", "steam.exe", "explorer.exe", "  ", "Code.exe")
	withLiveConfig(t, Config{Port: 5001})

	r := httptest.NewRequest("GET", "/api/processes", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	handleProcessesAPI(w, r)
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
	handleProcessesAPI(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "exe") {
		t.Errorf("LAN caller: %d %s", w.Code, w.Body.String())
	}

	// A secret requires the session cookie.
	withLiveConfig(t, Config{Port: 5001, Secret: "s3cr3t"})
	r = httptest.NewRequest("GET", "/api/processes", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w = httptest.NewRecorder()
	handleProcessesAPI(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", w.Code)
	}
}

// ---- Telegram ----------------------------------------------------------------

func TestTelegramActivityLine(t *testing.T) {
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"}})
	t.Cleanup(func() { setConfig(Config{Port: 5001}) })
	for _, tc := range []struct {
		in   stActivity
		want string
	}{
		{activityOff(), ""},
		{stActivity{Enabled: true, Kind: "none", Labels: []string{}}, ""},
		{stActivity{Enabled: true, Kind: "game", Labels: []string{"Steam"}}, "활동: 게임 중 · Steam"},
		{stActivity{Enabled: true, Kind: "stream", Labels: []string{"OBS", "Spotify"}}, "활동: 방송 중 · OBS, Spotify"},
		{stActivity{Enabled: true, Kind: "work", Labels: []string{"<b>"}}, "활동: 작업 중 · &lt;b&gt;"},
	} {
		if got := tgActivityLine(tc.in); got != tc.want {
			t.Errorf("tgActivityLine(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "en"}})
	if got := tgActivityLine(stActivity{Enabled: true, Kind: "media", Labels: []string{"Spotify"}}); got != "Activity: Playing media · Spotify" {
		t.Errorf("en line = %q", got)
	}
	for _, k := range activityKinds {
		if tgText("activity_kind_"+k) == "activity_kind_"+k {
			t.Errorf("no Telegram label for kind %s", k)
		}
	}
}

func TestTelegramStatusShowsActivity(t *testing.T) {
	initLogger()
	stubProcesses(t, "steam.exe")
	cfg := Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"},
		Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch("steam.exe", "Steam", "game")}}}
	setConfig(cfg)
	t.Cleanup(func() { setConfig(Config{Port: 5001}) })
	activityScan.scan(cfg.Activity)
	if got := tgStatusText(); !strings.Contains(got, "\n활동: 게임 중 · Steam") {
		t.Errorf("/status lacks the activity line:\n%s", got)
	}
	cfg.Activity.Enabled = false
	setConfig(cfg)
	if got := tgStatusText(); strings.Contains(got, "활동") {
		t.Errorf("/status shows activity while the option is off:\n%s", got)
	}
}
