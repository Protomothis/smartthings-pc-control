package service

// Running-app detection (#110, #123): the service's scanner
// (service/activity), its 10-second loop and the activity.changed push.

import (
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/activity"
)

// activityScanInterval is how often the process list is read (§11).
const activityScanInterval = 10 * time.Second

// activityScan is the service's scanner. It reads the process list through
// sys.processes at scan time, so the tests' replacement takes effect.
var activityScan = &activity.Scanner{List: func() ([]string, error) { return sys.processes() }}

// activityKick wakes the scanner after a config save, so enabling the
// option or editing the list shows up in the status without waiting for
// the next tick.
var activityKick = make(chan struct{}, 1)

// kickActivityScan asks for an early scan; it never blocks.
func kickActivityScan() {
	select {
	case activityKick <- struct{}{}:
	default:
	}
}

// stActivityStatus is the status block for the live config.
func stActivityStatus(cfg Config) stActivity {
	return activityScan.Current(cfg.Activity)
}

// activityTick runs one scan and reports a change as activity.changed. The
// push data is the status block itself, filled in at delivery (stapi),
// so the event carries no fields of its own.
func activityTick(cfg ActivityConfig) {
	next, changed := activityScan.Scan(cfg)
	if !changed {
		return
	}
	logMsg("Activity: %s", activity.LogLine(next))
	emitDevice("activity", "changed", nil)
}

// watchActivity scans every activityScanInterval, and early after a save,
// until stop closes. While the option is off a tick costs a config read:
// a scan does not touch the process list then.
func watchActivity(stop <-chan struct{}) {
	t := time.NewTicker(activityScanInterval)
	defer t.Stop()
	activityTick(getConfig().Activity)
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		case <-activityKick:
		}
		activityTick(getConfig().Activity)
	}
}

// runningProcessNames is the app's picker list (/api/processes).
func runningProcessNames() ([]string, error) {
	names, err := sys.processes()
	if err != nil {
		return nil, err
	}
	return activity.PickerNames(names), nil
}
