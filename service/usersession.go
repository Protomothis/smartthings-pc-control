package service

// The user session (service/session): which session the commands act on,
// and running this exe there.

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
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

// startTrayIn starts the tray app ("gui --minimized") in session, for the
// launcher (#167). A minimized second launch is silent, so a tray that
// came up on its own meanwhile is not disturbed.
func startTrayIn(sessionID uint32) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}
	return wts.StartIn(sessionID, exe, "gui", "--minimized")
}

// autostartOn reports whether the user with sid keeps the app's login
// autostart: the GUI's Run value in their hive (HKU\<sid>, loaded while
// they are logged on). The GUI writes it on every start unless the user
// turned autostart off, when it removes it. A hive that cannot be read
// counts as on, the default.
func autostartOn(sid string) bool {
	if sid == "" {
		return true
	}
	k, err := registry.OpenKey(registry.USERS, sid+`\`+appid.RunKeyPath, registry.QUERY_VALUE)
	if err != nil {
		// No Run key at all is a user who never had a Run entry.
		return !errors.Is(err, registry.ErrNotExist)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(appid.RunValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false
	}
	return err != nil || strings.TrimSpace(v) != ""
}

// trayGrace is how long after the service start the start-up launch
// waits: the login autostart and a tray restarted by the updater get to
// connect first.
const trayGrace = 15 * time.Second

// launchMissingTrays starts the tray app, once, in the sessions commands
// go to that have none polling trayGrace after the service start (after
// an update the install script has ended every tray, #167). stop ends the
// wait early.
func launchMissingTrays(stop <-chan struct{}) {
	t := time.NewTimer(trayGrace)
	defer t.Stop()
	select {
	case <-t.C:
	case <-stop:
		return
	}
	trayLauncher.LaunchMissing(trayLaunchSessions())
}

// trayLaunchSessions is where the start-up launch looks: the sessions a
// command can target — FindUser's choice (every command) and the console
// (the screen commands), usually the same one — and nothing else. Another
// active RDP login, or a disconnected session, gets no command until it
// becomes that choice, and then the command starts its tray itself; there
// is no point putting a process into every session on the machine.
func trayLaunchSessions() []uint32 {
	var out []uint32
	for _, target := range []sessionTarget{sessionActiveUser, sessionConsole} {
		id, err := findSessionID(target)
		if err != nil || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
