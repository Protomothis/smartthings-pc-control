package service

// The service half of the user-session action channel (#103): the runner
// lives in service/session (Runner); this file wires it to the service's
// stores. A reply carrying "audio" also updates the audio store, so a
// command's result is visible in status before the next tray heartbeat; a
// `media info` reply updates the now-playing store the same way (#117).

import (
	"context"
	"os"
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
	// errUserActionTimeout: the child did not answer in time (killed).
	errUserActionTimeout = session.ErrTimeout
	// errUserActionOutput: no readable reply line.
	errUserActionOutput = session.ErrOutput
)

// userActions runs `user-action` in the user session. The tests replace
// its Exec, Exe and Timeout.
var userActions = session.Runner{
	Exec: func(ctx context.Context, target sessionTarget, exe string, args []string) ([]byte, error) {
		return wts.Output(ctx, target, exe, args...)
	},
	Exe: os.Executable,
	// Timeout is 3s end to end (§2); the child is killed when it runs out.
	Timeout: 3 * time.Second,
	// Look at the target session first: when it moved, the samples of the
	// old one are dropped now, not after this reply has been stored.
	BeforeRun: func() { targetUserSession() },
	OnReply:   storeUserActionReply,
}

// storeUserActionReply folds an ok reply of an active-user run into the
// stores.
func storeUserActionReply(args []string, res UserActionResult) {
	if res.Audio != nil {
		if err := res.Audio.Validate(); err != nil {
			logMsg("user-action %s: ignoring audio in reply: %v", args[0], err)
		} else {
			// Stamped with the completion time: the child read the device
			// just before it answered.
			recordAudioSample(*res.Audio, clock.audio())
		}
	}
	if np, ok := res.NowPlaying(); ok {
		recordMediaSample(np, clock.audio())
	}
}

// runUserAction runs `<this exe> user-action <args…>` in the interactive
// user's session and returns its reply (session.Runner.Run).
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
