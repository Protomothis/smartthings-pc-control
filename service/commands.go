package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Command represents a PC control command
type Command struct {
	Response string // HTTP response message
	Execute  func()
}

// Display state (edge-driver doc §3.2 "display"). The service cannot ask the
// monitor what it is doing, so this is simply the last screen command it
// sent: "on" after turnscreenon, "off" after turnscreenoff, "unknown"
// until either has run in this process.
var (
	displayState   = "unknown"
	displayStateMu sync.RWMutex
)

// setDisplayState records the effect of a screen command and, when the
// state actually changed, pushes display.changed to the SmartThings hub
// (edge-driver doc §3.5). It is device state, not a notification, so it
// goes to the bus taps only and never to Telegram.
func setDisplayState(state string) {
	displayStateMu.Lock()
	changed := displayState != state
	displayState = state
	displayStateMu.Unlock()
	if changed {
		emitDevice("display", "changed", map[string]string{"display": state})
	}
}

// getDisplayState returns "on", "off" or "unknown".
func getDisplayState() string {
	displayStateMu.RLock()
	defer displayStateMu.RUnlock()
	return displayState
}

// Last executed power command (edge-driver doc §3.5, "power.stopping"
// data.reason). Windows tells the service that it is stopping, and that
// the machine is suspending, but not why: SERVICE_CONTROL_SHUTDOWN looks
// the same for `shutdown /s` and `shutdown /r`, and PBT_APMSUSPEND looks
// the same for sleep and hibernation. The command this service ran just
// before is the best hint available, so it is remembered here.
var (
	lastPowerCommand   string
	lastPowerCommandAt time.Time
	lastPowerCommandMu sync.Mutex
)

// powerCommandReason maps a catalogue command to the reason the Edge
// driver's power state machine understands (§6.2).
var powerCommandReason = map[string]string{
	"shutdown":      "shutdown",
	"forceshutdown": "shutdown",
	"restart":       "restart",
	"suspend":       "suspend",
	"hibernate":     "hibernate",
}

// powerCommandHintTTL is how long a command stays a plausible explanation
// for a stop. The commands wait 5s (`shutdown /t 5`) and Windows then
// takes a while to tell the services, so this is generous.
const powerCommandHintTTL = 2 * time.Minute

// notePowerCommand remembers command when it is one that ends the session;
// anything else (ping, lock, screen) leaves the hint alone.
func notePowerCommand(command string) {
	if _, ok := powerCommandReason[command]; !ok {
		return
	}
	lastPowerCommandMu.Lock()
	lastPowerCommand, lastPowerCommandAt = command, time.Now()
	lastPowerCommandMu.Unlock()
}

// stoppingReason explains a stop for power.stopping. A power command run
// in the last powerCommandHintTTL wins; otherwise fallback is used, which
// is what the caller could work out on its own ("shutdown" for a system
// shutdown, "suspend" for a suspend broadcast, "unknown" for a plain
// service stop).
func stoppingReason(fallback string) string {
	lastPowerCommandMu.Lock()
	cmd, at := lastPowerCommand, lastPowerCommandAt
	lastPowerCommandMu.Unlock()
	if cmd != "" && time.Since(at) <= powerCommandHintTTL {
		if reason, ok := powerCommandReason[cmd]; ok {
			return reason
		}
	}
	return fallback
}

// resetPowerCommandHint forgets the hint (tests).
func resetPowerCommandHint() {
	lastPowerCommandMu.Lock()
	lastPowerCommand, lastPowerCommandAt = "", time.Time{}
	lastPowerCommandMu.Unlock()
}

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
// bounds the whole run (userActionTimeout, child killed when it runs out).
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
