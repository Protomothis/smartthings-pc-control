package service

// Opt-in session information for GET /st/v1/status (edge-driver doc §3.2,
// "smartthings.expose_session"): the lock state and the user name
// (session.QueryInfo), and the lock watcher that pushes their changes. The
// idle time arrives by heartbeat instead (st_idle.go, #77).
//
// Everything here is best effort: when the query fails (nobody logged in, an
// older Windows, a hardened policy) the caller reports null rather than
// guessing, and the status endpoint still answers.

import (
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// querySessionInfo describes the interactive user session, or fails when
// there is none to describe. It is the target session of the commands
// (targetUserSession), so the lock watcher's 5s poll also notices the
// target moving when a session is locked or unlocked, and the session it
// reports is the one the commands act on.
func querySessionInfo() (sessionInfo, error) {
	sessionID, err := targetUserSession()
	if err != nil {
		return sessionInfo{}, err
	}
	return session.QueryInfo(sessionID)
}

// ---- lock/unlock watcher (§3.5) --------------------------------------------

// sessionPollInterval is how often the lock state is sampled. A service in
// session 0 gets no WM_WTSSESSION_CHANGE and SERVICE_CONTROL_SESSIONCHANGE
// reports console connect/disconnect, not the lock screen — so there is no
// event to subscribe to and the state has to be polled. One
// WTSQuerySessionInformation call every 5s is cheap enough (§3.5 asks for
// "immediate", and 5s is the resolution the driver gets), and the poll only
// runs while smartthings.expose_session is on.
const sessionPollInterval = 5 * time.Second

// watchSessionLock emits session.locked / session.unlocked whenever the
// interactive session's lock state changes. The events are device state,
// not notifications: they go to the bus taps (the SmartThings push sink)
// and never to Telegram.
//
// The first successful sample only establishes the baseline; turning the
// option off and on again re-establishes it, so enabling the option never
// invents a transition. Failures (nobody logged in, WTS refusing) reset the
// baseline too, because what happened while the service could not look is
// unknown.
func watchSessionLock(stop <-chan struct{}) {
	t := time.NewTicker(sessionPollInterval)
	defer t.Stop()
	known := false
	var locked bool
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if !getConfig().SmartThings.ExposeSession {
			known = false
			continue
		}
		info, err := querySessionInfo()
		if err != nil {
			known = false
			continue
		}
		if known && info.Locked == locked {
			continue
		}
		if known {
			emitSessionLock(info)
		}
		known, locked = true, info.Locked
	}
}

// emitSessionLock reports one lock-state transition.
func emitSessionLock(info sessionInfo) {
	kind := "unlocked"
	if info.Locked {
		kind = "locked"
	}
	// No idle_seconds here: idle is heartbeat state, never an event (#77).
	fields := map[string]string{}
	// The user name follows the same opt-in as the status block (§3.2).
	if getConfig().SmartThings.ExposeSessionUser && info.User != "" {
		fields["user"] = info.User
	}
	logMsg("Session %s", kind)
	emitDevice("session", kind, fields)
}
