package service

// Power and lock commands through Windows APIs (#127). They replaced two
// PowerShell one-liners, which took most of a second just to start and
// looked powershell.exe up on PATH as SYSTEM:
//
//   - suspend: [System.Windows.Forms.Application]::SetSuspendState('Suspend',
//     $false, $false), which is powrprof's SetSuspendState(FALSE, FALSE,
//     FALSE) — called here directly;
//   - lock: Get-Process explorer, then `tsdiscon <session>` for each one,
//     which is WTSDisconnectSession — called here for every session with a
//     logged-in user.

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

var (
	procSetSuspendState      = windows.NewLazySystemDLL("powrprof.dll").NewProc("SetSuspendState")
	procWTSDisconnectSession = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSDisconnectSession")
)

// enableShutdownPrivilege turns SE_SHUTDOWN_NAME on in the process token,
// which SetSuspendState requires. LocalSystem holds the privilege but has
// it disabled. Best effort: when it cannot be enabled, SetSuspendState
// fails and says why.
func enableShutdownPrivilege() {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	var luid windows.LUID
	name, _ := windows.UTF16PtrFromString("SeShutdownPrivilege")
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	_ = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil) // see above
}

// setSuspendState is powrprof's SetSuspendState(hibernate, FALSE, FALSE):
// wake events stay enabled. It returns when the PC has resumed. Replaced by
// the tests, which must never suspend the machine they run on.
var setSuspendState = func(hibernate bool) error {
	enableShutdownPrivilege()
	var h uintptr
	if hibernate {
		h = 1
	}
	if r, _, err := procSetSuspendState.Call(h, 0, 0); r == 0 {
		return fmt.Errorf("SetSuspendState: %w", err)
	}
	return nil
}

// suspendPC is the suspend command: SetSuspendState, logged, with
// system.exec_failed on failure like the commands that run a tool.
func suspendPC() {
	notePowerCommand("suspend") // hint for power.stopping's reason (§3.5)
	if err := setSuspendState(false); err != nil {
		logMsg("suspend error: %v", err)
		reportExecFailure("suspend", err, nil)
		return
	}
	logMsg("suspend ok (resumed)")
}

// wtsDisconnectSession is WTSDisconnectSession on the local server without
// waiting, the call behind tsdiscon. Replaced by the tests.
var wtsDisconnectSession = func(session uint32) error {
	if r, _, err := procWTSDisconnectSession.Call(0, uintptr(session), 0); r == 0 {
		return err
	}
	return nil
}

// lockAllSessions locks the PC: every session with a user (wts.LoggedOn:
// the console and any RDP login; a session at the logon screen has
// nothing to lock) is disconnected, which leaves the console at the lock
// screen and ends an RDP connection.
func lockAllSessions() {
	ids, err := wts.LoggedOn()
	if err != nil {
		logMsg("lockAllSessions error: %v", err)
		return
	}
	if len(ids) == 0 {
		logMsg("lockAllSessions: no session with a logged-in user")
		return
	}
	var lines []string
	var errs []error
	for _, id := range ids {
		if err := wtsDisconnectSession(id); err != nil {
			errs = append(errs, fmt.Errorf("session %d: %w", id, err))
			lines = append(lines, fmt.Sprintf("session %d: %v", id, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("session %d disconnected", id))
	}
	if err := errors.Join(errs...); err != nil {
		logMsg("lockAllSessions error: %v - %s", err, strings.Join(lines, "; "))
		return
	}
	logMsg("lockAllSessions success - %s", strings.Join(lines, "; "))
}
