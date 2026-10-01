package service

// One path for every catalogue command (#127): the legacy
// /{secret}/{command} URL, POST /st/v1/command, POST /api/command (the app,
// tray, toast and WebUI) and the Telegram bot all end in dispatchCommand,
// so the grace rule, last_command and the notifications cannot drift apart
// between them.

import (
	"time"
)

// dispatchMode says whether a command may wait out the grace period.
type dispatchMode int

const (
	// dispatchDefault defers a grace command while shutdown_grace is on.
	dispatchDefault dispatchMode = iota
	// dispatchGrace defers a grace command even with shutdown_grace off:
	// the driver's mode "grace" (edge-driver doc §3.3).
	dispatchGrace
	// dispatchImmediate never defers: the person asking is at the PC, has
	// confirmed it already (Telegram), or the driver said "immediate".
	dispatchImmediate
)

// recordCommand remembers name as last_command (status, Telegram
// /status), under origin's wire name: "remote" for the legacy URL,
// "smartthings", "ui". ping is never recorded, and neither are the bot's
// own commands — the bot reports those itself.
func recordCommand(name, from string, origin scheduleOrigin) {
	if name == "ping" || origin == originTelegram {
		return
	}
	noteRemoteCommandBy(name, from, origin.String())
}

// notifiesCommand reports whether a command that runs now is announced as
// remote.received / remote.force. Commands from the network are; of the
// UIs on this PC only the WebUI can be another device — telling the person
// at the PC on Telegram what they just pressed is noise — and the bot does
// not announce its own commands.
func notifiesCommand(name, from string, origin scheduleOrigin) bool {
	switch {
	case name == "ping": // ping is never notified
		return false
	case origin == originUI:
		return from == "webui"
	}
	return origin == originRemote || origin == originSmartThings
}

// dispatchCommand runs the catalogue command name for a caller: from is
// who asked (an address, a UI name), origin the path it came by
// (originRemote for the legacy URL, originSmartThings, originUI,
// originTelegram). It records the command (recordCommand), then either
// defers it by the grace period — a remote grace schedule, so the tray
// toast appears and remote.grace_scheduled carries the cancel buttons — or
// announces it and starts it in the background.
//
// deferred is the grace period when the command was scheduled, 0 when it
// runs now; ok is false for a name the catalogue does not have.
// forceshutdown is never deferred.
func dispatchCommand(name, from string, origin scheduleOrigin, mode dispatchMode) (deferred time.Duration, ok bool) {
	cmd, ok := Commands[name]
	if !ok {
		return 0, false
	}
	recordCommand(name, from, origin)

	cfg := getConfig()
	if mode != dispatchImmediate && name != "forceshutdown" && graceCommands[name] &&
		(cfg.ShutdownGrace || mode == dispatchGrace) {
		grace := cfg.GraceDuration()
		// originRemote whoever asked: this deferral exists so the tray
		// toast appears, and the app words it as SmartThings.
		if err := setSchedule(name, grace, originRemote); err == nil {
			logMsg("Command: %s from %s deferred %s (grace period — cancel from the app or tray)", name, from, formatDelay(grace))
			emit("remote", "grace_scheduled", map[string]string{
				"command":    name,
				"from":       from,
				"delay":      formatDelay(grace),
				"execute_at": time.Now().Add(grace).Format("15:04:05"),
			}, graceActions()...)
			return grace, true
		}
		logMsg("WARNING: grace scheduling failed for %s, executing immediately", name)
	}

	logMsg("Command: %s from %s (%s)", name, from, origin)
	if notifiesCommand(name, from, origin) {
		if name == "forceshutdown" {
			emit("remote", "force", map[string]string{"from": from})
		} else {
			emit("remote", "received", map[string]string{"command": name, "from": from})
		}
	}
	if cmd.Execute != nil {
		go cmd.Execute()
	}
	return 0, true
}
