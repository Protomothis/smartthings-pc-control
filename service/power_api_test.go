package service

// Tests for the power and lock commands behind their seams (#127): no test
// ever suspends, locks or shuts down the machine it runs on.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// stubDisconnect records the sessions lockAllSessions disconnects; fail
// makes those sessions' disconnect fail.
func stubDisconnect(t *testing.T, fail map[uint32]bool) *[]uint32 {
	t.Helper()
	var got []uint32
	saved := wtsDisconnectSession
	wtsDisconnectSession = func(id uint32) error {
		got = append(got, id)
		if fail[id] {
			return windows.ERROR_ACCESS_DENIED
		}
		return nil
	}
	t.Cleanup(func() { wtsDisconnectSession = saved })
	return &got
}

// Every active session with a user is disconnected, the console and RDP
// alike, as `tsdiscon` did for every session with an explorer.exe; session
// 0, the logon screen and sessions that are not active are left alone.
func TestLockAllSessionsDisconnectsEverySessionWithAUser(t *testing.T) {
	active, disc := uint32(windows.WTSActive), uint32(windows.WTSDisconnected)
	fakeWTS(t, 1, []wtsSession{{ID: 0, State: disc}, {ID: 1, State: active}, {ID: 2, State: active}, {ID: 3, State: active}, {ID: 4, State: disc}},
		nil, map[uint32]error{1: nil, 2: nil, 4: nil}) // 3: logon screen
	got := stubDisconnect(t, nil)
	buf := captureLog(t)

	lockAllSessions()
	if !reflect.DeepEqual(*got, []uint32{1, 2}) {
		t.Errorf("disconnected %v, want [1 2]", *got)
	}
	if !strings.Contains(buf.String(), "lockAllSessions success") {
		t.Errorf("log: %q", buf.String())
	}
}

func TestLockAllSessionsReportsFailures(t *testing.T) {
	active := uint32(windows.WTSActive)
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}, {ID: 2, State: active}}, nil, map[uint32]error{1: nil, 2: nil})
	got := stubDisconnect(t, map[uint32]bool{1: true})
	buf := captureLog(t)

	lockAllSessions()
	// One failure does not stop the others.
	if !reflect.DeepEqual(*got, []uint32{1, 2}) {
		t.Errorf("disconnected %v, want [1 2]", *got)
	}
	if log := buf.String(); !strings.Contains(log, "lockAllSessions error") || !strings.Contains(log, "session 2 disconnected") {
		t.Errorf("log: %q", log)
	}
}

func TestLockAllSessionsNobodyLoggedIn(t *testing.T) {
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: uint32(windows.WTSActive)}}, nil, nil)
	got := stubDisconnect(t, nil)
	buf := captureLog(t)
	lockAllSessions()
	if len(*got) != 0 || !strings.Contains(buf.String(), "no session with a logged-in user") {
		t.Errorf("disconnected %v, log %q", *got, buf.String())
	}

	fakeWTS(t, 1, nil, errors.New("rpc down"), nil)
	lockAllSessions()
	if !strings.Contains(buf.String(), "WTSEnumerateSessions: rpc down") {
		t.Errorf("log %q", buf.String())
	}
}

// suspend is SetSuspendState(FALSE, …) — sleep, never hibernate — and is
// remembered as the power command that explains the next stop.
func TestSuspendCallsSetSuspendState(t *testing.T) {
	resetPowerCommandHint()
	t.Cleanup(resetPowerCommandHint)
	var calls []bool
	saved := setSuspendState
	setSuspendState = func(hibernate bool) error { calls = append(calls, hibernate); return nil }
	t.Cleanup(func() { setSuspendState = saved })

	Commands["suspend"].Execute()
	if !reflect.DeepEqual(calls, []bool{false}) {
		t.Errorf("SetSuspendState calls %v, want [false]", calls)
	}
	if got := stoppingReason("unknown"); got != "suspend" {
		t.Errorf("stoppingReason = %q, want suspend", got)
	}
}

func TestSuspendFailureRaisesExecFailed(t *testing.T) {
	events := captureNotifications(t)
	saved := setSuspendState
	setSuspendState = func(bool) error {
		return errors.New("SetSuspendState: A required privilege is not held by the client.")
	}
	t.Cleanup(func() { setSuspendState = saved })

	suspendPC()
	ev := expectNotification(t, events, "system.exec_failed")
	if ev.Fields["command"] != "suspend" || !strings.Contains(ev.Fields["error"], "privilege") {
		t.Errorf("fields = %v", ev.Fields)
	}
}

// The tool commands run shutdown.exe from System32 with the same
// arguments as before.
func TestPowerCommandsRunShutdownExe(t *testing.T) {
	resetPowerCommandHint()
	t.Cleanup(resetPowerCommandHint)
	var got [][]string
	saved := runTool
	runTool = func(tool string, args ...string) ([]byte, error) {
		got = append(got, append([]string{tool}, args...))
		return nil, nil
	}
	t.Cleanup(func() { runTool = saved })

	for _, name := range []string{"shutdown", "forceshutdown", "restart", "hibernate"} {
		Commands[name].Execute()
	}
	want := [][]string{
		{systool.Shutdown, "/s", "/t", "5"},
		{systool.Shutdown, "/s", "/f", "/t", "0"},
		{systool.Shutdown, "/r", "/t", "5"},
		{systool.Shutdown, "/h"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runs %q, want %q", got, want)
	}
}
