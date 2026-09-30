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

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// idleHeartbeatTTL is how long one sample stays valid. The tray app
	// sends every 30s, so this tolerates two missed posts before the
	// status block reports "unknown" — which is what a stopped tray app,
	// a logged-off user or a suspended machine look like.
	idleHeartbeatTTL = 90 * time.Second
	// idleHeartbeatMaxBody caps the body: one integer and, since #103, an
	// audio block whose device name is at most 128 characters.
	idleHeartbeatMaxBody = 4 << 10
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

// idleHeartbeatRequest is the body of POST /api/session/heartbeat. Both
// parts are optional and stored independently: idle_seconds follows the
// expose_session opt-in on the tray side, while audio (#103) is the default
// playback device's state, which the audio commands need either way.
type idleHeartbeatRequest struct {
	IdleSeconds *int64          `json:"idle_seconds"`
	Audio       *heartbeatAudio `json:"audio"`
}

// heartbeatAudio is the heartbeat's audio block: the sample and, since
// #104, when the tray app took it (RFC3339). The newer-wins guard of the
// audio store compares sample times, so a heartbeat read just before a
// volume command but delivered just after it cannot undo the command's
// result. A tray app without sampled_at gets the receive time.
type heartbeatAudio struct {
	useraction.Audio
	SampledAt string `json:"sampled_at,omitempty"`
}

// heartbeatClockSkew is how far in the future a sampled_at may lie. Tray
// app and service share one clock, so anything later is a bogus value,
// and storing it would make every real sample look older until then.
const heartbeatClockSkew = 5 * time.Second

// sampleTime is when the audio block was taken: sampled_at, clamped to
// the receive time when it is in the future, or the receive time when it
// is absent. ok is false for a value that is not RFC3339.
func (a heartbeatAudio) sampleTime(received time.Time) (time.Time, bool) {
	if a.SampledAt == "" {
		return received, true
	}
	at, err := time.Parse(time.RFC3339Nano, a.SampledAt)
	if err != nil {
		return time.Time{}, false
	}
	if at.After(received.Add(heartbeatClockSkew)) {
		return received, true
	}
	return at, true
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
	if body.IdleSeconds != nil && (*body.IdleSeconds < 0 || *body.IdleSeconds > idleHeartbeatMax) {
		writeAPIError(w, http.StatusBadRequest, "idle_seconds out of range")
		return
	}
	var audioAt time.Time
	if body.Audio != nil {
		if err := body.Audio.Validate(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "audio: "+err.Error())
			return
		}
		at, ok := body.Audio.sampleTime(audioNow())
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "audio: sampled_at is not RFC3339")
			return
		}
		audioAt = at
	}
	// Validate everything before storing anything: a rejected body leaves
	// both samples as they were.
	if body.IdleSeconds != nil {
		noteIdleHeartbeat(*body.IdleSeconds)
	}
	if body.Audio != nil {
		recordAudioSample(body.Audio.Audio, audioAt)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
