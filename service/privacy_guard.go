package service

// Camera and microphone in-use detection (#143, docs/design/v1.3-sensors.md):
// the monitor (service/privacy) for the target session's user, its 3-second
// loop, the privacy.changed push and the guard that warns about or refuses
// a remote or scheduled shutdown, restart, sleep or hibernate while either
// is in use.

import (
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/privacy"
	"github.com/Protomothis/smartthings-pc-control/service/status"

	"golang.org/x/sys/windows"
)

// privacyScanInterval is how often the consent store is read: about the
// rhythm of the other device loops, and quick enough that a call starting
// reaches a routine within seconds. A scan is a handful of registry reads.
const privacyScanInterval = 3 * time.Second

// privacyMon reads HKU\<SID> of the user the commands act on. Its funcs
// are the tests' seams.
var privacyMon = &privacy.Monitor{User: targetUserSID, Open: privacy.OpenUserHive}

// privacyZone is the zone the status "since" is written in (the golden
// tests pin it).
var privacyZone = func() *time.Location { return time.Local }

// privacyKick wakes the loop after a config save.
var privacyKick = make(chan struct{}, 1)

// kickPrivacyScan asks for an early scan; it never blocks.
func kickPrivacyScan() {
	select {
	case privacyKick <- struct{}{}:
	default:
	}
}

// privacyOn remembers whether detection was on at the last refresh, so
// switching it on or off is reported like a device change: the status
// block and the "privacy" feature come and go with it.
var privacyOn struct {
	mu sync.Mutex
	on bool
}

// targetUserSID is the SID of the target session's user (findUserSession,
// the session every command acts on).
func targetUserSID() (string, error) {
	_, tok, err := findUserSession()
	if err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := windows.Token(tok).GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// refreshPrivacy scans while privacy.enabled is on and reports a change as
// privacy.changed (whose push data is the status block itself, filled in
// at delivery). Turning the option on or off is a change too. Off, the
// consent store is not read and the reading is unknown.
func refreshPrivacy(cfg Config) privacy.Reading {
	privacyOn.mu.Lock()
	defer privacyOn.mu.Unlock()
	if !cfg.Privacy.Enabled {
		if privacyOn.on {
			privacyOn.on = false
			privacyMon.Reset()
			logMsg("Privacy: detection off")
			emitDevice("privacy", "changed", nil)
		}
		return privacy.UnknownReading()
	}
	next, changed := privacyMon.Scan()
	turnedOn := !privacyOn.on
	privacyOn.on = true
	if changed || turnedOn {
		logMsg("Privacy: %s", next.LogLine())
		emitDevice("privacy", "changed", nil)
	}
	return next
}

// resetPrivacy forgets the reading and the on/off memory (tests).
func resetPrivacy() {
	privacyOn.mu.Lock()
	privacyOn.on = false
	privacyOn.mu.Unlock()
	privacyMon.Reset()
}

// watchPrivacy scans every privacyScanInterval, and early after a save,
// until stop closes. While the option is off a tick costs a config read.
func watchPrivacy(stop <-chan struct{}) {
	t := time.NewTicker(privacyScanInterval)
	defer t.Stop()
	refreshPrivacy(getConfig())
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		case <-privacyKick:
		}
		refreshPrivacy(getConfig())
	}
}

// stPrivacyStatus is the status block for cfg: nil while the option is
// off, so the key is left out.
func stPrivacyStatus(cfg Config) *status.Privacy {
	if !cfg.Privacy.Enabled {
		return nil
	}
	b := privacyMon.Current().Wire(privacyZone())
	return &b
}

// ---- the guard -------------------------------------------------------------

// privacyGuarded reports whether the guard covers name when it comes from
// origin/from: the power commands the grace period defers, from anywhere
// but the person at this PC. The desktop app, the tray and the toast are
// at the keyboard; the WebUI may be a phone across the room.
func privacyGuarded(name, from string, origin scheduleOrigin) bool {
	if !graceCommands[name] {
		return false
	}
	return origin != originUI || from == "webui"
}

// privacyVerdict judges a power command against a fresh reading. The zero
// verdict (go ahead, nothing to say) when the option or the guard is off
// or name is not a power command.
func privacyVerdict(name string) privacy.Verdict {
	cfg := getConfig()
	if !cfg.Privacy.Enabled || cfg.Privacy.Guard == config.PrivacyGuardOff || !graceCommands[name] {
		return privacy.Verdict{}
	}
	return privacy.Judge(cfg.Privacy.Guard, refreshPrivacy(cfg))
}

// remoteVerdict is privacyVerdict for a command from origin/from, the zero
// verdict when the guard does not cover the caller.
func remoteVerdict(name, from string, origin scheduleOrigin) privacy.Verdict {
	if !privacyGuarded(name, from, origin) {
		return privacy.Verdict{}
	}
	return privacyVerdict(name)
}

// privacyRefusal is the refusal dispatchCommand returns for a blocked
// command, after logging it and — for the callers dispatchCommand
// announces — raising privacy.blocked.
func privacyRefusal(name, from string, origin scheduleOrigin, v privacy.Verdict) error {
	logMsg("Command: %s from %s refused: %s in use (privacy guard)", name, from, v.Devices())
	if notifiesCommand(name, from, origin) {
		emit("privacy", "blocked", map[string]string{"command": name, "from": from, "in_use": v.Devices()})
	}
	return &privacy.DeviceInUseError{Command: name, Devices: v.InUse}
}

// privacyHold is the scheduler's Hold (power.Scheduler): right before a
// schedule runs a power command, "block" drops it while a device is in
// use, and "warn" pushes it back once by the grace period (then runs it).
// Every schedule counts — a timer set at the PC may fire after a call has
// started — and each outcome is notified (privacy.blocked,
// privacy.extended).
func privacyHold(t power.Task) (time.Duration, bool) {
	v := privacyVerdict(t.Command)
	switch {
	case v.Block:
		logMsg("Scheduled %s refused: %s in use (privacy guard)", t.Command, v.Devices())
		emit("privacy", "blocked", map[string]string{
			"command": t.Command, "from": "schedule", "origin": t.Origin.String(), "in_use": v.Devices(),
		})
		return 0, true
	case v.Warn() && t.Held == 0:
		d := getConfig().GraceDuration()
		logMsg("Scheduled %s held back %s: %s in use (privacy guard)", t.Command, formatDelay(d), v.Devices())
		emit("privacy", "extended", map[string]string{
			"command": t.Command, "origin": t.Origin.String(), "in_use": v.Devices(),
			"delay": formatDelay(d), "execute_at": time.Now().Add(d).Format("15:04:05"),
		})
		return d, false
	}
	return 0, false
}

// schedulePrivacyWarning is the /api/schedule "in_use" list the grace
// toast shows: the devices in use while a guarded schedule waits, from
// the last reading (the schedule poll must not scan). nil when none.
func schedulePrivacyWarning(command string) []string {
	cfg := getConfig()
	if !cfg.Privacy.Enabled || !graceCommands[command] {
		return nil
	}
	return privacy.Judge(cfg.Privacy.Guard, privacyMon.Current()).InUse
}

func init() {
	scheduler.Hold = privacyHold
}
