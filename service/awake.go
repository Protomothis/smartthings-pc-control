package service

// Keep-awake (#111): the service's controller (service/power.Awake), its
// awake.changed push and the wall-clock check.

import (
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/power"
)

// awake is the service's controller; tests swap it (see stubAwake).
var (
	awake   = newAwakeController()
	awakeMu sync.RWMutex
)

// newAwakeController builds the controller the service uses.
func newAwakeController() *power.Awake {
	return power.NewAwake(power.AwakeHooks{
		KeepDisplay: func() bool { return getConfig().Awake.KeepDisplay },
		OnChange:    emitAwakeChanged,
	})
}

func currentAwake() *power.Awake {
	awakeMu.RLock()
	defer awakeMu.RUnlock()
	return awake
}

// emitAwakeChanged is the awake.changed push (§3.5 taps only: device state,
// not a notification).
func emitAwakeChanged(v awakeView) {
	w := v.Wire()
	on := "false"
	if w.On {
		on = "true"
	}
	emitDevice("awake", "changed", map[string]string{"on": on, "until": w.Until})
}

// startAwake runs the wall-clock check until stop closes, then releases the
// request. startupHooks calls it.
func startAwake(stop <-chan struct{}) {
	go func() {
		tick := time.NewTicker(power.AwakeCheckEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				currentAwake().Shutdown()
				return
			case <-tick.C:
				currentAwake().Tick()
			}
		}
	}()
}
