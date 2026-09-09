package service

// Command represents a PC control command
type Command struct {
	Response string // HTTP response message
	Execute  func()
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
		Execute:  func() { executeCommand("shutdown", "/s", "/t", "5") },
	},
	"forceshutdown": {
		Response: "Force shutting down...",
		Execute:  func() { executeCommand("shutdown", "/s", "/f", "/t", "0") },
	},
	"restart": {
		Response: "Restarting...",
		Execute:  func() { executeCommand("shutdown", "/r", "/t", "5") },
	},
	"hibernate": {
		Response: "Hibernating...",
		Execute:  func() { executeCommand("shutdown", "/h") },
	},
	"suspend": {
		Response: "Suspending...",
		Execute: func() {
			executePowerShell("Add-Type -Assembly System.Windows.Forms; [System.Windows.Forms.Application]::SetSuspendState('Suspend', $false, $false)")
		},
	},
	"lock": {
		Response: "Locking...",
		Execute:  func() { lockAllSessions() },
	},
	"turnscreenoff": {
		Response: "Screen off...",
		Execute: func() {
			runPowerShellInUserSession("(Add-Type '[DllImport(\"user32.dll\")] public static extern int SendMessage(int hWnd,int hMsg,int wParam,int lParam);' -Name a -Pas)::SendMessage(-1,0x0112,0xF170,2)")
		},
	},
}
