package service

// Tests for the native user-session lookup (#104) over injected WTS calls.

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeWTS installs a console session, a session table and the token each
// session answers with (a missing entry is ERROR_NO_TOKEN).
func fakeWTS(t *testing.T, console uint32, sessions []wtsSession, enumErr error, tokens map[uint32]error) *[]uint32 {
	t.Helper()
	var queried []uint32
	savedConsole, savedQuery, savedEnum := wtsActiveConsoleSession, wtsQueryUserToken, wtsEnumerateSessions
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
	})
	return &queried
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
