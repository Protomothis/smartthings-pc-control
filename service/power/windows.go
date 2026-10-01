package power

import (
	"fmt"

	"golang.org/x/sys/windows"
)

var procSetSuspendState = windows.NewLazySystemDLL("powrprof.dll").NewProc("SetSuspendState")

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

// SetSuspendState is powrprof's SetSuspendState(hibernate, FALSE, FALSE):
// wake events stay enabled. It returns when the PC has resumed. (#127: it
// replaced PowerShell's [System.Windows.Forms.Application]::SetSuspendState,
// which makes the same call.)
func SetSuspendState(hibernate bool) error {
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
