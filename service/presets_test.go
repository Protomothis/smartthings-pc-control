package service

// Tests for presets (#109): config validation, the /st/v1 preset command,
// the status block, the security key and the app's API.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

func TestLoadConfigIgnoresInvalidPresets(t *testing.T) {
	path := protectConfigFile(t)
	os.WriteFile(path, []byte(`{"port":5001,"presets":[
		{"slot":2,"name":"ok","type":"url","path":"https://ok.example"},
		{"slot":1,"name":"bad","type":"program","path":"notepad.exe"},
		{"slot":3,"name":"also ok","type":"program","path":"C:\\Windows\\notepad.exe","args":["a b"]}
	]}`), 0644)
	cfg := loadConfig()
	if len(cfg.Presets) != 2 || cfg.Presets[0].Slot != 2 || cfg.Presets[1].Slot != 3 {
		t.Errorf("presets = %+v, want slots 2 and 3 (sorted, invalid dropped)", cfg.Presets)
	}
	if !cfg.NotifyPC.Enabled {
		t.Error("a config without notify_pc should keep notifications on")
	}
}

func TestConfigAPIPresets(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001})
	initLogger()

	w := httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"presets":[{"slot":1,"name":"x","type":"program","path":"notepad.exe"}]}`))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "absolute") {
		t.Fatalf("invalid preset: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"presets":[
		{"slot":4,"name":"b","type":"url","path":"https://b.example"},
		{"slot":2,"name":"a","type":"url","path":"https://a.example"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("valid presets: %d %s", w.Code, w.Body.String())
	}
	got := getConfig().Presets
	if len(got) != 2 || got[0].Slot != 2 || got[1].Slot != 4 {
		t.Errorf("stored = %+v", got)
	}
	// A body without presets keeps them (an older WebUI page).
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"notify_pc":{"enabled":false}}`))
	if w.Code != http.StatusOK || len(getConfig().Presets) != 2 || getConfig().NotifyPC.Enabled {
		t.Errorf("omitted presets: %d, %+v", w.Code, getConfig())
	}
	// [] clears them.
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"presets":[]}`))
	if w.Code != http.StatusOK || len(getConfig().Presets) != 0 {
		t.Errorf("cleared: %d, %+v", w.Code, getConfig().Presets)
	}
	// The retired notify_pc.speak/voice keys of an older app are ignored.
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"notify_pc":{"enabled":true,"speak":true,"voice":"a\nb"}}`))
	if w.Code != http.StatusOK || !getConfig().NotifyPC.Enabled {
		t.Errorf("old speech keys: %d, %+v", w.Code, getConfig().NotifyPC)
	}
}

// fakePresetRun replaces the user-action runner for presets.
func fakePresetRun(t *testing.T, reply string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	saved := presetRun
	presetRun = func(ctx context.Context, args ...string) (UserActionResult, error) {
		calls = append(calls, args)
		if err != nil {
			return UserActionResult{}, err
		}
		return session.ParseOutput([]byte(reply))
	}
	t.Cleanup(func() { presetRun = saved })
	return &calls
}

var testPresets = []Preset{
	{Slot: 3, Name: "게임 모드", Type: "program", Path: `C:\Games\Steam\steam.exe`, Args: []string{"-bigpicture", "two words"}},
	{Slot: 1, Name: "대시보드", Type: "url", Path: "https://example.com/d"},
}

func TestSTPresetCommand(t *testing.T) {
	stSetup(t, Config{Port: 5001, Presets: testPresets})
	stubAwake(t)
	calls := fakePresetRun(t, `{"ok":true,"started":true}`, nil)

	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"preset","value":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := stJSON(t, w)
	p, _ := body["preset"].(map[string]any)
	if body["accepted"] != true || body["executed"] != true || p["slot"] != float64(3) || p["name"] != "게임 모드" || p["started"] != true {
		t.Errorf("body = %v", body)
	}
	want := []string{"preset", "--type", "program", "--path", `C:\Games\Steam\steam.exe`, "--arg", "-bigpicture", "--arg", "two words"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("args = %q, want %q", *calls, want)
	}

	status := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	lc, _ := status["last_command"].(map[string]any)
	lp, _ := lc["preset"].(map[string]any)
	if lc["command"] != "preset" || lc["origin"] != "smartthings" || lc["result"] != "started" || lp["slot"] != float64(3) || lp["name"] != "게임 모드" {
		t.Errorf("last_command = %v", status["last_command"])
	}
	if got := fmt.Sprint(status["presets"]); got != "[map[name:대시보드 slot:1] map[name:게임 모드 slot:3]]" {
		t.Errorf("status presets = %s", got)
	}
	features := fmt.Sprint(status["features"])
	if !strings.Contains(features, "presets") {
		t.Errorf("features = %s", features)
	}
	// Nothing about what a slot runs leaves the PC.
	if raw := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", "").Body.String(); strings.Contains(raw, "steam.exe") || strings.Contains(raw, "bigpicture") {
		t.Errorf("status leaks preset details: %s", raw)
	}
	// No presets: an empty list, and still the "presets" feature (the driver
	// tells an empty slot from an old service by it).
	setConfig(Config{Port: 5001})
	status = stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	if ps, ok := status["presets"].([]any); !ok || len(ps) != 0 || !strings.Contains(fmt.Sprint(status["features"]), "presets") {
		t.Errorf("no presets: presets %v, features %v", status["presets"], status["features"])
	}
}

func TestSTPresetCommandRefusals(t *testing.T) {
	stSetup(t, Config{Port: 5001, Presets: testPresets})
	calls := fakePresetRun(t, `{"ok":true,"started":true}`, nil)
	for _, c := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"command":"preset"}`, http.StatusBadRequest, ""},
		{`{"command":"preset","value":0}`, http.StatusBadRequest, ""},
		{`{"command":"preset","value":11}`, http.StatusBadRequest, ""},
		{`{"command":"preset","value":3,"minutes":5}`, http.StatusBadRequest, ""},
		{`{"command":"preset","value":"3"}`, http.StatusBadRequest, ""},
		{`{"command":"preset","value":2}`, http.StatusNotFound, "no_such_preset"},
	} {
		stSrv.ResetRateLimit()
		w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", c.body)
		if w.Code != c.status || (c.code != "" && stJSON(t, w)["error"] != c.code) {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	if len(*calls) != 0 {
		t.Errorf("refused commands ran: %q", *calls)
	}
}

func TestSTPresetCommandUserSessionErrors(t *testing.T) {
	stSetup(t, Config{Port: 5001, Presets: testPresets})
	stubAwake(t)
	fakePresetRun(t, "", fmt.Errorf("get session: %w", errNoUserSession))
	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"preset","value":1}`)
	if w.Code != http.StatusConflict || stJSON(t, w)["error"] != "no_user_session" {
		t.Fatalf("no session: %d %s", w.Code, w.Body.String())
	}
	lc, _ := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))["last_command"].(map[string]any)
	if lc["result"] != "no_user_session" {
		t.Errorf("last_command = %v", lc)
	}

	// A reply that does not confirm the start is a failure.
	fakePresetRun(t, `{"ok":true}`, nil)
	stSrv.ResetRateLimit()
	if w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"preset","value":1}`); w.Code != http.StatusBadGateway {
		t.Errorf("unconfirmed start: %d %s", w.Code, w.Body.String())
	}
	fakePresetRun(t, `{"ok":false,"error":"failed","message":"start steam.exe: file not found"}`, nil)
	stSrv.ResetRateLimit()
	w = stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"preset","value":3}`)
	if w.Code != http.StatusBadGateway || stJSON(t, w)["error"] != "failed" {
		t.Errorf("failed start: %d %s", w.Code, w.Body.String())
	}
}

func TestPresetsAPI(t *testing.T) {
	withLiveConfig(t, Config{Port: 5001, Presets: testPresets})
	initLogger()
	calls := fakePresetRun(t, `{"ok":true,"started":true}`, nil)

	w := httptest.NewRecorder()
	handlePresetsRunAPI(w, postJSON("/api/presets/run", `{"slot":1}`))
	if w.Code != http.StatusOK || decodeBody(t, w)["started"] != true {
		t.Errorf("run: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handlePresetsRunAPI(w, postJSON("/api/presets/run", `{"slot":9}`))
	if w.Code != http.StatusNotFound || decodeBody(t, w)["error"] != "no_such_preset" {
		t.Errorf("unknown slot: %d %s", w.Code, w.Body.String())
	}

	// The editor's test runs an unsaved row, through the save rules.
	w = httptest.NewRecorder()
	handlePresetsTestAPI(w, postJSON("/api/presets/test", `{"type":"script","path":"C:\\s\\go.ps1","args":["-x"]}`))
	if w.Code != http.StatusOK {
		t.Errorf("test: %d %s", w.Code, w.Body.String())
	}
	if got := (*calls)[len(*calls)-1]; !reflect.DeepEqual(got, []string{"preset", "--type", "script", "--path", `C:\s\go.ps1`, "--arg", "-x"}) {
		t.Errorf("test args = %q", got)
	}
	n := len(*calls)
	w = httptest.NewRecorder()
	handlePresetsTestAPI(w, postJSON("/api/presets/test", `{"type":"program","path":"notepad.exe"}`))
	if w.Code != http.StatusBadRequest || len(*calls) != n {
		t.Errorf("invalid test: %d, ran %d", w.Code, len(*calls)-n)
	}
	w = httptest.NewRecorder()
	handlePresetsRunAPI(w, httptest.NewRequest("POST", "/api/presets/run", strings.NewReader(`{"slot":1}`)))
	if w.Code != http.StatusForbidden {
		t.Errorf("without CSRF header: %d", w.Code)
	}
}
