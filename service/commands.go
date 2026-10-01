package service

import (
	"context"
	"errors"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Command represents a PC control command
type Command struct {
	Response string // HTTP response message
	Execute  func()
}

// Display state (edge-driver doc §3.2 "display"). The service cannot ask the
// monitor what it is doing, so this is simply the last screen command it
// sent (dev.display): "on" after turnscreenon, "off" after turnscreenoff,
// "unknown" until either has run in this process.

// setDisplayState records the effect of a screen command and, when the
// state actually changed, pushes display.changed to the SmartThings hub
// (edge-driver doc §3.5). It is device state, not a notification, so it
// goes to the bus taps only and never to Telegram.
func setDisplayState(state string) {
	if dev.display.Set(state) {
		emitDevice("display", "changed", map[string]string{"display": state})
	}
}

// getDisplayState returns "on", "off" or "unknown".
func getDisplayState() string { return dev.display.Get() }

// powerHint is the last power command this service ran (power.Hint), the
// best explanation of a stop Windows does not explain (edge-driver doc
// §3.5, power.stopping's reason).
var powerHint power.Hint

// notePowerCommand remembers command when it is one that ends the session.
func notePowerCommand(command string) { powerHint.Note(command) }

// stoppingReason explains a stop for power.stopping: a power command run
// in the last two minutes, else fallback.
func stoppingReason(fallback string) string { return powerHint.Reason(fallback) }

// resetPowerCommandHint forgets the hint (tests).
func resetPowerCommandHint() { powerHint.Reset() }

// screenRun is runUserActionIn pinned to the console session, replaced by
// the tests.
var screenRun = func(ctx context.Context, args ...string) (UserActionResult, error) {
	return runUserActionIn(ctx, sessionConsole, args...)
}

// setScreen runs `user-action screen <state>` ("off" or "on") in the
// session on the physical console — a service has no interactive desktop
// (session 0 isolation) — and records the display state. It replaced a
// PowerShell SendMessage(HWND_BROADCAST) that one hung window could block
// forever (#121); the child now uses SendMessageTimeoutW and runUserAction
// bounds the whole run (userActions.Timeout, child killed when it runs out).
//
// Unlike the other user actions it never follows the unlocked-session
// target: next to a locked console that is an RDP session, whose display
// is virtual, so SC_MONITORPOWER there leaves the real monitor alone. A
// locked console is used as it is, as before the unlocked-session target
// existed: the broadcast goes to its own desktop's windows. A
// console at the logon screen is errNoConsoleSession (see
// findConsoleUserSession), logged and not retried elsewhere.
//
// A run that timed out still counts: the broadcast was under way and the
// monitor reacts to the first window that handles it. Any other failure
// (nobody logged in at the console, the child could not start) leaves the
// state alone.
func setScreen(state string) {
	_, err := screenRun(context.Background(), useraction.ActionScreen, state)
	switch {
	case err == nil:
	case errors.Is(err, errUserActionTimeout):
		logMsg("screen %s: %v (assuming it took effect)", state, err)
	default:
		logMsg("screen %s: %v", state, err)
		return
	}
	setDisplayState(state)
}

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
		Execute:  func() { executeCommand("shutdown", systool.Shutdown, "/s", "/t", "5") },
	},
	"forceshutdown": {
		Response: "Force shutting down...",
		Execute:  func() { executeCommand("forceshutdown", systool.Shutdown, "/s", "/f", "/t", "0") },
	},
	"restart": {
		Response: "Restarting...",
		Execute:  func() { executeCommand("restart", systool.Shutdown, "/r", "/t", "5") },
	},
	// hibernate stays on shutdown.exe: /h is one fast call that already
	// reports a disabled hibernation in its own words.
	"hibernate": {
		Response: "Hibernating...",
		Execute:  func() { executeCommand("hibernate", systool.Shutdown, "/h") },
	},
	"suspend": {
		Response: "Suspending...",
		Execute:  suspendPC, // SetSuspendState (power_api.go)
	},
	"lock": {
		Response: "Locking...",
		Execute:  func() { lockAllSessions() },
	},
	"turnscreenoff": {
		Response: "Screen off...",
		Execute:  func() { setScreen("off") },
	},
	// turnscreenon wakes the monitor again (edge-driver doc §3.3): a zero
	// mouse move, then SC_MONITORPOWER -1 (useraction/screen.go).
	"turnscreenon": {
		Response: "Screen on...",
		Execute:  func() { setScreen("on") },
	},
}
