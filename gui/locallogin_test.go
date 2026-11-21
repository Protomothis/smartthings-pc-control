package gui

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// localService is a service with a secret set: every API route wants the
// "local" session, which /api/local-login hands out with status 200 — or
// refuses with whatever status says.
type localService struct {
	mu       sync.Mutex
	status   int // /api/local-login reply; 0 = route unknown (404)
	logins   int
	csrf     bool // the last local login carried X-Requested-With
	canceled []string
}

func (s *localService) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/api/local-login" {
			s.logins++
			s.csrf = r.Header.Get("X-Requested-With") == "XMLHttpRequest"
			if s.status == 0 {
				http.NotFound(w, r)
				return
			}
			if s.status == http.StatusOK {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "local", Path: "/"})
			}
			w.WriteHeader(s.status)
			w.Write([]byte(`{"status":"x"}`))
			return
		}
		if c, err := r.Cookie("session"); err != nil || c.Value != "local" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/config":
			w.Write([]byte(`{"port":5001}`))
		case "/api/session/heartbeat":
			w.Write([]byte(`{"status":"ok"}`))
		case "/api/schedule":
			if r.Method == http.MethodDelete {
				s.canceled = append(s.canceled, r.URL.Query().Get("by"))
			}
			w.Write([]byte(`{"active":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *localService) loginCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return NewClient(srv.Listener.Addr().(*net.TCPAddr).Port)
}

func TestClientLocalLogin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		ok     bool
	}{
		{"trusted", http.StatusOK, true},
		{"not trusted", http.StatusForbidden, false},
		{"rate limited", http.StatusTooManyRequests, false},
		{"older service", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &localService{status: tc.status}
			c := clientFor(t, svc.server(t))
			err := c.LocalLogin()
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, errLocalLoginRefused)) {
				t.Fatalf("LocalLogin = %v", err)
			}
			if !svc.csrf {
				t.Error("no X-Requested-With header")
			}
			_, err = c.GetConfig()
			if tc.ok && err != nil {
				t.Errorf("GetConfig after the login: %v", err)
			}
			if !tc.ok && !errors.Is(err, errUnauthorized) {
				t.Errorf("GetConfig after a refusal: %v, want errUnauthorized", err)
			}
		})
	}
	// A service that is not there is a network error, not a refusal.
	closed := httptest.NewServer(http.NotFoundHandler())
	c := clientFor(t, closed)
	closed.Close()
	if err := c.LocalLogin(); err == nil || errors.Is(err, errLocalLoginRefused) {
		t.Errorf("unreachable service: %v", err)
	}
}

// A refusal carries the service's reason (#168), or the HTTP status when
// the reply has none.
func TestLocalLoginRefusalCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusForbidden, `{"status":"error","code":"not_admin","message":"Not trusted for a local login"}`, "not_admin"},
		{http.StatusForbidden, `{"status":"error","code":"other_exe"}`, "other_exe"},
		{http.StatusTooManyRequests, `{"status":"error","code":"rate_limited"}`, "rate_limited"},
		{http.StatusNotFound, "404 page not found\n", "http_404"},
		{http.StatusForbidden, "Forbidden\n", "http_403"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		err := clientFor(t, srv).LocalLogin()
		srv.Close()
		if !errors.Is(err, errLocalLoginRefused) {
			t.Errorf("%s: %v is not a refusal", tc.want, err)
		}
		if got := LocalLoginRefusalCode(err); got != tc.want {
			t.Errorf("code %q, want %q (%v)", got, tc.want, err)
		}
	}
	if LocalLoginRefusalCode(errors.New("connection refused")) != "" {
		t.Error("a network error has a refusal code")
	}
}

func TestToastActionLogsInLocally(t *testing.T) {
	svc := &localService{status: http.StatusOK}
	runToastAction(clientFor(t, svc.server(t)), "stpc://cancel")
	if svc.loginCount() != 1 || len(svc.canceled) != 1 || svc.canceled[0] != "toast" {
		t.Errorf("logins=%d canceled=%v, want one login and a toast cancel", svc.loginCount(), svc.canceled)
	}

	// No secret set: nothing answers 401, so no login is asked for.
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/local-login" {
			t.Error("asked for a session the service does not need")
		}
		w.Write([]byte(`{}`))
	}))
	defer open.Close()
	runToastAction(clientFor(t, open), "stpc://cancel")
}

func TestReadLocalConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), trayConfigFile)
	if got := readLocalConfigFile(path); got.Port != 0 || got.Media.Enabled != nil || got.Debug {
		t.Errorf("missing file: %+v", got)
	}
	data := `{"port": 5101, "smartthings": {"expose_session": true}, "media": {"enabled": false, "now_playing": true}, "debug": true}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readLocalConfigFile(path)
	if got.Port != 5101 || !got.SmartThings.ExposeSession || got.Media.Enabled == nil || *got.Media.Enabled || !got.Media.NowPlaying || !got.Debug {
		t.Errorf("tray.json read as %+v", got)
	}
}
