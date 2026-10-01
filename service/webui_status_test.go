package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withWebUISession logs a browser in for one test: the cookie it returns
// matches the live session token.
func withWebUISession(t *testing.T) *http.Cookie {
	t.Helper()
	sessionMu.Lock()
	saved := sessionToken
	sessionToken = "page-session"
	sessionMu.Unlock()
	t.Cleanup(func() {
		sessionMu.Lock()
		sessionToken = saved
		sessionMu.Unlock()
	})
	return &http.Cookie{Name: "session", Value: "page-session"}
}

func getPage(t *testing.T, path string, cookie *http.Cookie, remote bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "http://192.168.1.30:5002"+path, nil)
	req.RemoteAddr = "192.168.1.30:50000"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return serveWebUI(req, remote)
}

const pageSecret = "page-secret-value"

// The page keeps the four remote sections and drops everything the
// desktop app now owns (#122).
func TestSettingsPageIsTheCompactRemotePage(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, Secret: pageSecret, WebUIRemote: true, ShutdownGrace: true, GraceSeconds: 45})
	w := getPage(t, "/", withWebUISession(t), true)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
		t.Fatal("the template did not render to the end")
	}
	for _, kept := range []string{
		`data-section="status"`, `data-section="power"`, `data-section="schedule"`, `data-section="settings"`,
		"/api/status", "/api/command", "/api/schedule", "/api/config", "X-Requested-With",
		`id="port"`, `id="secret"`, `id="webuiRemote"`, `id="shutdownGrace"`, `id="graceSeconds" min="5" max="3600" value="45"`,
		"나머지 설정은 PC의 앱에서", "Other settings are in the PC app",
		"toggleLang", "toggleTheme", `data-theme="dark"`,
	} {
		if !strings.Contains(body, kept) {
			t.Errorf("page lost %q", kept)
		}
	}
	for _, removed := range []string{
		// logs, the WoL panel and the WoL adapter dropdown
		"logViewer", "/api/logs", "/api/wol-status", "/api/st/hub", "stWolMac",
		// SmartThings exposure and hubs, Telegram, media, PC notifications, activity
		"stSessionUser", "stHubs", "tgPCName", "mediaEnabled", "nowPlaying", "notifyPC", "activityEnabled",
		// presets, keep-awake and the rest of the app-only features
		"/api/presets", "/api/awake", "/api/telegram", "/api/media", "/api/processes",
	} {
		if strings.Contains(body, removed) {
			t.Errorf("page still contains %q", removed)
		}
	}
	// The secret is no longer printed into the page.
	if strings.Contains(body, pageSecret) {
		t.Error("the page leaks the secret")
	}
}

func TestSettingsPageStaysGuarded(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, Secret: pageSecret, WebUIRemote: true})
	withWebUISession(t)
	w := getPage(t, "/", &http.Cookie{Name: "session", Value: "stale"}, true)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("GET / without a session = %d → %q, want 302 → /login", w.Code, w.Header().Get("Location"))
	}
	// Browser access off: the disabled notice, even with a session.
	if w := getPage(t, "/", withWebUISession(t), false); w.Code != http.StatusForbidden {
		t.Errorf("GET / with remote access off = %d, want 403", w.Code)
	}
}

func TestWebUIStatusAPI(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, Secret: pageSecret, WebUIRemote: true, ShutdownGrace: true, GraceSeconds: 30,
		SmartThings: SmartThingsConfig{ExposeSession: true}})
	cookie := withWebUISession(t)

	savedVersion, savedLatest, savedUptime, savedQuery := Version, latestReleaseTag(), webUIUptime, stSessionQuery
	Version = "v1.2.0"
	noteLatestRelease("v1.2.1")
	webUIUptime = func() time.Duration { return 90 * time.Minute }
	stSessionQuery = func() (sessionInfo, error) { return sessionInfo{Locked: true, User: "kim"}, nil }
	hubLastSeenMu.Lock()
	savedHub := hubLastSeen
	hubLastSeen = hubSeen{IP: "192.168.1.20", DriverVersion: "1.1.0", At: time.Now().Add(-time.Minute)}
	hubLastSeenMu.Unlock()
	t.Cleanup(func() {
		Version, webUIUptime, stSessionQuery = savedVersion, savedUptime, savedQuery
		latestRelease.Store(savedLatest)
		hubLastSeenMu.Lock()
		hubLastSeen = savedHub
		hubLastSeenMu.Unlock()
	})

	if w := getPage(t, "/api/status", nil, true); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/status without a session = %d, want 401", w.Code)
	}
	post := httptest.NewRequest("POST", "http://192.168.1.30:5002/api/status", nil)
	post.AddCookie(cookie)
	post.Header.Set("X-Requested-With", "XMLHttpRequest")
	if w := serveWebUI(post, true); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/status = %d, want 405", w.Code)
	}
	foreign := httptest.NewRequest("GET", "http://evil.example:5002/api/status", nil)
	if w := serveWebUI(foreign, false); w.Code != http.StatusForbidden {
		t.Errorf("GET /api/status with a foreign Host = %d, want 403", w.Code)
	}

	w := getPage(t, "/api/status", cookie, true)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want 200", w.Code)
	}
	var st webUIStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != "v1.2.0" || !st.Update.Available || st.Update.Latest != "v1.2.1" {
		t.Errorf("version/update = %q %+v", st.Version, st.Update)
	}
	if st.UptimeSeconds != 5400 {
		t.Errorf("uptime_seconds = %d, want 5400", st.UptimeSeconds)
	}
	if !st.Grace.Enabled || st.Grace.Seconds != 30 {
		t.Errorf("grace = %+v, want on/30", st.Grace)
	}
	if !st.SmartThings.Connected || st.SmartThings.DriverVersion != "1.1.0" || st.SmartThings.LastSeen == "" {
		t.Errorf("smartthings = %+v", st.SmartThings)
	}
	if st.Schedule["active"] != false {
		t.Errorf("schedule = %v, want inactive", st.Schedule)
	}
	// Session follows the SmartThings exposure settings: locked, no user.
	if !st.Session.Exposed || st.Session.Locked == nil || !*st.Session.Locked || st.Session.User != "" {
		t.Errorf("session = %+v, want exposed+locked without the user", st.Session)
	}
	// The status carries none of the config: no secret, no token.
	if strings.Contains(w.Body.String(), pageSecret) {
		t.Error("/api/status leaks the secret")
	}
}

func TestWebUISessionFollowsExposure(t *testing.T) {
	saved := stSessionQuery
	t.Cleanup(func() { stSessionQuery = saved })
	stSessionQuery = func() (sessionInfo, error) { return sessionInfo{Locked: false, User: "kim"}, nil }

	if s := webUISession(SmartThingsConfig{}); s.Exposed || s.Locked != nil || s.User != "" {
		t.Errorf("not exposed = %+v, want nothing", s)
	}
	if s := webUISession(SmartThingsConfig{ExposeSession: true, ExposeSessionUser: true}); !s.Exposed || s.Locked == nil || *s.Locked || s.User != "kim" {
		t.Errorf("exposed with user = %+v", s)
	}
	stSessionQuery = func() (sessionInfo, error) { return sessionInfo{}, errors.New("no session") }
	if s := webUISession(SmartThingsConfig{ExposeSession: true}); !s.Exposed || s.Locked != nil {
		t.Errorf("nobody signed in = %+v, want exposed with locked unset", s)
	}
}

// The page saves only the core keys; whatever the app manages survives a
// save from the browser.
func TestConfigPostFromPageKeepsAppSettings(t *testing.T) {
	protectConfigFile(t)
	cfg := defaultConfig
	cfg.Port, cfg.Secret, cfg.WebUIRemote = 5001, pageSecret, true
	cfg.Telegram.PCName = "desk"
	cfg.Media.NowPlaying = true
	withWebUIConfig(t, cfg)
	cookie := withWebUISession(t)

	req := httptest.NewRequest("POST", "http://192.168.1.30:5002/api/config",
		strings.NewReader(`{"port":5001,"webui_remote":true,"shutdown_grace":true,"grace_seconds":20}`))
	req.AddCookie(cookie)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if w := serveWebUI(req, true); w.Code != http.StatusOK {
		t.Fatalf("POST /api/config = %d %s", w.Code, w.Body.String())
	}
	got := getConfig()
	if got.Secret != pageSecret {
		t.Error("a save without a secret key changed the secret")
	}
	if !got.ShutdownGrace || got.GraceSeconds != 20 {
		t.Errorf("grace = %v/%d, want on/20", got.ShutdownGrace, got.GraceSeconds)
	}
	if got.Telegram.PCName != "desk" || !got.Media.NowPlaying {
		t.Errorf("app settings lost: pc_name=%q now_playing=%v", got.Telegram.PCName, got.Media.NowPlaying)
	}

	// Without the CSRF header nothing is saved.
	req = httptest.NewRequest("POST", "http://192.168.1.30:5002/api/config", strings.NewReader(`{"grace_seconds":99}`))
	req.AddCookie(cookie)
	if w := serveWebUI(req, true); w.Code != http.StatusForbidden {
		t.Errorf("POST /api/config without X-Requested-With = %d, want 403", w.Code)
	}
}
