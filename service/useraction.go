package service

// The service half of the user-session action channel (#103): the runner
// lives in service/session (Runner) and hands each action to the tray app
// of the target session (session.Broker, #164); this file wires both to
// the service's stores. A reply carrying "audio" also updates the audio
// store, so a command's result is visible in status before the next tray
// heartbeat; a `media info` reply updates the now-playing store the same
// way (#117).

import (
	"context"
	"fmt"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// The runner's names as the service code has always spelt them.
type (
	// UserActionResult is one parsed reply line.
	UserActionResult = session.Result
	// userActionError is an {"ok":false} reply.
	userActionError = session.ActionError
)

var (
	// errUserActionTimeout: the tray app did not answer in time.
	errUserActionTimeout = session.ErrTimeout
	// errUserActionOutput: no readable reply line.
	errUserActionOutput = session.ErrOutput
	// errTrayUnavailable: someone is logged in, but no tray app takes
	// jobs in that session (#164); answered 409 tray_unavailable.
	errTrayUnavailable = session.ErrTrayUnavailable
)

// trays hands the user-session actions to the tray apps (#164); the WebUI
// serves its endpoints (webSrv's Deps.Trays).
var trays = session.NewBroker()

// trayLauncher starts the tray app in a session where none polls (#167):
// before a command (userActions.Ready) and once after the service start
// (launchMissingTrays). The tests replace it with one that has no Start.
var trayLauncher = newTrayLauncher(trays)

// newTrayLauncher is the real launcher over b.
func newTrayLauncher(b *session.Broker) *session.Launcher {
	l := session.NewLauncher(b, startTrayIn, wts.QueryLogon)
	l.Autostart = autostartOn
	l.Logf = logMsg
	return l
}

// userActions runs the user-session actions. The tests replace its Exec
// and Timeout.
var userActions = session.Runner{
	Exec: trayExec,
	// Timeout is 3s end to end (§2), as when it bounded a child process.
	Timeout: 3 * time.Second,
	// Look at the target session first: when it moved, the samples of the
	// old one are dropped now, not after this reply has been stored.
	BeforeRun: func() { targetUserSession() },
	// A missing tray app is started first (#167); its wait is not part of
	// the 3s.
	Ready:   ensureTray,
	OnReply: storeUserActionReply,
}

// trayJobs is the WebUI's view of the tray side (webui.TrayJobs): the
// broker's poll and reply, the launcher's quit notice. It reads the
// variables on each call, so a test that swaps them is followed.
type trayJobs struct{}

func (trayJobs) Poll(ctx context.Context, sessionID, pid uint32) (session.Job, bool) {
	return trays.Poll(ctx, sessionID, pid)
}

func (trayJobs) Reply(sessionID uint32, id string, line []byte) error {
	return trays.Reply(sessionID, id, line)
}

func (trayJobs) Quit(sessionID uint32) { trayLauncher.UserQuit(sessionID) }

// trayExec looks the target session up and hands args to its tray app.
// Nobody logged in stays errNoUserSession (errNoConsoleSession for the
// console), checked before the tray, so a logged-off PC answers as before.
func trayExec(ctx context.Context, target sessionTarget, args []string) ([]byte, error) {
	id, err := findSessionID(target)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return trays.Do(ctx, id, args)
}

// ensureTray makes sure target's session has a tray app polling, starting
// one when it is missing (session.Launcher.Ensure). A failed lookup is
// left to trayExec, which reports it.
func ensureTray(ctx context.Context, target sessionTarget) error {
	id, err := findSessionID(target)
	if err != nil {
		return nil
	}
	return trayLauncher.Ensure(ctx, id)
}

// findSessionID is wts.Find without the token.
func findSessionID(target sessionTarget) (uint32, error) {
	id, tok, err := wts.Find(target)
	if err != nil {
		return 0, err
	}
	tok.Close()
	return id, nil
}

// trayConnected reports whether the tray app of target's session takes
// jobs; false when nobody is logged in there.
func trayConnected(target sessionTarget) bool {
	id, err := findSessionID(target)
	return err == nil && trays.Connected(id)
}

// trayMissing reports whether someone is logged in to target's session
// but its tray app takes no jobs: the one cause of a failing user-session
// command the user can fix on the spot (Telegram says so).
func trayMissing(target sessionTarget) bool {
	id, err := findSessionID(target)
	return err == nil && !trays.Connected(id)
}

// storeUserActionReply folds an ok reply of an active-user run into the
// stores.
func storeUserActionReply(args []string, res UserActionResult) {
	if res.Audio != nil {
		if err := res.Audio.Validate(); err != nil {
			logMsg("user-action %s: ignoring audio in reply: %v", args[0], err)
		} else {
			// Stamped with the completion time: the tray read the device
			// just before it answered.
			recordAudioSample(*res.Audio, clock.audio())
		}
	}
	if np, ok := res.NowPlaying(); ok {
		recordMediaSample(np, clock.audio())
	}
}

// runUserAction runs one useraction (`audio get`, `notify --title …`) in
// the interactive user's session and returns its reply
// (session.Runner.Run).
func runUserAction(ctx context.Context, args ...string) (UserActionResult, error) {
	return userActions.Run(ctx, sessionActiveUser, args...)
}

// runUserActionIn is runUserAction in the given session: sessionActiveUser
// is runUserAction itself; sessionConsole runs in the session on the
// physical monitor (the screen commands) and answers errNoConsoleSession
// when nobody is logged in there.
func runUserActionIn(ctx context.Context, target sessionTarget, args ...string) (UserActionResult, error) {
	return userActions.Run(ctx, target, args...)
}
