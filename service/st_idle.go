package service

// Idle-time heartbeat (#77). The service runs in session 0, where
// GetLastInputInfo describes nothing and WTSINFOEXW.LastInputTime is pinned
// near logon (see st_session.go), so the only process that can measure the
// user's idle time is the tray app in the interactive session. It posts a
// sample every 30s to POST /api/session/heartbeat, and the status endpoint
// reports the newest one while it is fresh.
//
// The endpoint lives on the WebUI server behind the same session cookie and
// CSRF header as every other /api/* route the tray app calls: the idle time
// of the logged-in user is not something an unauthenticated caller on the
// LAN should be able to write (or read back through /st/v1/status).
//
// Idle never produces a push event (§3.5): it changes continuously, and the
// driver reads it from the status document it polls anyway.

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	// idleHeartbeatTTL is how long one sample stays valid. The tray app
	// sends every 30s, so this tolerates two missed posts before the
	// status block reports "unknown" — which is what a stopped tray app,
	// a logged-off user or a suspended machine look like.
	idleHeartbeatTTL = 90 * time.Second
	// idleHeartbeatMaxBody caps the body; it is one integer.
	idleHeartbeatMaxBody = 1 << 10
	// idleHeartbeatMax is a sanity bound (a year) on the reported value.
	idleHeartbeatMax = int64(365 * 24 * 60 * 60)
)

// idleHeartbeat is the last sample the tray app posted.
type idleHeartbeat struct {
	Seconds int64
	At      time.Time
}

var (
	idleBeat   idleHeartbeat
	idleBeatMu sync.Mutex
	// idleNow is time.Now, replaced by the staleness tests.
	idleNow = time.Now
)

// noteIdleHeartbeat records one sample from the user session.
func noteIdleHeartbeat(seconds int64) {
	idleBeatMu.Lock()
	idleBeat = idleHeartbeat{Seconds: seconds, At: idleNow()}
	idleBeatMu.Unlock()
}

// lastIdleSeconds returns the idle time of the interactive session; ok is
// false when no heartbeat has arrived, or the newest one is older than
// idleHeartbeatTTL, in which case the status block reports null.
func lastIdleSeconds() (int64, bool) {
	idleBeatMu.Lock()
	defer idleBeatMu.Unlock()
	if idleBeat.At.IsZero() || idleNow().Sub(idleBeat.At) > idleHeartbeatTTL {
		return 0, false
	}
	return idleBeat.Seconds, true
}

// resetIdleHeartbeat forgets the stored sample (tests).
func resetIdleHeartbeat() {
	idleBeatMu.Lock()
	idleBeat = idleHeartbeat{}
	idleBeatMu.Unlock()
}

// idleHeartbeatRequest is the body of POST /api/session/heartbeat.
type idleHeartbeatRequest struct {
	IdleSeconds int64 `json:"idle_seconds"`
}

// handleSessionHeartbeat serves POST /api/session/heartbeat. The sample is
// stored whatever smartthings.expose_session says — the flag decides what
// /st/v1/status publishes, and the tray app already stops posting when it
// is off, so a stale flag never turns into a stale reading.
func handleSessionHeartbeat(w http.ResponseWriter, r *http.Request) {
	liveCfg := getConfig()
	if !checkAuth(r, liveCfg.Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var body idleHeartbeatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, idleHeartbeatMaxBody)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.IdleSeconds < 0 || body.IdleSeconds > idleHeartbeatMax {
		writeAPIError(w, http.StatusBadRequest, "idle_seconds out of range")
		return
	}
	noteIdleHeartbeat(body.IdleSeconds)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
