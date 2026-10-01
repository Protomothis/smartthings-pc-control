package service

// Tests for the native user-session lookup (#104) over injected WTS calls.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// noConsoleSession is what WTSGetActiveConsoleSessionId returns while the
// console is between sessions.
const noConsoleSession = 0xFFFFFFFF

// fakeWTS installs a console session, a session table and the token each
// session answers with (a missing entry is ERROR_NO_TOKEN). Every lock
// state is unknown (a read error) until fakeLocks says otherwise, so the
// order alone decides.
func fakeWTS(t *testing.T, console uint32, sessions []wtsSession, enumErr error, tokens map[uint32]error) *[]uint32 {
	t.Helper()
	var queried []uint32
	saved := wts
	wts.Locked = func(id uint32) (bool, error) {
		return false, fmt.Errorf("lock state of session %d unknown", id)
	}
	wts.ConsoleSession = func() uint32 { return console }
	wts.QueryUserToken = func(id uint32) (windows.Token, error) {
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
	wts.Sessions = func() ([]wtsSession, error) { return sessions, enumErr }
	t.Cleanup(func() { wts = saved })
	return &queried
}

// fakeLocks sets each session's lock state after fakeWTS: true is locked,
// false unlocked; a session missing from the map cannot be read.
func fakeLocks(t *testing.T, locked map[uint32]bool) {
	t.Helper()
	wts.Locked = func(id uint32) (bool, error) {
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
	rdp := []wtsSession{{ID: 0, State: windows.WTSDisconnected}, {ID: 1, State: active}, {ID: 2, State: active}}
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
		{"second rdp unlocked", append(rdp, wtsSession{ID: 3, State: active}),
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
	queried := fakeWTS(t, 1, []wtsSession{{ID: 2, State: windows.WTSActive}}, nil, map[uint32]error{1: nil, 2: nil})
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
			[]wtsSession{{ID: 2, State: active}}, map[uint32]error{1: nil, 2: nil}, 1},
		{"logon screen, rdp user", 1,
			[]wtsSession{{ID: 0, State: windows.WTSDisconnected}, {ID: 1, State: windows.WTSConnected}, {ID: 3, State: active}},
			map[uint32]error{3: nil}, 3},
		{"console detaching", noConsoleSession,
			[]wtsSession{{ID: 4, State: active}}, map[uint32]error{4: nil}, 4},
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
		{ID: 0, State: active}, // never session 0, whatever it claims
		{ID: 1, State: active}, // the console, already asked
		{ID: 2, State: windows.WTSDisconnected},
		{ID: 5, State: active},
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
	fakeWTS(t, 1, []wtsSession{{ID: 2, State: windows.WTSActive}}, nil,
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
		fakeWTS(t, 1, []wtsSession{{ID: 2, State: active}, {ID: 3, State: active}}, nil, nil)
		fakeLocks(t, c.locked)
		opened := map[uint32]windows.Token{}
		wts.QueryUserToken = func(id uint32) (windows.Token, error) {
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

// The screen commands' lookup: the session on the physical monitor, locked
// or not, and never an RDP session instead.
func TestFindConsoleUserSession(t *testing.T) {
	active := uint32(windows.WTSActive)
	rdp := []wtsSession{{ID: 0, State: windows.WTSDisconnected}, {ID: 1, State: active}, {ID: 2, State: active}}
	cases := []struct {
		name    string
		console uint32
		tokens  map[uint32]error
		locked  map[uint32]bool
		want    uint32
		wantErr error
	}{
		{"console locked, rdp unlocked", 1, map[uint32]error{1: nil, 2: nil}, map[uint32]bool{1: true, 2: false}, 1, nil},
		{"console unlocked", 1, map[uint32]error{1: nil}, map[uint32]bool{1: false}, 1, nil},
		{"console lock unreadable", 1, map[uint32]error{1: nil}, nil, 1, nil},
		{"console logon screen, rdp unlocked", 1, map[uint32]error{2: nil}, map[uint32]bool{2: false}, 0, errNoConsoleSession},
		{"nobody", 1, nil, nil, 0, errNoConsoleSession},
		{"console detached", noConsoleSession, map[uint32]error{2: nil}, map[uint32]bool{2: false}, 0, errNoConsoleSession},
	}
	for _, c := range cases {
		queried := fakeWTS(t, c.console, rdp, nil, c.tokens)
		fakeLocks(t, c.locked)
		id, tok, err := findConsoleUserSession()
		if err == nil {
			tok.Close()
		}
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) || errors.Is(err, errNoUserSession) {
				t.Errorf("%s: session %d, %v; want %v", c.name, id, err, c.wantErr)
			}
		} else if err != nil || id != c.want {
			t.Errorf("%s: session %d, %v; want %d", c.name, id, err, c.want)
		}
		// Only the console is ever asked for a token.
		for _, q := range *queried {
			if q != c.console {
				t.Errorf("%s: queried session %d, want only the console", c.name, q)
			}
		}
	}

	// Any other failure is itself, not "nobody at the console".
	fakeWTS(t, 1, nil, nil, map[uint32]error{1: windows.ERROR_PRIVILEGE_NOT_HELD})
	if _, _, err := findConsoleUserSession(); errors.Is(err, errNoConsoleSession) || !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Errorf("privilege error: %v", err)
	}
}

// screenSessionSetup fakes a locked console (1) next to an unlocked RDP
// session (2), or a console at the logon screen when consoleUser is false,
// and a user-action runner that looks its target session up the way the
// real one does. It returns the targets and sessions the runs went to.
func screenSessionSetup(t *testing.T, consoleUser bool) (targets *[]sessionTarget, sessions *[]uint32, queried *[]uint32) {
	t.Helper()
	tokens := map[uint32]error{2: nil}
	if consoleUser {
		tokens[1] = nil
	}
	queried = fakeWTS(t, 1, []wtsSession{{ID: 1, State: windows.WTSActive}, {ID: 2, State: windows.WTSActive}}, nil, tokens)
	fakeLocks(t, map[uint32]bool{1: true, 2: false})
	fakeUserAction(t, nil)
	targets, sessions = new([]sessionTarget), new([]uint32)
	userActions.Exec = func(_ context.Context, target sessionTarget, _ string, args []string) ([]byte, error) {
		*targets = append(*targets, target)
		id, tok, err := wts.Find(target)
		if err != nil {
			return nil, fmt.Errorf("get session: %w", err)
		}
		tok.Close()
		*sessions = append(*sessions, id)
		return []byte(`{"ok":true,"screen":"` + args[len(args)-1] + `","audio":{"volume":1,"muted":true,"device":"콘솔"}}`), nil
	}
	resetTargetSession()
	setDisplayState("unknown")
	t.Cleanup(func() { resetTargetSession(); setDisplayState("unknown") })
	return targets, sessions, queried
}

// Next to a locked console the other commands go to the unlocked RDP
// session, but the monitor is on the console: the screen commands go there,
// and leave the target bookkeeping (the session the samples describe, the
// stored audio) exactly as it was.
func TestScreenRunsInConsoleSession(t *testing.T) {
	targets, sessions, queried := screenSessionSetup(t, true)
	// A target the lookup would not pick, so any lookup would show.
	observeTargetSession(7)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	recordAudioSample(useraction.Audio{Volume: 30, Device: "원격 오디오"}, at)

	Commands["turnscreenoff"].Execute()
	Commands["turnscreenon"].Execute()

	if want := []sessionTarget{sessionConsole, sessionConsole}; !reflect.DeepEqual(*targets, want) {
		t.Errorf("targets = %v, want %v", *targets, want)
	}
	if want := []uint32{1, 1}; !reflect.DeepEqual(*sessions, want) {
		t.Errorf("sessions = %v, want %v (the console)", *sessions, want)
	}
	if got := getDisplayState(); got != "on" {
		t.Errorf("display state = %q, want on", got)
	}
	for _, q := range *queried {
		if q != 1 {
			t.Errorf("queried session %d: the screen commands looked the target up", q)
		}
	}
	last, known := dev.target.Current()
	if !known || last != 7 {
		t.Errorf("target = %d (known %v), want 7 untouched", last, known)
	}
	if s, ok := currentAudio(); !ok || s.Device != "원격 오디오" || !s.UpdatedAt.Equal(at) {
		t.Errorf("audio = %+v, %v; want the stored sample untouched", s, ok)
	}

	// The other commands still follow the unlocked session.
	*targets, *sessions = nil, nil
	if _, err := runUserAction(context.Background(), "audio", "get"); err != nil {
		t.Fatalf("audio get: %v", err)
	}
	if !reflect.DeepEqual(*targets, []sessionTarget{sessionActiveUser}) || !reflect.DeepEqual(*sessions, []uint32{2}) {
		t.Errorf("audio get went to %v / %v, want the active user's session 2", *targets, *sessions)
	}
}

// A console at the logon screen is no_console_session even with an RDP
// user: the screen command is not sent to the RDP session's virtual
// display, and the display state is left alone.
func TestScreenConsoleLogonScreen(t *testing.T) {
	targets, sessions, _ := screenSessionSetup(t, false)
	setDisplayState("on")

	_, err := screenRun(context.Background(), useraction.ActionScreen, "off")
	if !errors.Is(err, errNoConsoleSession) {
		t.Errorf("err = %v, want errNoConsoleSession", err)
	}
	Commands["turnscreenoff"].Execute()
	if len(*sessions) != 0 || len(*targets) != 2 || (*targets)[0] != sessionConsole {
		t.Errorf("targets %v, sessions %v; want two console attempts and no run", *targets, *sessions)
	}
	if got := getDisplayState(); got != "on" {
		t.Errorf("display state = %q, want on (unchanged)", got)
	}
	_, known := dev.target.Current()
	if known {
		t.Error("the screen command recorded a target session")
	}
}
