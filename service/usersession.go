package service

// The user session (service/session): which session the commands act on,
// and running this exe there.

import (
	"fmt"
	"os"
	"syscall"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// wts is the Terminal Services lookups every user-session command goes
// through; the tests fill in their own (fakeWTS).
var wts = session.System()

// The session package's names as the service code has always spelt them.
type (
	sessionTarget = session.Target
	wtsSession    = session.Row
	sessionInfo   = session.Info
)

const (
	// sessionActiveUser is findUserSession's choice: the target every
	// command, the heartbeat filter and the status follow.
	sessionActiveUser = session.ActiveUser
	// sessionConsole is findConsoleUserSession's: the session on the
	// physical monitor, for the screen commands only.
	sessionConsole = session.Console
)

var (
	// errNoUserSession means nobody is logged in interactively (#103);
	// callers answer 409 no_user_session.
	errNoUserSession = session.ErrNoUserSession
	// errNoConsoleSession means nobody is logged in at the console.
	errNoConsoleSession = session.ErrNoConsoleSession
)

// findUserSession is wts.FindUser: the unlocked session, else the console.
func findUserSession() (uint32, syscall.Token, error) { return wts.FindUser() }

// findConsoleUserSession is wts.FindConsoleUser: the session on the
// physical monitor.
func findConsoleUserSession() (uint32, syscall.Token, error) { return wts.FindConsoleUser() }

// getActiveUserSessionID returns the session ID of the logged-in user (see
// findUserSession); errNoUserSession when there is none.
func getActiveUserSessionID() (uint32, error) { return wts.ActiveUserID() }

// launchTrayApp starts this executable as the tray app ("gui --minimized")
// in the active user's session so the grace-period toast can be shown. The
// GUI's single-instance guard makes this a silent no-op when the tray app
// is already running.
func launchTrayApp() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}
	return wts.Start(exe, "gui", "--minimized")
}
