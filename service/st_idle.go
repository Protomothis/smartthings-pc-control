package service

// Idle-time heartbeat (#77): the store behind it. The tray app in the
// interactive session posts its idle time to POST /api/session/heartbeat
// (service/webui), and the status endpoint reports the newest sample while
// it is fresh. The service itself runs in session 0, where
// GetLastInputInfo describes nothing and WTSINFOEXW.LastInputTime is pinned
// near logon (see st_session.go).
//
// Idle never produces a push event (§3.5): it changes continuously, and the
// driver reads it from the status document it polls anyway.

import (
	"errors"
	"time"
)

// idleHeartbeatTTL is how long one sample stays valid. The tray app sends
// every 30s, so this tolerates two missed posts before the status block
// reports "unknown" — which is what a stopped tray app, a logged-off user
// or a suspended machine look like.
const idleHeartbeatTTL = 90 * time.Second

// noteIdleHeartbeat records one sample from the user session, stamped
// with the receive time (dev.idle).
func noteIdleHeartbeat(seconds int64) { dev.idle.Set(seconds, clock.idle()) }

// lastIdleSeconds returns the idle time of the interactive session; ok is
// false when no heartbeat has arrived, or the newest one is older than
// idleHeartbeatTTL, in which case the status block reports null.
func lastIdleSeconds() (int64, bool) {
	s, _, ok := dev.idle.Fresh(clock.idle(), idleHeartbeatTTL)
	return s, ok
}

// resetIdleHeartbeat forgets the stored sample (tests).
func resetIdleHeartbeat() { dev.idle.Reset() }

// ---- which session the samples describe -----------------------------------

// targetUserSession is getActiveUserSessionID — the same findUserSession
// that runUserAction's commands go through — which also notices the target
// moving to another session (a logon or logoff, or — since the unlocked
// session wins — one session being locked or unlocked next to another).
// The change is logged once, not on every lookup. The idle, audio and
// media samples then describe a session nothing acts on any more and are
// forgotten: the audio store has no TTL, so they would otherwise be
// reported until the next command. The lookup is a few WTS calls, cheap
// enough for every heartbeat and status read; the heartbeat only stores
// samples from this session (heartbeatStore).
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

// observeTargetSession records the target session (0 = nobody logged in)
// and drops the stored samples when it differs from the one seen before.
func observeTargetSession(id uint32) {
	if prev, changed := dev.target.Observe(id); changed {
		resetIdleHeartbeat()
		resetAudioSample()
		resetMediaSample()
		logMsg("user session changed from %d to %d: idle, audio and media samples cleared", prev, id)
	}
}

// noteIgnoredHeartbeat logs an ignored heartbeat once per source and target.
func noteIgnoredHeartbeat(from, target uint32) {
	if dev.target.NoteIgnored(from, target) {
		logMsg("heartbeat from session %d ignored: commands act on session %d", from, target)
	}
}

// resetTargetSession forgets the target session (tests).
func resetTargetSession() { dev.target.Reset() }
