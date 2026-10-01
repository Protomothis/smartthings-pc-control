// Package session finds the interactive user's Windows session and runs
// programs in it. The service runs in session 0, which has no desktop, no
// audio endpoint and no toasts, so everything that must happen where the
// user is — volume, media keys, toasts, presets, the screen commands, the
// tray app — goes through here: a WTS lookup for the session and its
// user's token, then CreateProcessAsUser with that token (exec.Cmd with
// SysProcAttr.Token), and for `user-action` the reply it prints (run.go).
package session

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNoUserSession means nobody is logged in interactively: neither the
// console session nor any other active session has a user token (#103).
// Callers test for it with errors.Is and answer 409 no_user_session.
var ErrNoUserSession = errors.New("no_user_session")

// ErrNoConsoleSession means the session attached to the physical console
// has nobody logged in: the logon screen, or the console between sessions.
// Only the console-pinned lookup (FindConsoleUser) returns it.
var ErrNoConsoleSession = errors.New("no_console_session")

// errorNoToken is ERROR_NO_TOKEN, what WTSQueryUserToken returns for a
// session nobody is logged into (the logon screen, a logging-off session).
const errorNoToken = windows.ERROR_NO_TOKEN

// noConsoleSession is what WTSGetActiveConsoleSessionId returns while the
// console is between sessions (attaching or detaching).
const noConsoleSession = 0xFFFFFFFF

// Row is the part of one WTSEnumerateSessions row the lookups use.
type Row struct {
	ID    uint32
	State uint32 // windows.WTSActive, WTSConnected, WTSDisconnected, ...
}

// WTS is the set of Terminal Services calls the lookups make. System()
// is the real one; a test fills in its own. WTSQueryUserToken needs
// SE_TCB_NAME, which LocalSystem — the service — has.
type WTS struct {
	// ConsoleSession is WTSGetActiveConsoleSessionId.
	ConsoleSession func() uint32
	// QueryUserToken is WTSQueryUserToken: the primary token of the
	// session's user, ERROR_NO_TOKEN when nobody is logged in there.
	QueryUserToken func(session uint32) (windows.Token, error)
	// Sessions is WTSEnumerateSessions.
	Sessions func() ([]Row, error)
	// Locked reads one session's lock state (QueryInfo) — the same
	// reading the status block reports.
	Locked func(session uint32) (bool, error)
}

// System is the real WTS.
func System() WTS {
	return WTS{
		ConsoleSession: windows.WTSGetActiveConsoleSessionId,
		QueryUserToken: func(session uint32) (windows.Token, error) {
			var t windows.Token
			err := windows.WTSQueryUserToken(session, &t)
			return t, err
		},
		Sessions: enumerateSessions,
		Locked: func(session uint32) (bool, error) {
			info, err := QueryInfo(session)
			return info.Locked, err
		},
	}
}

// enumerateSessions lists the sessions on this machine.
func enumerateSessions() ([]Row, error) {
	var info *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &n); err != nil {
		return nil, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	out := make([]Row, 0, n)
	for _, s := range unsafe.Slice(info, n) {
		out = append(out, Row{ID: s.SessionID, State: s.State})
	}
	return out, nil
}

// FindUser returns the interactive user's session and a primary token for
// them; the caller closes the token. A session "has a user" when
// WTSQueryUserToken gives a token for it; the logon screen answers
// ERROR_NO_TOKEN. The candidates are the console session, then every
// other active session (an RDP login) in enumeration order, never session
// 0.
//
// Of those, the session someone is actually using wins: the first one that
// is positively unlocked. Commands, the heartbeat filter and the status all
// follow this one choice, so a mute, a toast or a media key reaches the
// person at the keyboard — next to a locked console that is the RDP
// session, whose audio is what that remote user hears. Only when no
// candidate is known to be unlocked (all locked, or the lock state cannot
// be read — unknown is not unlocked) does the order alone decide, and the
// console comes first: it has the monitor and the speakers. The target can
// therefore move as sessions are locked and unlocked; the service notices
// that (observeTargetSession) and drops the old session's samples.
//
// Nobody logged in is ErrNoUserSession. Any other WTS failure (a missing
// privilege, say) is returned as itself so it is not mistaken for an
// empty machine.
func (w WTS) FindUser() (uint32, syscall.Token, error) {
	var firstErr error
	// The first candidate with a user, kept in case none is unlocked.
	var fallbackID uint32
	var fallbackTok syscall.Token
	haveFallback := false
	// try reports whether session id has a user and is unlocked; a
	// candidate that is merely logged in may become the fallback. Every
	// token not handed to the caller is closed.
	try := func(id uint32) (syscall.Token, bool) {
		t, err := w.QueryUserToken(id)
		if err != nil {
			if !errors.Is(err, errorNoToken) && firstErr == nil {
				firstErr = fmt.Errorf("WTSQueryUserToken(session %d): %w", id, err)
			}
			return 0, false
		}
		tok := syscall.Token(t)
		if locked, err := w.Locked(id); err == nil && !locked {
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

	console := w.ConsoleSession()
	if console != noConsoleSession {
		if tok, ok := try(console); ok {
			return console, tok, nil
		}
	}
	sessions, err := w.Sessions()
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
	return 0, 0, fmt.Errorf("%w: no session has a logged-in user", ErrNoUserSession)
}

// FindConsoleUser returns the session attached to the physical console —
// the one WTSGetActiveConsoleSessionId names, whose desktop is on the real
// monitor — and a primary token for its user; the caller closes the
// token. Unlike FindUser it ignores the lock state and never picks another
// session: an RDP session's display is virtual, so a monitor power
// broadcast there does nothing to the screen on the desk.
//
// A console nobody is logged into (the logon screen) is
// ErrNoConsoleSession, even when an RDP session has a user. Reaching the
// logon screen anyway would mean starting a SYSTEM process on its Winlogon
// desktop — far more privilege than a screen command is worth — and
// Windows already turns the monitor off there on its own timeout.
func (w WTS) FindConsoleUser() (uint32, syscall.Token, error) {
	console := w.ConsoleSession()
	// Session 0 is the services' own and never the console since Vista.
	if console == noConsoleSession || console == 0 {
		return 0, 0, fmt.Errorf("%w: no session is attached to the console", ErrNoConsoleSession)
	}
	t, err := w.QueryUserToken(console)
	if errors.Is(err, errorNoToken) {
		return 0, 0, fmt.Errorf("%w: console session %d has no logged-in user", ErrNoConsoleSession, console)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("WTSQueryUserToken(session %d): %w", console, err)
	}
	return console, syscall.Token(t), nil
}

// Target says which session a user-session command runs in.
type Target int

const (
	// ActiveUser is FindUser's choice: the target every command, the
	// heartbeat filter and the status follow.
	ActiveUser Target = iota
	// Console is FindConsoleUser's: the session on the physical monitor,
	// for the screen commands only.
	Console
)

// Find looks the target session up and returns it with a primary token
// the caller closes.
func (w WTS) Find(t Target) (uint32, syscall.Token, error) {
	if t == Console {
		return w.FindConsoleUser()
	}
	return w.FindUser()
}

// ActiveUserID returns the session ID of the logged-in user (see
// FindUser); ErrNoUserSession when there is none.
func (w WTS) ActiveUserID() (uint32, error) {
	id, tok, err := w.FindUser()
	if err != nil {
		return 0, err
	}
	tok.Close()
	return id, nil
}

// LoggedOn lists every active session other than 0 that has a logged-in
// user (the console and any RDP login), in enumeration order. A session at
// the logon screen has no user.
func (w WTS) LoggedOn() ([]uint32, error) {
	sessions, err := w.Sessions()
	if err != nil {
		return nil, fmt.Errorf("WTSEnumerateSessions: %w", err)
	}
	var out []uint32
	for _, s := range sessions {
		if s.ID == 0 || s.State != windows.WTSActive {
			continue
		}
		tok, err := w.QueryUserToken(s.ID)
		if err != nil {
			continue
		}
		tok.Close()
		out = append(out, s.ID)
	}
	return out, nil
}
