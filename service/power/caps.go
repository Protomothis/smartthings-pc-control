package power

// The PC's sleep states (#162): powrprof's GetPwrCapabilities. A laptop
// with modern standby (S0 low power idle, "AoAc" in winnt.h) has no S3,
// and SetSuspendState(FALSE, …) does not put it into modern standby —
// Windows hibernates instead (the reporter's SleepStudy: Hibernate (S4),
// reason "Application API", woken only by the power button). There is no
// documented call that enters modern standby; the display going off is
// what starts it, so the suspend command turns the display off there
// (service/power_api.go).

import (
	"fmt"
	"strings"
	"unsafe"
)

var procGetPwrCapabilities = modPowrprof.NewProc("GetPwrCapabilities")

// batteryReportingScale is BATTERY_REPORTING_SCALE.
type batteryReportingScale struct {
	Granularity uint32
	Capacity    uint32
}

// systemPowerCapabilities is SYSTEM_POWER_CAPABILITIES as winnt.h lays it
// out for Windows 10 and later (NTDDI_WINTHRESHOLD): 30 one-byte fields
// (BOOLEAN, BYTE), then the battery scales and five SYSTEM_POWER_STATE
// enums, 76 bytes in all on 386 and amd64 alike (no pointers). The test
// pins the offsets of the fields read here.
type systemPowerCapabilities struct {
	PowerButtonPresent        byte
	SleepButtonPresent        byte
	LidPresent                byte
	SystemS1                  byte
	SystemS2                  byte
	SystemS3                  byte // offset 5
	SystemS4                  byte // offset 6
	SystemS5                  byte
	HiberFilePresent          byte // offset 8
	FullWake                  byte
	VideoDimPresent           byte
	ApmPresent                byte
	UpsPresent                byte
	ThermalControl            byte
	ProcessorThrottle         byte
	ProcessorMinThrottle      byte
	ProcessorMaxThrottle      byte
	FastSystemS4              byte
	Hiberboot                 byte
	WakeAlarmPresent          byte
	AoAc                      byte // offset 20: S0 low power idle
	DiskSpinDown              byte
	HiberFileType             byte
	AoAcConnectivitySupported byte
	spare3                    [6]byte
	SystemBatteriesPresent    byte
	BatteriesAreShortTerm     byte
	BatteryScale              [3]batteryReportingScale // offset 32
	AcOnLineWake              uint32
	SoftLidWake               uint32
	RtcWake                   uint32
	MinDeviceWakeState        uint32
	DefaultLowLatencyWake     uint32 // offset 72
}

// Capabilities is what the service reads from SYSTEM_POWER_CAPABILITIES.
type Capabilities struct {
	// S3 is SystemS3: classic sleep, what SetSuspendState(FALSE) enters.
	S3 bool
	// S4 is SystemS4 (hibernation supported) and HiberFile
	// HiberFilePresent (hibernation turned on).
	S4, HiberFile bool
	// AoAc is the S0 low power idle model: modern standby.
	AoAc bool
}

// ModernStandby reports whether the PC sleeps in S0 low power idle.
func (c Capabilities) ModernStandby() bool { return c.AoAc }

// String is the start-up log line's text, "modern standby (S0 low power
// idle), no S3, hibernate on".
func (c Capabilities) String() string {
	var parts []string
	if c.AoAc {
		parts = append(parts, "modern standby (S0 low power idle)")
	}
	if c.S3 {
		parts = append(parts, "S3 sleep")
	} else {
		parts = append(parts, "no S3")
	}
	switch {
	case c.S4 && c.HiberFile:
		parts = append(parts, "hibernate on")
	case c.S4:
		parts = append(parts, "hibernate off")
	default:
		parts = append(parts, "no hibernate")
	}
	return strings.Join(parts, ", ")
}

// capabilitiesFrom picks the fields the service uses out of the raw
// structure.
func capabilitiesFrom(raw *systemPowerCapabilities) Capabilities {
	return Capabilities{
		S3:        raw.SystemS3 != 0,
		S4:        raw.SystemS4 != 0,
		HiberFile: raw.HiberFilePresent != 0,
		AoAc:      raw.AoAc != 0,
	}
}

// ReadCapabilities calls GetPwrCapabilities. Any user may: it reads, it
// changes nothing.
func ReadCapabilities() (Capabilities, error) {
	var raw systemPowerCapabilities
	if r, _, err := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&raw))); r == 0 {
		return Capabilities{}, fmt.Errorf("GetPwrCapabilities: %w", err)
	}
	return capabilitiesFrom(&raw), nil
}
