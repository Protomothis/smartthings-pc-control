package service

// The suspend and lock commands through Windows APIs (#127). They replaced
// two PowerShell one-liners, which took most of a second just to start and
// looked powershell.exe up on PATH as SYSTEM:
//
//   - suspend: [System.Windows.Forms.Application]::SetSuspendState('Suspend',
//     $false, $false), which is powrprof's SetSuspendState(FALSE, FALSE,
//     FALSE) — power.SetSuspendState;
//   - lock: Get-Process explorer, then `tsdiscon <session>` for each one,
//     which is WTSDisconnectSession — session.Disconnect for every session
//     with a logged-in user.

import (
	"errors"
	"fmt"
	"strings"
)

// suspendPC is the suspend command: SetSuspendState, logged, with
// system.exec_failed on failure like the commands that run a tool.
func suspendPC() {
	notePowerCommand("suspend") // hint for power.stopping's reason (§3.5)
	if err := sys.suspend(false); err != nil {
		logMsg("suspend error: %v", err)
		reportExecFailure("suspend", err, nil)
		return
	}
	logMsg("suspend ok (resumed)")
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
		if err := sys.disconnect(id); err != nil {
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
