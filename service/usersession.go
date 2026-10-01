package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// modWtsapi32 also serves st_session.go's WTSQuerySessionInformationW.
var modWtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")

// errNoUserSession means nobody is logged in interactively: neither the
// console session nor any other active session has a user token (#103).
// Callers test for it with errors.Is and answer 409 no_user_session.
var errNoUserSession = errors.New("no_user_session")

// errorNoToken is ERROR_NO_TOKEN, what WTSQueryUserToken returns for a
// session nobody is logged into (the logon screen, a logging-off session).
const errorNoToken = windows.ERROR_NO_TOKEN

// noConsoleSession is what WTSGetActiveConsoleSessionId returns while the
// console is between sessions (attaching or detaching).
const noConsoleSession = 0xFFFFFFFF

// wtsSession is the part of one WTSEnumerateSessions row the lookup uses.
type wtsSession struct {
	ID    uint32
	State uint32 // windows.WTSActive, WTSConnected, WTSDisconnected, ...
}

// The WTS calls behind findUserSession, replaced by the tests. They need
// SE_TCB_NAME for WTSQueryUserToken, which LocalSystem — the service — has.
var (
	wtsActiveConsoleSession = windows.WTSGetActiveConsoleSessionId
	wtsQueryUserToken       = func(session uint32) (windows.Token, error) {
		var t windows.Token
		err := windows.WTSQueryUserToken(session, &t)
		return t, err
	}
	wtsEnumerateSessions = enumerateWTSSessions
	// wtsSessionLocked reads one session's lock state through st_session.go's
	// WTSSessionInfoEx query — the same reading the status block reports.
	wtsSessionLocked = func(session uint32) (bool, error) {
		info, err := querySessionInfoID(session)
		return info.Locked, err
	}
)

// enumerateWTSSessions lists the sessions on this machine.
func enumerateWTSSessions() ([]wtsSession, error) {
	var info *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &n); err != nil {
		return nil, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	out := make([]wtsSession, 0, n)
	for _, s := range unsafe.Slice(info, n) {
		out = append(out, wtsSession{ID: s.SessionID, State: s.State})
	}
	return out, nil
}

// findUserSession returns the interactive user's session and a primary
// token for them; the caller closes the token. A session "has a user" when
// WTSQueryUserToken gives a token for it; the logon screen answers
// ERROR_NO_TOKEN. The candidates are the console session, then every other
// active session (an RDP login) in enumeration order, never session 0.
//
// Of those, the session someone is actually using wins: the first one that
// is positively unlocked. Commands, the heartbeat filter and the status all
// follow this one choice, so a mute, a toast or a media key reaches the
// person at the keyboard — next to a locked console that is the RDP
// session, whose audio is what that remote user hears. Only when no
// candidate is known to be unlocked (all locked, or the lock state cannot
// be read — unknown is not unlocked) does the order alone decide, and the
// console comes first: it has the monitor and the speakers. The target can
// therefore move as sessions are locked and unlocked; observeTargetSession
// (st_idle.go) notices that and drops the old session's samples.
//
// Nobody logged in is errNoUserSession. Any other WTS failure (a missing
// privilege, say) is returned as itself so it is not mistaken for an
// empty machine.
//
// This replaced a PowerShell `Get-Process explorer` lookup (#104): that
// cost most of a second per call, a third of runUserAction's 3s budget,
// and also missed a user whose shell had crashed.
func findUserSession() (uint32, syscall.Token, error) {
	var firstErr error
	// The first candidate with a user, kept in case none is unlocked.
	var fallbackID uint32
	var fallbackTok syscall.Token
	haveFallback := false
	// try reports whether session id has a user and is unlocked; a
	// candidate that is merely logged in may become the fallback. Every
	// token not handed to the caller is closed.
	try := func(id uint32) (syscall.Token, bool) {
		t, err := wtsQueryUserToken(id)
		if err != nil {
			if !errors.Is(err, errorNoToken) && firstErr == nil {
				firstErr = fmt.Errorf("WTSQueryUserToken(session %d): %w", id, err)
			}
			return 0, false
		}
		tok := syscall.Token(t)
		if locked, err := wtsSessionLocked(id); err == nil && !locked {
			if haveFallback {
				fallbackTok.Close()
			}
			return tok, true
		}
		if haveFallback {
			tok.Close()
		} else {
			fallbackID, fallbackTok, haveFallback = id, tok, true
		}
		return 0, false
	}

	console := wtsActiveConsoleSession()
	if console != noConsoleSession {
		if tok, ok := try(console); ok {
			return console, tok, nil
		}
	}
	sessions, err := wtsEnumerateSessions()
	if err != nil && firstErr == nil {
		firstErr = fmt.Errorf("WTSEnumerateSessions: %w", err)
	}
	for _, s := range sessions {
		// Session 0 is the services' own; it never has an interactive user.
		if s.State != windows.WTSActive || s.ID == 0 || s.ID == console {
			continue
		}
		if tok, ok := try(s.ID); ok {
			return s.ID, tok, nil
		}
	}
	if haveFallback {
		return fallbackID, fallbackTok, nil
	}
	if firstErr != nil {
		return 0, 0, firstErr
	}
	return 0, 0, fmt.Errorf("%w: no session has a logged-in user", errNoUserSession)
}

// userSessionWaitDelay bounds how long a waited-for user-session command
// may keep its output pipes open after it exited or was killed — a
// grandchild that inherited them must not keep CombinedOutput blocked.
const userSessionWaitDelay = 500 * time.Millisecond

// getActiveUserSessionID returns the session ID of the logged-in user (see
// findUserSession); errNoUserSession when there is none.
func getActiveUserSessionID() (uint32, error) {
	id, tok, err := findUserSession()
	if err != nil {
		return 0, err
	}
	tok.Close()
	return id, nil
}

// userSessionCommand builds an exec.Cmd that runs under the active user's
// token, i.e. inside their interactive desktop session. The caller must
// Close the returned token once the process has been started. The command
// is killed when ctx is done (exec.CommandContext), and the error wraps
// errNoUserSession when nobody is logged in.
func userSessionCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, syscall.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	_, token, err := findUserSession()
	if err != nil {
		return nil, 0, fmt.Errorf("get session: %w", err)
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Token: token,
	}
	return cmd, token, nil
}

// outputInUserSession executes a command in the active user's desktop
// session (Session 0 isolation keeps the service off the interactive
// desktop), waits for it and returns the combined output. The child is
// killed when ctx is done. The output is returned even when the command
// fails, since a failing child may still have said why on stdout —
// user-action does exactly that.
func outputInUserSession(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, token, err := userSessionCommand(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	defer token.Close()
	cmd.WaitDelay = userSessionWaitDelay

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("exec error: %w - output: %s", err, string(output))
	}
	return output, nil
}

// startInUserSession launches a command in the active user's desktop
// session without waiting for it. Used for long-lived processes such as the
// tray app, where waiting would pin a goroutine for the app's lifetime.
func startInUserSession(name string, args ...string) error {
	cmd, token, err := userSessionCommand(context.Background(), name, args...)
	if err != nil {
		return err
	}
	defer token.Close()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start error: %v", err)
	}
	// Detach: the child outlives this call and nobody will Wait on it.
	return cmd.Process.Release()
}

// launchTrayApp starts this executable as the tray app ("gui --minimized")
// in the active user's session so the grace-period toast can be shown. The
// GUI's single-instance guard makes this a silent no-op when the tray app
// is already running.
func launchTrayApp() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}
	return startInUserSession(exe, "gui", "--minimized")
}
