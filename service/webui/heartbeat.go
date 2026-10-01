package webui

// Idle-time heartbeat (#77). The service runs in session 0, where
// GetLastInputInfo describes nothing and WTSINFOEXW.LastInputTime is pinned
// near logon, so the only process that can measure the user's idle time is
// the tray app in the interactive session. It posts a sample every 30s to
// POST /api/session/heartbeat, and the status endpoint reports the newest
// one while it is fresh.
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
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// heartbeatMaxBody caps the body: one integer, since #103 an audio
	// block whose device name is at most 128 characters, and since #117 a
	// media block of four short texts (at most ~2.6 KB of UTF-8).
	heartbeatMaxBody = 8 << 10
	// idleMax is a sanity bound (a year) on the reported value.
	idleMax = int64(365 * 24 * 60 * 60)
	// heartbeatClockSkew is how far in the future a sampled_at may lie.
	// Tray app and service share one clock, so anything later is a bogus
	// value, and storing it would make every real sample look older until
	// then.
	heartbeatClockSkew = 5 * time.Second
)

// heartbeatRequest is the body of POST /api/session/heartbeat. Both parts
// are optional and stored independently: idle_seconds follows the
// expose_session opt-in on the tray side, while audio (#103) is the default
// playback device's state, which the audio commands need either way.
type heartbeatRequest struct {
	IdleSeconds *int64          `json:"idle_seconds"`
	Audio       *heartbeatAudio `json:"audio"`
	// Media is the system media session (#117). The tray app sends it
	// every 3s when it changes, as a body with only this block.
	Media *heartbeatMedia `json:"media"`
	// SessionID is the Windows session the tray app runs in. A heartbeat
	// from any session but the one the commands act on is ignored (see
	// serveHeartbeat); an older tray app leaves it out and is believed as
	// before.
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

// serveHeartbeat serves POST /api/session/heartbeat. The sample is stored
// whatever smartthings.expose_session says — the flag decides what
// /st/v1/status publishes, and the tray app already stops posting when it
// is off, so a stale flag never turns into a stale reading.
func (s *Server) serveHeartbeat(w http.ResponseWriter, r *http.Request) {
	hb := s.d.Heartbeat
	var body heartbeatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, heartbeatMaxBody)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.IdleSeconds != nil && (*body.IdleSeconds < 0 || *body.IdleSeconds > idleMax) {
		writeAPIError(w, http.StatusBadRequest, "idle_seconds out of range")
		return
	}
	var audioAt time.Time
	if body.Audio != nil {
		if err := body.Audio.Validate(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "audio: "+err.Error())
			return
		}
		at, ok := body.Audio.sampleTime(hb.Now())
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
		at, ok := body.Media.sampleTime(hb.Now())
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "media: sampled_at is not RFC3339")
			return
		}
		mediaAt = at
	}
	if body.SessionID != nil {
		if target, err := hb.Target(); err == nil && target != *body.SessionID {
			// The commands run in target (the unlocked session, else the
			// console), so this session's idle time, volume and media say
			// nothing about what they did: the tray app of a session
			// nobody acts on would undo every mute within one heartbeat.
			// 200, so the tray app neither retries nor logs in again; the
			// reason tells it why nothing was stored.
			hb.Ignored(*body.SessionID, target)
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "other_session"})
			return
		}
	}
	// Validate everything before storing anything: a rejected body leaves
	// every sample as it was.
	if body.IdleSeconds != nil {
		hb.Idle(*body.IdleSeconds)
	}
	if body.Audio != nil {
		hb.Audio(body.Audio.Audio, audioAt)
	}
	if body.Media != nil {
		hb.Media(body.Media.NowPlaying, mediaAt)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
