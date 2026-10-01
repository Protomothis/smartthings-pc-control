package service

// Laptop battery (#112, docs/design/media-notify.md §13). GetSystemPowerStatus
// is a plain kernel32 query that works from session 0, so the service reads
// it itself every batteryPollEvery and keeps the newest answer for the
// status document. A change is pushed as battery.changed; a desktop, which
// has no battery, never changes and so never pushes. The reading and its
// decoding are service/devstate.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/devstate"
)

// batteryPollEvery is how often the service asks. The percentage moves a
// point every few minutes at most, and the driver's routines ("below 20%")
// do not need better.
const batteryPollEvery = 60 * time.Second

type (
	batteryInfo       = devstate.BatteryInfo
	systemPowerStatus = devstate.PowerStatus
	batteryMonitor    = devstate.BatteryMonitor
)

// battery is the service's monitor; the tests swap it.
var battery = devstate.NewBatteryMonitor(devstate.GetSystemPowerStatus, emitBatteryChanged)

// emitBatteryChanged is the battery.changed push (taps only).
func emitBatteryChanged(b batteryInfo) {
	emitDevice("battery", "changed", map[string]string{
		"present":  strconv.FormatBool(b.Present),
		"percent":  strconv.Itoa(b.Percent),
		"charging": strconv.FormatBool(b.Charging),
		"ac":       strconv.FormatBool(b.AC),
	})
}

// startBatteryMonitor takes the first reading now, so the first status
// already has it, and then polls until stop closes. startupHooks calls it.
func startBatteryMonitor(stop <-chan struct{}) {
	battery.Poll()
	go func() {
		tick := time.NewTicker(batteryPollEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				battery.Poll()
			}
		}
	}()
}

// handleBatteryAPI serves GET /api/battery for the app's status bar, behind
// the same session check as the other /api routes.
var handleBatteryAPI = apiAuth(serveBatteryAPI, http.MethodGet)

func serveBatteryAPI(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, battery.Info())
}
