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
	"errors"
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
	// idleHeartbeatMaxBody caps the body: one integer, since #103 an audio
	// block whose device name is at most 128 characters, and since #117 a
	// media block of four short texts (at most ~2.6 KB of UTF-8).
	idleHeartbeatMaxBody = 8 << 10
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
	// Media is the system media session (#117). The tray app sends it
	// every 3s when it changes, as a body with only this block.
	Media *heartbeatMedia `json:"media"`
	// SessionID is the Windows session the tray app runs in. A heartbeat
	// from any session but the one the commands act on is ignored (see
	// handleSessionHeartbeat); an older tray app leaves it out and is
	// believed as before.
	SessionID *uint32 `json:"session_id"`
}

// heartbeatMedia is the heartbeat's media block, with the same sampled_at
// rule as the audio block. The tray app leaves the track out while the
// media.now_playing opt-in is off; the store drops it anyway.
type heartbeatMedia struct {
	useraction.NowPlaying
	SampledAt string `json:"sampled_at,omitempty"`
}

func (m heartbeatMedia) sampleTime(received time.Time) (time.Time, bool) {
	return heartbeatSampleTime(m.SampledAt, received)
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
	return heartbeatSampleTime(a.SampledAt, received)
}

// heartbeatSampleTime is sampleTime for any block's sampled_at.
func heartbeatSampleTime(sampledAt string, received time.Time) (time.Time, bool) {
	if sampledAt == "" {
		return received, true
	}
	at, err := time.Parse(time.RFC3339Nano, sampledAt)
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
	var mediaAt time.Time
	if body.Media != nil {
		if err := body.Media.Validate(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "media: "+err.Error())
			return
		}
		at, ok := body.Media.sampleTime(audioNow())
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "media: sampled_at is not RFC3339")
			return
		}
		mediaAt = at
	}
	if body.SessionID != nil {
		if target, err := heartbeatTargetSession(); err == nil && target != *body.SessionID {
			// The commands run in target (findUserSession prefers the
			// console), so this session's idle time, volume and media say
			// nothing about what they did: a tray app in an RDP session
			// next to a locked console would undo every mute within one
			// heartbeat. 200, so the tray app neither retries nor logs in
			// again; the reason tells it why nothing was stored.
			noteIgnoredHeartbeat(*body.SessionID, target)
			writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "other_session"})
			return
		}
	}
	// Validate everything before storing anything: a rejected body leaves
	// every sample as it was.
	if body.IdleSeconds != nil {
		noteIdleHeartbeat(*body.IdleSeconds)
	}
	if body.Audio != nil {
		recordAudioSample(body.Audio.Audio, audioAt)
	}
	if body.Media != nil {
		recordMediaSample(body.Media.NowPlaying, mediaAt)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- which session the samples describe -----------------------------------

// heartbeatTargetSession is the session the commands act on, the one whose
// heartbeats are stored. A var so the tests need no real sessions.
var heartbeatTargetSession = targetUserSession

var (
	targetMu sync.Mutex
	// targetLast is the target session last seen (0 = nobody logged in);
	// targetKnown is false until the first lookup.
	targetLast  uint32
	targetKnown bool
	// ignoredFrom and ignoredFor are the source and target of the last
	// ignored heartbeat logged: a foreign tray app costs one log line, not
	// one per post.
	ignoredFrom, ignoredFor uint32
)

// targetUserSession is getActiveUserSessionID — the same findUserSession
// that runUserAction's commands go through — which also notices the target
// moving to another session (the console user logging on next to an RDP
// login, say). The idle, audio and media samples then describe a session
// nothing acts on any more and are forgotten: the audio store has no TTL,
// so they would otherwise be reported until the next command. The lookup
// is a few WTS calls, cheap enough for every heartbeat and status read.
func targetUserSession() (uint32, error) {
	id, err := getActiveUserSessionID()
	switch {
	case err == nil:
		observeTargetSession(id)
	case errors.Is(err, errNoUserSession):
		observeTargetSession(0)
	}
	return id, err
}

// observeTargetSession records the target session and drops the stored
// samples when it differs from the one seen before.
func observeTargetSession(id uint32) {
	targetMu.Lock()
	prev, changed := targetLast, targetKnown && targetLast != id
	targetLast, targetKnown = id, true
	targetMu.Unlock()
	if changed {
		resetIdleHeartbeat()
		resetAudioSample()
		resetMediaSample()
		logMsg("user session changed from %d to %d: idle, audio and media samples cleared", prev, id)
	}
}

// noteIgnoredHeartbeat logs an ignored heartbeat once per source and target.
func noteIgnoredHeartbeat(from, target uint32) {
	targetMu.Lock()
	first := ignoredFrom != from || ignoredFor != target
	ignoredFrom, ignoredFor = from, target
	targetMu.Unlock()
	if first {
		logMsg("heartbeat from session %d ignored: commands act on session %d", from, target)
	}
}

// resetTargetSession forgets the target session (tests).
func resetTargetSession() {
	targetMu.Lock()
	targetLast, targetKnown, ignoredFrom, ignoredFor = 0, false, 0, 0
	targetMu.Unlock()
}
