package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every /api route but the two logins sits behind apiAuth (#127): with a
// secret and no session it answers 401 before anything else, whatever the
// method, and a POST with no CSRF header is 403.
func TestEveryAPIRouteNeedsASession(t *testing.T) {
	withWebUIConfig(t, Config{Port: 5001, Secret: "s3cret"})
	executed := stubCommand(t, "lock")
	routes := []string{
		"/api/status", "/api/config", "/api/st/hub", "/api/session/heartbeat", "/api/awake",
		"/api/battery", "/api/media", "/api/processes", "/api/notify/test", "/api/presets/run",
		"/api/presets/test", "/api/telegram/test", "/api/telegram/me", "/api/telegram/chats",
		"/api/telegram/state", "/api/command", "/api/restart-service", "/api/logs",
		"/api/wol-status", "/api/schedule",
	}
	for _, path := range routes {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			req := httptest.NewRequest(method, "http://127.0.0.1:5002"+path, strings.NewReader(`{"command":"lock"}`))
			req.RemoteAddr = "127.0.0.1:50000"
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			if w := serveWebUI(req, false); w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without a session = %d, want 401", method, path, w.Code)
			}
		}
	}

	setConfig(Config{Port: 5001}) // no secret: no session needed
	for _, path := range []string{"/api/command", "/api/config", "/api/restart-service", "/api/session/heartbeat", "/api/schedule"} {
		req := httptest.NewRequest("POST", "http://127.0.0.1:5002"+path, strings.NewReader(`{"command":"lock"}`))
		req.RemoteAddr = "127.0.0.1:50000"
		if w := serveWebUI(req, false); w.Code != http.StatusForbidden {
			t.Errorf("POST %s without the CSRF header = %d, want 403", path, w.Code)
		}
	}
	expectNotExecuted(t, executed, "lock without a session or CSRF header")
}
