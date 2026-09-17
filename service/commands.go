package service

import (
	"strconv"
	"sync"
)

// Command represents a PC control command
type Command struct {
	Response string // HTTP response message
	Execute  func()
}

// Display state (design doc §4.2 "display"). The service cannot ask the
// monitor what it is doing, so this is simply the last screen command it
// sent: "on" after turnscreenon, "off" after turnscreenoff, "unknown"
// until either has run in this process.
var (
	displayState   = "unknown"
	displayStateMu sync.RWMutex
)

// setDisplayState records the effect of a screen command.
func setDisplayState(state string) {
	displayStateMu.Lock()
	displayState = state
	displayStateMu.Unlock()
}

// getDisplayState returns "on", "off" or "unknown".
func getDisplayState() string {
	displayStateMu.RLock()
	defer displayStateMu.RUnlock()
	return displayState
}

// screenPowerScript builds the PowerShell one-liner that broadcasts
// WM_SYSCOMMAND/SC_MONITORPOWER with lParam: 2 turns the monitor off,
// -1 turns it back on. It must run in the user's session because a
// service has no interactive desktop (session 0 isolation).
func screenPowerScript(lParam int) string {
	return "(Add-Type '[DllImport(\"user32.dll\")] public static extern int SendMessage(int hWnd,int hMsg,int wParam,int lParam);' -Name a -Pas)::SendMessage(-1,0x0112,0xF170," +
		strconv.Itoa(lParam) + ")"
}

// Grace period bounds (seconds). defaultGraceSeconds applies when
// grace_seconds is missing from config.json; the app offers
// 10s/30s/1m/5m/10m/30m, the API accepts anything within the bounds.
const (
	defaultGraceSeconds = 300
	minGraceSeconds     = 5
	maxGraceSeconds     = 3600
)

// graceCommands are deferred by the grace period. forceshutdown is the
// escape hatch and always runs immediately; lock/screen-off are harmless.
var graceCommands = map[string]bool{
	"shutdown":  true,
	"restart":   true,
	"suspend":   true,
	"hibernate": true,
}

// Commands is the registry of all supported commands
var Commands = map[string]Command{
	"ping": {
		Response: "OK",
		Execute:  nil, // ping doesn't execute anything
	},
	"shutdown": {
		Response: "Shutting down...",
		Execute:  func() { executeCommand("shutdown", "shutdown", "/s", "/t", "5") },
	},
	"forceshutdown": {
		Response: "Force shutting down...",
		Execute:  func() { executeCommand("forceshutdown", "shutdown", "/s", "/f", "/t", "0") },
	},
	"restart": {
		Response: "Restarting...",
		Execute:  func() { executeCommand("restart", "shutdown", "/r", "/t", "5") },
	},
	"hibernate": {
		Response: "Hibernating...",
		Execute:  func() { executeCommand("hibernate", "shutdown", "/h") },
	},
	"suspend": {
		Response: "Suspending...",
		Execute: func() {
			executePowerShell("suspend", "Add-Type -Assembly System.Windows.Forms; [System.Windows.Forms.Application]::SetSuspendState('Suspend', $false, $false)")
		},
	},
	"lock": {
		Response: "Locking...",
		Execute:  func() { lockAllSessions() },
	},
	"turnscreenoff": {
		Response: "Screen off...",
		Execute: func() {
			runPowerShellInUserSession(screenPowerScript(2))
			setDisplayState("off")
		},
	},
	// turnscreenon wakes the monitor again (design doc §4.3). SC_MONITORPOWER
	// with lParam -1 is the documented "power on" value.
	"turnscreenon": {
		Response: "Screen on...",
		Execute: func() {
			runPowerShellInUserSession(screenPowerScript(-1))
			setDisplayState("on")
		},
	},
}
