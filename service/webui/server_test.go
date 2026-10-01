package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/session"
)

func TestCheckCSRF(t *testing.T) {
	// GET requests should pass
	req := httptest.NewRequest("GET", "/", nil)
	if !checkCSRF(req) {
		t.Error("GET request should pass CSRF check")
	}

	// POST without header should fail
	req = httptest.NewRequest("POST", "/", nil)
	if checkCSRF(req) {
		t.Error("POST without X-Requested-With should fail CSRF check")
	}

	// POST with header should pass
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if !checkCSRF(req) {
		t.Error("POST with X-Requested-With should pass CSRF check")
	}
}

func TestCheckAuth(t *testing.T) {
	s := newTestServer(t, config.Config{})
	// No secret -> always authenticated
	req := httptest.NewRequest("GET", "/", nil)
	if !s.checkAuth(req, "") {
		t.Error("no secret should always authenticate")
	}

	// Secret set but no cookie -> not authenticated
	if s.checkAuth(req, "mysecret") {
		t.Error("missing cookie should not authenticate")
	}

	// Secret set with valid session

	s.SetSessionToken("valid-token")

	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "valid-token"})
	if !s.checkAuth(req, "mysecret") {
		t.Error("valid session cookie should authenticate")
	}

	// Wrong cookie value
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "wrong-token"})
	if s.checkAuth(req, "mysecret") {
		t.Error("wrong session cookie should not authenticate")
	}

	// Cleanup

	s.SetSessionToken("")

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
		if got := hostAllowed(c.host, 5002, false); got != c.local {
			t.Errorf("%s (%q), remote off: allowed = %v, want %v", c.description, c.host, got, c.local)
		}
		// Remote access requires a secret and a session cookie on every API
		// call, so any name the user reaches the PC by is fine.
		if !hostAllowed(c.host, 5002, true) {
			t.Errorf("%s (%q), remote on: refused", c.description, c.host)
		}
	}
}

func TestWebUISessionFollowsExposure(t *testing.T) {
	ts := newTestServer(t, config.Config{})
	ts.session = func() (session.Info, error) { return session.Info{Locked: false, User: "kim"}, nil }

	if s := ts.sessionBlock(config.SmartThingsConfig{}); s.Exposed || s.Locked != nil || s.User != "" {
		t.Errorf("not exposed = %+v, want nothing", s)
	}
	if s := ts.sessionBlock(config.SmartThingsConfig{ExposeSession: true, ExposeSessionUser: true}); !s.Exposed || s.Locked == nil || *s.Locked || s.User != "kim" {
		t.Errorf("exposed with user = %+v", s)
	}
	ts.session = func() (session.Info, error) { return session.Info{}, errors.New("no session") }
	if s := ts.sessionBlock(config.SmartThingsConfig{ExposeSession: true}); !s.Exposed || s.Locked != nil {
		t.Errorf("nobody signed in = %+v, want exposed with locked unset", s)
	}
}
