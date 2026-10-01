package service

// Tests for the native user-session lookup (#104) over injected WTS calls.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeWTS installs a console session, a session table and the token each
// session answers with (a missing entry is ERROR_NO_TOKEN). Every lock
// state is unknown (a read error) until fakeLocks says otherwise, so the
// order alone decides.
func fakeWTS(t *testing.T, console uint32, sessions []wtsSession, enumErr error, tokens map[uint32]error) *[]uint32 {
	t.Helper()
	var queried []uint32
	savedConsole, savedQuery, savedEnum, savedLocked := wtsActiveConsoleSession, wtsQueryUserToken, wtsEnumerateSessions, wtsSessionLocked
	wtsSessionLocked = func(id uint32) (bool, error) {
		return false, fmt.Errorf("lock state of session %d unknown", id)
	}
	wtsActiveConsoleSession = func() uint32 { return console }
	wtsQueryUserToken = func(id uint32) (windows.Token, error) {
		queried = append(queried, id)
		err, ok := tokens[id]
		if !ok {
			return 0, windows.ERROR_NO_TOKEN
		}
		if err != nil {
			return 0, err
		}
		// A real handle so the caller's Close is harmless.
		return windows.Token(windows.CurrentProcess()), nil
	}
	wtsEnumerateSessions = func() ([]wtsSession, error) { return sessions, enumErr }
	t.Cleanup(func() {
		wtsActiveConsoleSession, wtsQueryUserToken, wtsEnumerateSessions = savedConsole, savedQuery, savedEnum
		wtsSessionLocked = savedLocked
	})
	return &queried
}

// fakeLocks sets each session's lock state after fakeWTS: true is locked,
// false unlocked; a session missing from the map cannot be read.
func fakeLocks(t *testing.T, locked map[uint32]bool) {
	t.Helper()
	wtsSessionLocked = func(id uint32) (bool, error) {
		l, ok := locked[id]
		if !ok {
			return false, windows.ERROR_ACCESS_DENIED
		}
		return l, nil
	}
}

// The session someone is using wins; when nobody is known to be, the
// console does. Seen live (v1.2.0-rc7): a locked console (1) next to an
// RDP login (2) swallowed every toast and mute.
func TestFindUserSessionPrefersUnlocked(t *testing.T) {
	active := uint32(windows.WTSActive)
	both := map[uint32]error{1: nil, 2: nil}
	rdp := []wtsSession{{0, windows.WTSDisconnected}, {1, active}, {2, active}}
	cases := []struct {
		name     string
		sessions []wtsSession
		tokens   map[uint32]error
		locked   map[uint32]bool
		want     uint32
	}{
		{"console locked, rdp unlocked", rdp, both, map[uint32]bool{1: true, 2: false}, 2},
		{"both unlocked", rdp, both, map[uint32]bool{1: false, 2: false}, 1},
		{"both locked", rdp, both, map[uint32]bool{1: true, 2: true}, 1},
		{"console unreadable, rdp unlocked", rdp, both, map[uint32]bool{2: false}, 2},
		{"console locked, rdp unreadable", rdp, both, map[uint32]bool{1: true}, 1},
		{"nothing readable", rdp, both, nil, 1},
		{"only the console, locked", nil, map[uint32]error{1: nil}, map[uint32]bool{1: true}, 1},
		{"only the console, unlocked", nil, map[uint32]error{1: nil}, map[uint32]bool{1: false}, 1},
		{"console logon screen, rdp locked", rdp, map[uint32]error{2: nil}, map[uint32]bool{2: true}, 2},
		{"second rdp unlocked", append(rdp, wtsSession{3, active}),
			map[uint32]error{1: nil, 2: nil, 3: nil}, map[uint32]bool{1: true, 2: true, 3: false}, 3},
	}
	for _, c := range cases {
		fakeWTS(t, 1, c.sessions, nil, c.tokens)
		fakeLocks(t, c.locked)
		id, err := getActiveUserSessionID()
		if err != nil || id != c.want {
			t.Errorf("%s: session %d, %v; want %d", c.name, id, err, c.want)
		}
	}
}

// An unlocked console answers without enumerating the other sessions.
func TestFindUserSessionUnlockedConsoleShortcut(t *testing.T) {
	queried := fakeWTS(t, 1, []wtsSession{{2, windows.WTSActive}}, nil, map[uint32]error{1: nil, 2: nil})
	fakeLocks(t, map[uint32]bool{1: false, 2: false})
	if id, err := getActiveUserSessionID(); err != nil || id != 1 {
		t.Fatalf("session %d, %v; want 1", id, err)
	}
	if got := *queried; len(got) != 1 {
		t.Errorf("queried sessions %v, want only [1]", got)
	}
}

func TestFindUserSession(t *testing.T) {
	active := uint32(windows.WTSActive)
	cases := []struct {
		name     string
		console  uint32
		sessions []wtsSession
		tokens   map[uint32]error
		want     uint32
	}{
		{"console user", 1, nil, map[uint32]error{1: nil}, 1},
		{"console preferred over rdp", 1,
			[]wtsSession{{2, active}}, map[uint32]error{1: nil, 2: nil}, 1},
		{"logon screen, rdp user", 1,
			[]wtsSession{{0, windows.WTSDisconnected}, {1, windows.WTSConnected}, {3, active}},
			map[uint32]error{3: nil}, 3},
		{"console detaching", noConsoleSession,
			[]wtsSession{{4, active}}, map[uint32]error{4: nil}, 4},
	}
	for _, c := range cases {
		fakeWTS(t, c.console, c.sessions, nil, c.tokens)
		id, err := getActiveUserSessionID()
		if err != nil || id != c.want {
			t.Errorf("%s: session %d, %v; want %d", c.name, id, err, c.want)
		}
	}
}

func TestFindUserSessionNobody(t *testing.T) {
	active := uint32(windows.WTSActive)
	queried := fakeWTS(t, 1, []wtsSession{
		{0, active}, // never session 0, whatever it claims
		{1, active}, // the console, already asked
		{2, windows.WTSDisconnected},
		{5, active},
	}, nil, nil)
	_, err := getActiveUserSessionID()
	if !errors.Is(err, errNoUserSession) {
		t.Fatalf("err = %v, want errNoUserSession", err)
	}
	if got := *queried; len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Errorf("queried sessions %v, want [1 5]", got)
	}

	// No console and nothing in the table either.
	fakeWTS(t, noConsoleSession, nil, nil, nil)
	if _, err := getActiveUserSessionID(); !errors.Is(err, errNoUserSession) {
		t.Errorf("empty machine: err = %v, want errNoUserSession", err)
	}
}

// A failure that is not "nobody is logged in" must not read as one: a
// service without SE_TCB_NAME would otherwise report an empty PC forever.
func TestFindUserSessionOtherErrors(t *testing.T) {
	fakeWTS(t, 1, nil, nil, map[uint32]error{1: windows.ERROR_PRIVILEGE_NOT_HELD})
	_, err := getActiveUserSessionID()
	if err == nil || errors.Is(err, errNoUserSession) || !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Errorf("privilege error: %v", err)
	}
	if !strings.Contains(err.Error(), "session 1") {
		t.Errorf("error does not name the session: %v", err)
	}

	fakeWTS(t, noConsoleSession, nil, errors.New("RPC server unavailable"), nil)
	if _, err := getActiveUserSessionID(); err == nil || errors.Is(err, errNoUserSession) {
		t.Errorf("enumeration error: %v", err)
	}

	// A later session with a user still wins over an earlier odd error.
	fakeWTS(t, 1, []wtsSession{{2, windows.WTSActive}}, nil,
		map[uint32]error{1: windows.ERROR_ACCESS_DENIED, 2: nil})
	if id, err := getActiveUserSessionID(); err != nil || id != 2 {
		t.Errorf("fallback after an error: %d, %v", id, err)
	}
}

// Every token findUserSession does not return is closed: the lookup runs
// on every heartbeat and status read, so a leaked handle per call adds up.
func TestFindUserSessionClosesUnusedTokens(t *testing.T) {
	active := uint32(windows.WTSActive)
	cases := []struct {
		name   string
		locked map[uint32]bool
		want   uint32
	}{
		{"rdp unlocked", map[uint32]bool{1: true, 2: true, 3: false}, 3},
		{"all locked", map[uint32]bool{1: true, 2: true, 3: true}, 1},
	}
	for _, c := range cases {
		fakeWTS(t, 1, []wtsSession{{2, active}, {3, active}}, nil, nil)
		fakeLocks(t, c.locked)
		opened := map[uint32]windows.Token{}
		wtsQueryUserToken = func(id uint32) (windows.Token, error) {
			var tok windows.Token
			if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
				t.Fatalf("OpenProcessToken: %v", err)
			}
			opened[id] = tok
			return tok, nil
		}
		id, tok, err := findUserSession()
		if err != nil || id != c.want {
			t.Fatalf("%s: session %d, %v; want %d", c.name, id, err, c.want)
		}
		if windows.Token(tok) != opened[id] {
			t.Errorf("%s: returned token is not session %d's", c.name, id)
		}
		// A closed handle answers ERROR_INVALID_HANDLE. Windows reuses a
		// closed handle's value for the next one opened, so what is checked
		// is that exactly one distinct handle — the returned one — is open.
		open := map[windows.Token]bool{}
		for _, h := range opened {
			if _, err := h.GetTokenUser(); err == nil {
				open[h] = true
			}
		}
		if len(open) != 1 || !open[windows.Token(tok)] {
			t.Errorf("%s: open tokens %v of %v, want only the returned %v", c.name, open, opened, tok)
		}
		tok.Close()
	}
}
