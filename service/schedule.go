package service

// The schedule slot (service/power.Scheduler) wired to the catalogue, the
// notifications and the Telegram grace message, and the views of it.

import (
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/tgcontrol"
)

// scheduleOrigin and its values under the names the service code has
// always used.
type scheduleOrigin = power.Origin

const (
	originUI          = power.OriginUI
	originRemote      = power.OriginRemote
	originTelegram    = power.OriginTelegram
	originSmartThings = power.OriginSmartThings
)

// replacedSchedule is the summary of a schedule a newer one cancelled.
type replacedSchedule = power.Replaced

// formatDelay renders a delay for log lines and notifications ("30 sec",
// "5 min", "1 h 30 min", "1 d 3 h").
func formatDelay(d time.Duration) string { return power.FormatDelay(d) }

// scheduler is the single schedule slot. Its hooks announce each change:
// a remote grace deferral is reported as remote.* (remote.grace_scheduled
// itself comes from dispatchCommand, which knows the caller and carries
// the cancel buttons), everything else as schedule.*; and the Telegram
// grace message of a schedule that ends is stamped once (#62).
var scheduler = &power.Scheduler{
	Lookup: func(command string) (func(), bool) {
		cmd, ok := Commands[command]
		return cmd.Execute, ok
	},
	Log: logMsg,
	Hooks: power.Hooks{
		Replaced: func(old power.Task, command string, origin power.Origin) {
			emit("schedule", "replaced", map[string]string{
				"command": command, "origin": origin.String(),
				"old_command": old.Command, "old_origin": old.Origin.String(),
			})
			tgCtl.FinishGraceMessage(old.Seq, tgcontrol.GraceReplaced, "")
		},
		Created: func(t power.Task, delay time.Duration) {
			if t.Origin == originRemote {
				return
			}
			emit("schedule", "created", map[string]string{
				"command": t.Command, "origin": t.Origin.String(),
				"delay": formatDelay(delay), "execute_at": t.ExecuteAt.Format("15:04:05"),
			})
		},
		Fired: func(t power.Task) {
			if t.Origin == originRemote {
				emit("remote", "executed", map[string]string{"command": t.Command})
			} else {
				emit("schedule", "executed", map[string]string{"command": t.Command, "origin": t.Origin.String()})
			}
			tgCtl.FinishGraceMessage(t.Seq, tgcontrol.GraceExecuted, "timer")
		},
		Ended: func(t power.Task, by string, runNow bool) {
			if t.Origin == originRemote {
				emit("remote", "grace_cancelled", map[string]string{"command": t.Command, "by": by})
			} else {
				emit("schedule", "cancelled", map[string]string{
					"command": t.Command, "origin": t.Origin.String(), "by": by,
				})
			}
			result := tgcontrol.GraceCancelled
			if runNow {
				result = tgcontrol.GraceExecuted
			}
			tgCtl.FinishGraceMessage(t.Seq, result, by)
		},
	},
}

// getSchedule returns the current scheduled task info, the /api/schedule
// wire form: {"active": false}, or the command, origin, executeAt
// (RFC3339), remainingSec and, when it displaced one, replaced.
func getSchedule() map[string]interface{} {
	t, ok := scheduler.Current()
	if !ok {
		return map[string]interface{}{"active": false}
	}
	remaining := time.Until(t.ExecuteAt).Seconds()
	if remaining < 0 {
		remaining = 0
	}
	info := map[string]interface{}{
		"active":       true,
		"command":      t.Command,
		"origin":       t.Origin.String(),
		"executeAt":    t.ExecuteAt.Format(time.RFC3339),
		"remainingSec": int(remaining),
	}
	if t.Replaced != nil {
		info["replaced"] = t.Replaced
	}
	return info
}

// wakeTrayApp launches the tray app in the background and logs the outcome.
// It never affects the scheduled command: a failure (no user logged in,
// token error, ...) only means no toast is shown.
func wakeTrayApp(command string) {
	launch := sys.trayLaunch // read before the goroutine: tests swap it back
	go func() {
		if err := launch(); err != nil {
			logMsg("Tray app wake failed for %s (grace toast may not appear): %v", command, err)
			emit("system", "tray_wake_failed", map[string]string{"command": command, "error": err.Error()})
			return
		}
		logMsg("Tray app launched in user session for %s grace toast", command)
	}()
}

// setSchedule creates a new scheduled task. origin says who requested it;
// remote grace schedules additionally wake the tray app (see wakeTrayApp).
func setSchedule(command string, delay time.Duration, origin scheduleOrigin) error {
	if err := scheduleTask(command, delay, origin); err != nil {
		return err
	}
	if origin.WakesTrayApp() {
		wakeTrayApp(command)
	}
	return nil
}

// scheduleTask arms the timer for command (power.Scheduler.Schedule):
// there is a single schedule slot, and an existing schedule is cancelled
// and remembered as replaced on the new one (#54).
func scheduleTask(command string, delay time.Duration, origin scheduleOrigin) error {
	return scheduler.Schedule(command, delay, origin)
}

// cancelScheduleBy cancels the current scheduled task. by says who asked
// (api/webui/app/toast/tray/telegram) and is reported in the notification:
// remote.grace_cancelled for a grace deferral, schedule.cancelled otherwise.
func cancelScheduleBy(by string) bool {
	_, ok := takeSchedule(by)
	return ok
}

// takeSchedule cancels the current scheduled task on behalf of by and
// returns its command.
func takeSchedule(by string) (string, bool) {
	return scheduler.End(by, false)
}

// takeScheduleForRun ends the current scheduled task because by is about
// to execute its command right away (/now, the runnow: button). The
// notifications are the same as a cancel; only the Telegram grace message
// is stamped "executed" instead of "cancelled".
func takeScheduleForRun(by string) (string, bool) {
	return scheduler.End(by, true)
}

// activeScheduleSeq returns the seq of the current schedule when it runs
// command on behalf of origin (#62).
func activeScheduleSeq(command string, origin scheduleOrigin) (uint64, bool) {
	return scheduler.ActiveSeq(command, origin)
}
