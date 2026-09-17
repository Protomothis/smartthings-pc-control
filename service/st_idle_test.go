package service

// Tests for the idle-time heartbeat (#77): the WebUI endpoint the tray app
// posts to, and how the §4.2 session block reports the stored sample.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// heartbeatDo posts one heartbeat body. session adds a valid session
// cookie, csrf the X-Requested-With header the WebUI API requires on POST.
func heartbeatDo(t *testing.T, body string, session, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/session/heartbeat", strings.NewReader(body))
	if session {
		sessionMu.Lock()
		sessionToken = "heartbeat-token"
		sessionMu.Unlock()
		r.AddCookie(&http.Cookie{Name: "session", Value: "heartbeat-token"})
	}
	if csrf {
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	w := httptest.NewRecorder()
	handleSessionHeartbeat(w, r)
	return w
}

// idleSetup clears the stored sample and restores the clock afterwards.
func idleSetup(t *testing.T) {
	t.Helper()
	resetIdleHeartbeat()
	t.Cleanup(func() {
		resetIdleHeartbeat()
		idleNow = time.Now
		sessionMu.Lock()
		sessionToken = ""
		sessionMu.Unlock()
	})
}

func TestSessionHeartbeatNeedsAuth(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	idleSetup(t)

	if w := heartbeatDo(t, `{"idle_seconds":42}`, false, true); w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d, want 401", w.Code)
	}
	if _, ok := lastIdleSeconds(); ok {
		t.Error("an unauthenticated heartbeat was stored")
	}
	// The CSRF header is required exactly as on the other /api/* POSTs.
	if w := heartbeatDo(t, `{"idle_seconds":42}`, true, false); w.Code != http.StatusForbidden {
		t.Errorf("without X-Requested-With: %d, want 403", w.Code)
	}
	if _, ok := lastIdleSeconds(); ok {
		t.Error("a heartbeat without the CSRF header was stored")
	}
}

func TestSessionHeartbeatStores(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	idleSetup(t)

	if w := heartbeatDo(t, `{"idle_seconds":136}`, true, true); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d (%s)", w.Code, w.Body.String())
	}
	idle, ok := lastIdleSeconds()
	if !ok || idle != 136 {
		t.Errorf("lastIdleSeconds = %d, %v; want 136, true", idle, ok)
	}
	// A newer sample replaces the old one.
	heartbeatDo(t, `{"idle_seconds":7}`, true, true)
	if idle, _ := lastIdleSeconds(); idle != 7 {
		t.Errorf("lastIdleSeconds = %d after a second post, want 7", idle)
	}

	for _, body := range []string{`not json`, `{"idle_seconds":-1}`, `{"idle_seconds":99999999999}`} {
		if w := heartbeatDo(t, body, true, true); w.Code != http.StatusBadRequest {
			t.Errorf("body %q: %d, want 400", body, w.Code)
		}
	}
	if idle, _ := lastIdleSeconds(); idle != 7 {
		t.Errorf("a rejected body changed the stored sample (%d)", idle)
	}
}

// TestSTStatusIdleFromHeartbeat is the whole point of #77: idle_seconds is
// the tray app's number, present only while it is fresh, and never the
// WTS last-input time (which reports the uptime on a console session).
func TestSTStatusIdleFromHeartbeat(t *testing.T) {
	stSetup(t, Config{Port: 5001, SmartThings: SmartThingsConfig{ExposeSession: true}})
	idleSetup(t)

	// No heartbeat yet: null, even though querySessionInfo may well be
	// answering on this machine (locked is unaffected by all this).
	sess := stStatusSession(t)
	if _, has := sess["idle_seconds"]; has {
		t.Errorf("idle_seconds = %v without a heartbeat, want it absent", sess["idle_seconds"])
	}
	if sess["exposed"] != true {
		t.Errorf("session = %v, want exposed", sess)
	}

	if w := heartbeatDo(t, `{"idle_seconds":136}`, true, true); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d", w.Code)
	}
	if sess := stStatusSession(t); sess["idle_seconds"] != float64(136) {
		t.Errorf("idle_seconds = %v, want 136", sess["idle_seconds"])
	}

	// Older than idleHeartbeatTTL: back to null rather than to a number
	// nobody has confirmed since.
	idleNow = func() time.Time { return time.Now().Add(idleHeartbeatTTL + time.Second) }
	if sess := stStatusSession(t); sess["idle_seconds"] != nil {
		t.Errorf("idle_seconds = %v for a stale heartbeat, want null", sess["idle_seconds"])
	}

	// The opt-in still gates the whole block.
	setConfig(Config{Port: 5001, SmartThings: SmartThingsConfig{ExposeSession: false}})
	idleNow = time.Now
	heartbeatDo(t, `{"idle_seconds":136}`, true, true)
	if sess := stStatusSession(t); len(sess) != 1 || sess["exposed"] != false {
		t.Errorf("session = %v with the opt-in off, want only {exposed:false}", sess)
	}
}

// stStatusSession returns the "session" object of GET /st/v1/status.
func stStatusSession(t *testing.T) map[string]any {
	t.Helper()
	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	sess, ok := got["session"].(map[string]any)
	if !ok {
		t.Fatalf("session = %v, want an object", got["session"])
	}
	return sess
}

// TestSessionInfoHasNoWTSIdle guards the regression: the WTS query reports
// the lock state and the user, and nothing about the idle time. sessionInfo
// having no idle field at all is what makes that structural, so this only
// has to check that a successful query leaves the status idle alone.
func TestSessionInfoHasNoWTSIdle(t *testing.T) {
	stSetup(t, Config{Port: 5001, SmartThings: SmartThingsConfig{ExposeSession: true}})
	idleSetup(t)

	info, err := querySessionInfo()
	if err != nil {
		t.Skipf("no interactive session to query here: %v", err)
	}
	out := stSessionInfo(SmartThingsConfig{ExposeSession: true})
	if out.Locked == nil || *out.Locked != info.Locked {
		t.Errorf("locked = %v, want the WTS value %v", out.Locked, info.Locked)
	}
	if out.IdleSeconds != nil {
		t.Errorf("idle_seconds = %d from WTS alone, want null until a heartbeat arrives", *out.IdleSeconds)
	}
}
