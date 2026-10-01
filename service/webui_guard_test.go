package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withWebUIConfig sets the live config for one test and restores it after.
func withWebUIConfig(t *testing.T, cfg Config) {
	t.Helper()
	initLogger()
	prev := getConfig()
	setConfig(cfg)
	t.Cleanup(func() { setConfig(prev) })
}

// commandRequest builds a request to the local command endpoint as the app
// sends it; csrf adds the X-Requested-With header.
func commandRequest(method, body string, csrf bool) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:5002/api/command", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	return req
}

func serveWebUI(req *http.Request, remote bool) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	webUIHandler(5002, remote).ServeHTTP(w, req)
	return w
}

func TestCommandAPIRejectsGET(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})
	executed := stubCommand(t, "forceshutdown")

	if w := serveWebUI(commandRequest("GET", "", true), false); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/command = %d, want 405", w.Code)
	}
	expectNotExecuted(t, executed, "forceshutdown via GET")
}

// The old GET /api/test/{cmd} must not run anything any more (#120).
func TestOldTestCommandPathIsGone(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})
	executed := stubCommand(t, "forceshutdown")

	for _, method := range []string{"GET", "POST"} {
		req := httptest.NewRequest(method, "http://127.0.0.1:5002/api/test/forceshutdown", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if w := serveWebUI(req, false); w.Code != http.StatusNotFound {
			t.Errorf("%s /api/test/forceshutdown = %d, want 404", method, w.Code)
		}
	}
	expectNotExecuted(t, executed, "forceshutdown via /api/test")
}

func TestCommandAPIRequiresCSRFHeader(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})
	executed := stubCommand(t, "lock")

	if w := serveWebUI(commandRequest("POST", `{"command":"lock"}`, false), false); w.Code != http.StatusForbidden {
		t.Fatalf("POST without X-Requested-With = %d, want 403", w.Code)
	}
	expectNotExecuted(t, executed, "lock without the CSRF header")
}

func TestCommandAPIRequiresSessionWhenSecretSet(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, Secret: "mysecret"})
	executed := stubCommand(t, "lock")

	if w := serveWebUI(commandRequest("POST", `{"command":"lock"}`, true), false); w.Code != http.StatusUnauthorized {
		t.Fatalf("POST without a session = %d, want 401", w.Code)
	}
	expectNotExecuted(t, executed, "lock without a session")
}

func TestCommandAPIRunsNotifiesAndRecords(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, ShutdownGrace: true})
	events := captureNotifications(t)
	executed := stubCommand(t, "shutdown")

	w := serveWebUI(commandRequest("POST", `{"command":"shutdown","by":"app"}`, true), false)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/command = %d (%s), want 200", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["status"] != "ok" || resp["command"] != "shutdown" {
		t.Fatalf("reply = %s", w.Body.String())
	}
	// Immediate even with the grace period on (the user is at the PC): it
	// runs within the test's wait, and the first event is not
	// remote.grace_scheduled.
	expectExecuted(t, executed, "shutdown")
	// The person at the PC pressed it: no Telegram notification.
	expectNoNotification(t, events)
	if lr := getLastRemote(); lr.Command != "shutdown" || lr.Origin != "ui" || lr.From != "app" {
		t.Errorf("last_command = %+v, want shutdown from app (origin ui)", lr)
	}
}

func TestCommandAPIForceShutdownAndCallers(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})
	events := captureNotifications(t)
	executed := stubCommand(t, "forceshutdown")

	// An unlisted "by" is the desktop app; "webui" is kept.
	w := serveWebUI(commandRequest("POST", `{"command":"ForceShutdown","by":"evil"}`, true), false)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/command = %d, want 200", w.Code)
	}
	expectExecuted(t, executed, "forceshutdown")
	expectNoNotification(t, events) // treated as the desktop app: quiet

	serveWebUI(commandRequest("POST", `{"command":"forceshutdown","by":"webui"}`, true), false)
	expectExecuted(t, executed, "forceshutdown")
	if ev := expectNotification(t, events, "remote.force"); ev.Fields["from"] != "webui" {
		t.Errorf("from = %q, want webui", ev.Fields["from"])
	}
}

func TestCommandAPIPingIsQuiet(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})
	events := captureNotifications(t)

	if w := serveWebUI(commandRequest("POST", `{"command":"ping"}`, true), false); w.Code != http.StatusOK {
		t.Fatalf("ping = %d, want 200", w.Code)
	}
	expectNoNotification(t, events)
}

func TestCommandAPIUnknownCommand(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})

	w := serveWebUI(commandRequest("POST", `{"command":"frobnicate"}`, true), false)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown command = %d, want 404", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["status"] != "error" || resp["command"] != "frobnicate" {
		t.Errorf("reply = %s, want a JSON error naming the command", w.Body.String())
	}
	if w := serveWebUI(commandRequest("POST", `not json`, true), false); w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d, want 400", w.Code)
	}
}

func TestWebUIHostAllowed(t *testing.T) {

	cases := []struct {
		host        string
		local       bool // allowed with remote access off
		description string
	}{
		{"127.0.0.1:5002", true, "loopback IPv4"},
		{"localhost:5002", true, "localhost"},
		{"LocalHost:5002", true, "localhost, any case"},
		{"[::1]:5002", true, "loopback IPv6"},
		{"127.0.0.1:5001", false, "wrong port"},
		{"127.0.0.1", false, "no port"},
		{"", false, "empty"},
		{"evil.example:5002", false, "rebound domain"},
		{"localhost.evil.example:5002", false, "localhost prefix"},
		{"127.0.0.1.nip.io:5002", false, "loopback-looking domain"},
		{"192.168.1.30:5002", false, "LAN IP"},
		{"pc.tailnet.ts.net:5002", false, "Tailscale name"},
	}
	for _, c := range cases {
		if got := webUIHostAllowed(c.host, 5002, false); got != c.local {
			t.Errorf("%s (%q), remote off: allowed = %v, want %v", c.description, c.host, got, c.local)
		}
		// Remote access requires a secret and a session cookie on every API
		// call, so any name the user reaches the PC by is fine.
		if !webUIHostAllowed(c.host, 5002, true) {
			t.Errorf("%s (%q), remote on: refused", c.description, c.host)
		}
	}
}

// The Host check runs before any handler: a rebound page cannot even read
// the config, let alone run a command.
func TestWebUIHostGuardRunsBeforeHandlers(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001})
	executed := stubCommand(t, "forceshutdown")

	evil := commandRequest("POST", `{"command":"forceshutdown"}`, true)
	evil.Host = "evil.example:5002"
	if w := serveWebUI(evil, false); w.Code != http.StatusForbidden {
		t.Fatalf("Host evil.example:5002 = %d, want 403", w.Code)
	}
	cfgReq := httptest.NewRequest("GET", "http://evil.example:5002/api/config", nil)
	// Remote access off is the rebinding case (no secret needed, loopback only).
	if w := serveWebUI(cfgReq, false); w.Code != http.StatusForbidden {
		t.Errorf("GET /api/config with a foreign Host = %d, want 403", w.Code)
	}
	expectNotExecuted(t, executed, "forceshutdown with a foreign Host")

	ok := httptest.NewRequest("GET", "http://127.0.0.1:5002/api/schedule", nil)
	if w := serveWebUI(ok, false); w.Code != http.StatusOK {
		t.Errorf("Host 127.0.0.1:5002 = %d, want 200", w.Code)
	}
	lan := httptest.NewRequest("GET", "http://192.168.1.30:5002/api/schedule", nil)
	if w := serveWebUI(lan, false); w.Code != http.StatusForbidden {
		t.Errorf("LAN Host with remote access off = %d, want 403", w.Code)
	}
	lan = httptest.NewRequest("GET", "http://192.168.1.30:5002/api/schedule", nil)
	if w := serveWebUI(lan, true); w.Code != http.StatusOK {
		t.Errorf("LAN Host with remote access on = %d, want 200", w.Code)
	}
}

func TestLegacyPathLocksOutSecretGuessing(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, Secret: "mysecret"})
	const attacker, hub = "10.9.8.7:40000", "10.9.8.8:40000"
	t.Cleanup(func() {
		resetLoginAttempts(attacker)
		resetLoginAttempts(hub)
	})
	get := func(path, from string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = from
		w := httptest.NewRecorder()
		newCommandHandler().ServeHTTP(w, req)
		return w.Code
	}

	for i := 0; i < maxLoginFailures; i++ {
		if code := get("/guess/ping", attacker); code != http.StatusUnauthorized {
			t.Fatalf("guess %d = %d, want 401", i+1, code)
		}
	}
	// Locked out now, even with the right secret.
	if code := get("/mysecret/ping", attacker); code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures = %d, want 429", maxLoginFailures, code)
	}
	// Another address is unaffected, and its success clears its own count.
	if code := get("/wrong/ping", hub); code != http.StatusUnauthorized {
		t.Fatalf("other address, wrong secret = %d, want 401", code)
	}
	if code := get("/mysecret/ping", hub); code != http.StatusOK {
		t.Fatalf("other address, right secret = %d, want 200", code)
	}
	loginAttemptsMu.Lock()
	_, left := loginAttempts["10.9.8.8"]
	loginAttemptsMu.Unlock()
	if left {
		t.Error("a correct secret should clear the address's failure count")
	}
}
