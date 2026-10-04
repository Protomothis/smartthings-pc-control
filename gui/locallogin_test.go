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
	"time"
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

func TestLocalLoginGate(t *testing.T) {
	var g localLoginGate
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	calls := 0
	refused := func() error { calls++; return errLocalLoginRefused }
	if g.try(now, refused) || calls != 1 {
		t.Fatalf("first refusal: calls=%d", calls)
	}
	if g.try(now.Add(time.Minute), refused) || calls != 1 {
		t.Errorf("asked again %d times within the backoff", calls-1)
	}
	if g.try(now.Add(localLoginRetryAfter), refused) || calls != 2 {
		t.Errorf("not asked again after the backoff: calls=%d", calls)
	}
	// A network error (service restarting) does not hold the next try back.
	var g2 localLoginGate
	down := func() error { calls++; return errors.New("connection refused") }
	g2.try(now, down)
	if !g2.try(now, func() error { return nil }) {
		t.Error("a network error blocked the next attempt")
	}
}

// TestHeartbeatLogsInLocally: a heartbeat answered 401 asks for the local
// session and is delivered on the retry; refused, it gives up quietly and
// the next posts do not keep asking.
func TestHeartbeatLogsInLocally(t *testing.T) {
	savedID := heartbeatSessionID
	heartbeatSessionID = func() uint32 { return 1 }
	t.Cleanup(func() { heartbeatSessionID = savedID })
	idle := int64(5)

	svc := &localService{status: http.StatusOK}
	u := &ui{client: clientFor(t, svc.server(t))}
	u.postHeartbeat(Heartbeat{IdleSeconds: &idle})
	if svc.loginCount() != 1 {
		t.Fatalf("local logins = %d, want 1", svc.loginCount())
	}
	u.postHeartbeat(Heartbeat{IdleSeconds: &idle})
	if svc.loginCount() != 1 {
		t.Errorf("logged in again with a valid session: %d", svc.loginCount())
	}

	refusing := &localService{status: http.StatusForbidden}
	u2 := &ui{client: clientFor(t, refusing.server(t))}
	for range 5 {
		u2.postHeartbeat(Heartbeat{IdleSeconds: &idle})
	}
	if refusing.loginCount() != 1 {
		t.Errorf("refused local login asked %d times, want once per backoff", refusing.loginCount())
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

// lockedAPI is fakeAPI behind a secret: GetConfig answers 401 until a
// local login succeeded.
type lockedAPI struct {
	*fakeAPI
	local    error
	loggedIn bool
	logins   int
}

func (l *lockedAPI) GetConfig() (Config, error) {
	if !l.loggedIn {
		return Config{}, errUnauthorized
	}
	return l.fakeAPI.GetConfig()
}

func (l *lockedAPI) LocalLogin() error {
	l.logins++
	l.loggedIn = l.local == nil
	return l.local
}

// TestInitialLoadLocalLogin is the window's path: a 401 tries the local
// login first and connects without a dialog; refused, the login dialog
// opens as before (and a reconnect does not ask the service again within
// the backoff).
func TestInitialLoadLocalLogin(t *testing.T) {
	trusted := &lockedAPI{fakeAPI: &fakeAPI{cfg: storedConfig()}}
	u := newTestUI(t, LangEn, trusted)
	u.initialLoad()
	if !u.connected.Load() || trusted.logins != 1 {
		t.Fatalf("connected=%v logins=%d, want a local login and a connection", u.connected.Load(), trusted.logins)
	}
	if u.loginState() != loginIdle {
		t.Errorf("login state %d, want idle (no dialog)", u.loginState())
	}

	refused := &lockedAPI{fakeAPI: &fakeAPI{cfg: storedConfig()}, local: errLocalLoginRefused}
	u2 := newTestUI(t, LangEn, refused)
	u2.initialLoad()
	if u2.connected.Load() {
		t.Fatal("connected without a session")
	}
	if u2.loginState() != loginPrompting {
		t.Errorf("login state %d, want the dialog (prompting)", u2.loginState())
	}
	u2.setLoginState(loginDeferred) // the user cancelled the dialog
	u2.initialLoad()                // connTick, 5 s later
	if refused.logins != 1 {
		t.Errorf("local logins = %d, want 1 within the backoff", refused.logins)
	}
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
