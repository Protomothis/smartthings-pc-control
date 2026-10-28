package service

// The suspend and lock commands through Windows APIs (#127). They replaced
// two PowerShell one-liners, which took most of a second just to start and
// looked powershell.exe up on PATH as SYSTEM:
//
//   - suspend: [System.Windows.Forms.Application]::SetSuspendState('Suspend',
//     $false, $false), which is powrprof's SetSuspendState(FALSE, FALSE,
//     FALSE) — power.SetSuspendState (on a modern standby PC the display
//     is turned off instead, #162);
//   - lock: Get-Process explorer, then `tsdiscon <session>` for each one,
//     which is WTSDisconnectSession — session.Disconnect for every session
//     with a logged-in user.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// powerCapsCache holds GetPwrCapabilities' answer for the life of the
// process: the sleep states are firmware settings and do not change while
// Windows runs. A mutex rather than sync.Once so the tests can reset it.
var powerCapsCache struct {
	mu   sync.Mutex
	done bool
	caps power.Capabilities
	err  error
}

// powerCaps returns the PC's sleep states (sys.powerCaps), read once.
func powerCaps() (power.Capabilities, error) {
	powerCapsCache.mu.Lock()
	defer powerCapsCache.mu.Unlock()
	if !powerCapsCache.done {
		powerCapsCache.caps, powerCapsCache.err = sys.powerCaps()
		powerCapsCache.done = true
	}
	return powerCapsCache.caps, powerCapsCache.err
}

// resetPowerCaps forgets the cached answer (tests).
func resetPowerCaps() {
	powerCapsCache.mu.Lock()
	powerCapsCache.done = false
	powerCapsCache.mu.Unlock()
}

// modernStandby reports whether this PC sleeps in S0 low power idle
// (#161, #162). Unreadable capabilities count as no: the commands then
// behave as they always have.
func modernStandby() bool {
	caps, err := powerCaps()
	return err == nil && caps.ModernStandby()
}

// logPowerCapabilities writes the sleep states to service.log once at
// start, so a support log shows which suspend path this PC takes.
func logPowerCapabilities() {
	caps, err := powerCaps()
	if err != nil {
		logMsg("Power: sleep states unreadable (%v); suspend uses SetSuspendState", err)
		return
	}
	logMsg("Power: %s", caps)
}

// suspendPC is the suspend command: SetSuspendState, logged, with
// system.exec_failed on failure like the commands that run a tool. On a
// modern standby PC it turns the display off instead (suspendToStandby).
func suspendPC() {
	caps, capsErr := powerCaps()
	if capsErr == nil && caps.ModernStandby() {
		suspendToStandby()
		return
	}
	notePowerCommand("suspend") // hint for power.stopping's reason (§3.5)
	if capsErr == nil && !caps.S3 {
		logMsg("suspend: this PC reports no S3 sleep (%s); Windows may hibernate instead", caps)
	}
	if err := sys.suspend(false); err != nil {
		logMsg("suspend error: %v", err)
		reportExecFailure("suspend", err, nil)
		return
	}
	logMsg("suspend ok (resumed)")
}

// suspendToStandby is suspend on a modern standby PC (#162). There is no
// S3, and SetSuspendState(FALSE, …) hibernates there — the PC then wakes
// only on the power button. No documented call enters modern standby; the
// display going off is what starts it, as Windows' own Sleep does, so this
// runs the screen-off user action in the console session (setScreen's
// path; session 0 has no display to turn off).
//
// It does not fall back to SetSuspendState: a hibernation the user did not
// ask for is the bug. A failed run is logged and raised as
// system.exec_failed, like a failed SetSuspendState.
//
// It leaves the power.stopping hint alone. A suspend broadcast, if Windows
// sends one on the way into standby, is reported as "suspend" without it;
// noting it would make any service stop in the next two minutes — the user
// waking the PC and restarting it — look like a suspend.
//
// The grace period and the other dispatch rules are unchanged: they run
// before Execute (dispatchCommand), whichever path this then takes.
func suspendToStandby() {
	logMsg("suspend: modern standby - turning the display off to enter standby instead of SetSuspendState, which would hibernate")
	_, err := userRun.screen(context.Background(), useraction.ActionScreen, "off")
	switch {
	case err == nil:
		logMsg("suspend ok: display off, Windows enters modern standby")
	case errors.Is(err, errUserActionTimeout):
		// Windows throttles desktop processes as the display goes off,
		// so the child may not answer in time; the broadcast was sent.
		logMsg("suspend: %v (the display was turning off; assuming Windows enters modern standby)", err)
	default:
		why := err.Error()
		if errors.Is(err, errNoConsoleSession) {
			why = "nobody is signed in on the PC's own screen"
		}
		logMsg("suspend error: modern standby - could not turn the display off: %s (not falling back to SetSuspendState, which would hibernate)", why)
		reportExecFailure("suspend", fmt.Errorf("modern standby: could not turn the display off (%s); not hibernating instead", why), nil)
		return
	}
	setDisplayState("off")
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
