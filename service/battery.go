package service

// Laptop battery (#112, docs/design/media-notify.md §13). GetSystemPowerStatus
// is a plain kernel32 query that works from session 0, so the service reads
// it itself every batteryPollEvery and keeps the newest answer for the
// status document. A change is pushed as battery.changed; a desktop, which
// has no battery, never changes and so never pushes.

import (
	"net/http"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// batteryPollEvery is how often the service asks. The percentage moves a
// point every few minutes at most, and the driver's routines ("below 20%")
// do not need better.
const batteryPollEvery = 60 * time.Second

// SYSTEM_POWER_STATUS values (WinBase.h).
const (
	acLineOnline = 1

	batteryFlagCharging  = 8
	batteryFlagNoBattery = 128
	batteryFlagUnknown   = 255
	// BatteryLifePercent is 0–100, or 255 when unknown.
)

// systemPowerStatus mirrors SYSTEM_POWER_STATUS.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// getSystemPowerStatus is the real query.
func getSystemPowerStatus() (systemPowerStatus, error) {
	var s systemPowerStatus
	ok, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	if ok == 0 {
		return s, err
	}
	return s, nil
}

// batteryInfo is the status "battery" block. Percent is 0–100, or -1 when
// Windows does not know it.
type batteryInfo struct {
	Present  bool `json:"present"`
	Percent  int  `json:"percent"`
	Charging bool `json:"charging"`
	AC       bool `json:"ac"`
}

// unknownBattery is what the status says before the first reading: no
// battery, nothing known.
var unknownBattery = batteryInfo{Percent: -1}

// decodeBattery turns the raw structure into the block.
//
//   - BatteryFlag 128 is "no system battery" — a desktop.
//   - BatteryFlag 255 is "unknown": some firmware reports it for a battery it
//     cannot read, some virtual machines for none at all. A known percentage
//     is taken as the sign that a battery is there.
//   - Bit 8 is "charging", only meaningful when the flag is not unknown.
//   - AC is ACLineStatus 1; 0 (offline) and 255 (unknown) read as false.
func decodeBattery(s systemPowerStatus) batteryInfo {
	out := batteryInfo{Percent: -1, AC: s.ACLineStatus == acLineOnline}
	if s.BatteryLifePercent <= 100 {
		out.Percent = int(s.BatteryLifePercent)
	}
	switch {
	case s.BatteryFlag == batteryFlagUnknown:
		out.Present = out.Percent >= 0
	case s.BatteryFlag&batteryFlagNoBattery != 0:
		out.Present = false
	default:
		out.Present = true
		out.Charging = s.BatteryFlag&batteryFlagCharging != 0
	}
	if !out.Present {
		// A desktop's "percent" is 255 anyway; never report a number or a
		// charge for a battery that is not there.
		out.Percent = -1
		out.Charging = false
	}
	return out
}

// batteryMonitor keeps the newest reading.
type batteryMonitor struct {
	mu    sync.Mutex
	last  batteryInfo
	known bool

	read     func() (systemPowerStatus, error)
	onChange func(batteryInfo)
}

var battery = &batteryMonitor{
	last:     unknownBattery,
	read:     getSystemPowerStatus,
	onChange: emitBatteryChanged,
}

// poll reads once and reports whether the block changed. The first reading
// only fills the cache: there is nothing to compare it with, and the hub
// gets it with the next status anyway. A failed read keeps the last value.
func (m *batteryMonitor) poll() bool {
	raw, err := m.read()
	if err != nil {
		logMsg("Battery: GetSystemPowerStatus failed: %v", err)
		return false
	}
	info := decodeBattery(raw)
	m.mu.Lock()
	first := !m.known
	changed := !first && info != m.last
	m.last, m.known = info, true
	m.mu.Unlock()
	if first {
		if info.Present {
			logMsg("Battery: %d%%, charging=%v, ac=%v", info.Percent, info.Charging, info.AC)
		}
		return false
	}
	if changed && m.onChange != nil {
		m.onChange(info)
	}
	return changed
}

// info returns the newest reading (unknownBattery before the first one).
func (m *batteryMonitor) info() batteryInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

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
	battery.poll()
	go func() {
		tick := time.NewTicker(batteryPollEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				battery.poll()
			}
		}
	}()
}

// handleBatteryAPI serves GET /api/battery for the app's status bar, behind
// the same session check as the other /api routes.
func handleBatteryAPI(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r, getConfig().Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, battery.info())
}
