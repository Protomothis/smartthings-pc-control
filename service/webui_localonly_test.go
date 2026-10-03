package service

// The local-only endpoints (review C5): preset test, the process list, and
// a config save that changes the presets or the watch list need the
// desktop app's local trusted session (POST /api/local-login), whatever
// the secret. GET /api/config masks what presets run for anyone else.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// localOnlyCaller is one kind of client of the matrix.
type localOnlyCaller struct {
	name string
	// prepare turns a bare request into this caller's.
	prepare func(t *testing.T, r *http.Request) *http.Request
}

const browserToken = "browser-session"

var localOnlyCallers = []localOnlyCaller{
	{"no session (loopback)", func(t *testing.T, r *http.Request) *http.Request {
		r.RemoteAddr = "127.0.0.1:50000"
		return r
	}},
	{"secret session from the LAN", func(t *testing.T, r *http.Request) *http.Request {
		r.RemoteAddr = "192.168.1.30:50000"
		r.AddCookie(&http.Cookie{Name: "session", Value: browserToken})
		return r
	}},
	{"secret session from loopback", func(t *testing.T, r *http.Request) *http.Request {
		// The app's login dialog (a standard user) or a browser on the PC.
		r.RemoteAddr = "127.0.0.1:50000"
		r.AddCookie(&http.Cookie{Name: "session", Value: browserToken})
		return r
	}},
	{"local session cookie from the LAN", func(t *testing.T, r *http.Request) *http.Request {
		r = asLocalApp(t, r)
		r.RemoteAddr = "192.168.1.30:50000"
		return r
	}},
	{"local trusted session", func(t *testing.T, r *http.Request) *http.Request {
		return asLocalApp(t, r)
	}},
}

// localOnlyRequests are the guarded calls, each a fresh request.
var localOnlyRequests = []struct {
	name string
	make func() *http.Request
}{
	{"POST /api/presets/test", func() *http.Request {
		return postJSON("/api/presets/test", `{"slot":2,"name":"t","type":"url","path":"https://example.com/t"}`)
	}},
	{"GET /api/processes", func() *http.Request {
		r := httptest.NewRequest("GET", "/api/processes", nil)
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		return r
	}},
	{"POST /api/config changing presets", func() *http.Request {
		return postJSON("/api/config", `{"port":5001,"presets":[{"slot":2,"name":"new","type":"url","path":"https://example.com/new"}]}`)
	}},
	{"POST /api/config changing activity.watch", func() *http.Request {
		return postJSON("/api/config", `{"port":5001,"activity":{"enabled":true,"watch":[{"slot":1,"process":"steam.exe","label":"Steam"}]}}`)
	}},
}

func TestLocalOnlyMatrix(t *testing.T) {
	protectConfigFile(t)
	initLogger()
	stubProcesses(t, "steam.exe")
	fakePresetRun(t, `{"ok":true,"started":true}`, nil)
	savedBrowser := webSrv.SetSessionToken(browserToken)
	t.Cleanup(func() { webSrv.SetSessionToken(savedBrowser) })

	for _, secret := range []string{"", "s3cr3t"} {
		for _, req := range localOnlyRequests {
			for _, caller := range localOnlyCallers {
				name := req.name + " / " + caller.name + " / secret=" + map[bool]string{true: "set", false: "none"}[secret != ""]
				t.Run(name, func(t *testing.T) {
					withLiveConfig(t, Config{Port: 5001, Secret: secret, Presets: testPresets})
					w := httptest.NewRecorder()
					webAPI(w, caller.prepare(t, req.make()))

					switch {
					case caller.name == "local trusted session":
						if w.Code != http.StatusOK {
							t.Errorf("%d %s, want 200", w.Code, w.Body.String())
						}
					case secret != "" && (caller.name == "no session (loopback)" || caller.name == "local session cookie from the LAN"):
						// apiAuth answers first.
						if w.Code != http.StatusUnauthorized {
							t.Errorf("%d %s, want 401", w.Code, w.Body.String())
						}
					default:
						var body map[string]any
						json.Unmarshal(w.Body.Bytes(), &body)
						if w.Code != http.StatusForbidden || body["error"] != "local_only" {
							t.Errorf("%d %s, want 403 local_only", w.Code, w.Body.String())
						}
					}
				})
			}
		}
	}
}

// TestConfigSaveWithoutLocalSession: a secret login still saves everything
// that is not the presets or the watch list — the compact WebUI page's core
// keys, the activity switch, and a body that repeats the lists unchanged.
func TestConfigSaveWithoutLocalSession(t *testing.T) {
	protectConfigFile(t)
	initLogger()
	running := []string{}
	stubRunning(t, &running)
	savedBrowser := webSrv.SetSessionToken(browserToken)
	t.Cleanup(func() { webSrv.SetSessionToken(savedBrowser) })
	withLiveConfig(t, Config{Port: 5001, Secret: "s3cr3t", Presets: testPresets,
		Activity: ActivityConfig{Watch: []ActivityWatch{watch(1, "steam.exe", "Steam")}}})

	remote := func(body string) *httptest.ResponseRecorder {
		r := postJSON("/api/config", body)
		r.RemoteAddr = "192.168.1.30:50000"
		r.AddCookie(&http.Cookie{Name: "session", Value: browserToken})
		w := httptest.NewRecorder()
		webAPI(w, r)
		return w
	}
	if w := remote(`{"port":5001,"webui_remote":false,"shutdown_grace":true,"grace_seconds":90}`); w.Code != http.StatusOK {
		t.Errorf("core keys: %d %s", w.Code, w.Body.String())
	}
	if w := remote(`{"port":5001,"activity":{"enabled":true}}`); w.Code != http.StatusOK || !getConfig().Activity.Enabled {
		t.Errorf("activity switch: %d %s", w.Code, w.Body.String())
	}
	presets, _ := json.Marshal(testPresets)
	if w := remote(`{"port":5001,"presets":` + string(presets) + `,"activity":{"enabled":true,"watch":[{"slot":1,"process":"steam.exe","label":"Steam"}]}}`); w.Code != http.StatusOK {
		t.Errorf("unchanged lists: %d %s", w.Code, w.Body.String())
	}
	// Sending back the masked view is a change, and refused.
	if w := remote(`{"port":5001,"presets":[{"slot":3,"name":"게임 모드","type":"program","path":""},{"slot":1,"name":"대시보드","type":"url","path":""}]}`); w.Code != http.StatusForbidden {
		t.Errorf("masked presets sent back: %d %s", w.Code, w.Body.String())
	}
	if got := getConfig().Presets; len(got) != 2 || got[1].Path != `C:\Games\Steam\steam.exe` {
		t.Errorf("presets changed by a refused save: %+v", got)
	}
}

// TestConfigGetMasksPresetsWithoutLocalSession: path and args are only for
// the desktop app; the watch list's process names stay (user labels).
func TestConfigGetMasksPresetsWithoutLocalSession(t *testing.T) {
	savedBrowser := webSrv.SetSessionToken(browserToken)
	t.Cleanup(func() { webSrv.SetSessionToken(savedBrowser) })
	for _, secret := range []string{"", "s3cr3t"} {
		withLiveConfig(t, Config{Port: 5001, Secret: secret, Presets: testPresets,
			Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch(1, "steam.exe", "Steam")}}})
		get := func(r *http.Request) (int, string) {
			w := httptest.NewRecorder()
			webAPI(w, r)
			return w.Code, w.Body.String()
		}

		browser := httptest.NewRequest("GET", "/api/config", nil)
		browser.RemoteAddr = "192.168.1.30:50000"
		browser.AddCookie(&http.Cookie{Name: "session", Value: browserToken})
		code, body := get(browser)
		if code != http.StatusOK {
			t.Fatalf("secret=%q browser: %d %s", secret, code, body)
		}
		for _, leak := range []string{"steam.exe\",\"args", `Steam\\steam.exe`, "-bigpicture", "two words", "example.com/d"} {
			if strings.Contains(body, leak) {
				t.Errorf("secret=%q: the masked view carries %q: %s", secret, leak, body)
			}
		}
		var view struct {
			Presets []struct {
				Slot int      `json:"slot"`
				Name string   `json:"name"`
				Type string   `json:"type"`
				Path string   `json:"path"`
				Args []string `json:"args"`
			} `json:"presets"`
			Activity struct {
				Watch []ActivityWatch `json:"watch"`
			} `json:"activity"`
		}
		json.Unmarshal([]byte(body), &view)
		if len(view.Presets) != 2 || view.Presets[1].Name != "게임 모드" || view.Presets[1].Type != "program" ||
			view.Presets[1].Slot != 3 || view.Presets[1].Path != "" || len(view.Presets[1].Args) != 0 {
			t.Errorf("secret=%q masked presets = %+v", secret, view.Presets)
		}
		if len(view.Activity.Watch) != 1 || view.Activity.Watch[0].Process != "steam.exe" {
			t.Errorf("secret=%q watch list = %+v, want the names kept", secret, view.Activity.Watch)
		}

		// The desktop app sees everything.
		code, body = get(asLocalApp(t, httptest.NewRequest("GET", "/api/config", nil)))
		if code != http.StatusOK || !strings.Contains(body, `C:\\Games\\Steam\\steam.exe`) || !strings.Contains(body, "-bigpicture") {
			t.Errorf("secret=%q local app: %d %s", secret, code, body)
		}
	}
}
