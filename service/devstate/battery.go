package devstate

// Laptop battery (#112, docs/design/media-notify.md §13). GetSystemPowerStatus
// is a plain kernel32 query that works from session 0, so the service reads
// it itself and keeps the newest answer for the status document.

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
)

// SYSTEM_POWER_STATUS values (WinBase.h).
const (
	acLineOnline = 1

	batteryFlagCharging  = 8
	batteryFlagNoBattery = 128
	batteryFlagUnknown   = 255
	// BatteryLifePercent is 0–100, or 255 when unknown.
)

// PowerStatus mirrors SYSTEM_POWER_STATUS.
type PowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// GetSystemPowerStatus is the real query.
func GetSystemPowerStatus() (PowerStatus, error) {
	var s PowerStatus
	ok, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	if ok == 0 {
		return s, err
	}
	return s, nil
}

// BatteryInfo is the status "battery" block. Percent is 0–100, or -1 when
// Windows does not know it.
type BatteryInfo struct {
	Present  bool `json:"present"`
	Percent  int  `json:"percent"`
	Charging bool `json:"charging"`
	AC       bool `json:"ac"`
}

// UnknownBattery is what the status says before the first reading: no
// battery, nothing known.
var UnknownBattery = BatteryInfo{Percent: -1}

// DecodeBattery turns the raw structure into the block.
//
//   - BatteryFlag 128 is "no system battery" — a desktop.
//   - BatteryFlag 255 is "unknown": some firmware reports it for a battery it
//     cannot read, some virtual machines for none at all. A known percentage
//     is taken as the sign that a battery is there.
//   - Bit 8 is "charging", only meaningful when the flag is not unknown.
//   - AC is ACLineStatus 1; 0 (offline) and 255 (unknown) read as false.
func DecodeBattery(s PowerStatus) BatteryInfo {
	out := BatteryInfo{Percent: -1, AC: s.ACLineStatus == acLineOnline}
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

// BatteryMonitor keeps the newest reading. Last and Known may be set
// before the monitor is in use (a fixture that starts from a reading);
// after that only Poll writes them.
type BatteryMonitor struct {
	mu    sync.Mutex
	Last  BatteryInfo
	Known bool

	// Read is the query: GetSystemPowerStatus, or a test's.
	Read func() (PowerStatus, error)
	// OnChange receives every reading that differs from the one before
	// (never the first). nil: nobody is told.
	OnChange func(BatteryInfo)
}

// NewBatteryMonitor starts from UnknownBattery.
func NewBatteryMonitor(read func() (PowerStatus, error), onChange func(BatteryInfo)) *BatteryMonitor {
	return &BatteryMonitor{Last: UnknownBattery, Read: read, OnChange: onChange}
}

// Poll reads once and reports whether the block changed. The first reading
// only fills the cache: there is nothing to compare it with, and the hub
// gets it with the next status anyway. A failed read keeps the last value.
func (m *BatteryMonitor) Poll() bool {
	raw, err := m.Read()
	if err != nil {
		logx.Printf("Battery: GetSystemPowerStatus failed: %v", err)
		return false
	}
	info := DecodeBattery(raw)
	m.mu.Lock()
	first := !m.Known
	changed := !first && info != m.Last
	m.Last, m.Known = info, true
	onChange := m.OnChange
	m.mu.Unlock()
	if first {
		if info.Present {
			logx.Printf("Battery: %d%%, charging=%v, ac=%v", info.Percent, info.Charging, info.AC)
		}
		return false
	}
	if changed && onChange != nil {
		onChange(info)
	}
	return changed
}

// Info returns the newest reading (UnknownBattery before the first one).
func (m *BatteryMonitor) Info() BatteryInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Last
}
