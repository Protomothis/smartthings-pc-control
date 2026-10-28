package power

import (
	"testing"
	"unsafe"
)

// The Go struct must match winnt.h byte for byte: GetPwrCapabilities fills
// sizeof(SYSTEM_POWER_CAPABILITIES) bytes, and a field read one byte off
// would answer the wrong question (#162).
func TestSystemPowerCapabilitiesLayout(t *testing.T) {
	var c systemPowerCapabilities
	if got := unsafe.Sizeof(c); got != 76 {
		t.Errorf("sizeof = %d, want 76", got)
	}
	offsets := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"SystemS3", unsafe.Offsetof(c.SystemS3), 5},
		{"SystemS4", unsafe.Offsetof(c.SystemS4), 6},
		{"HiberFilePresent", unsafe.Offsetof(c.HiberFilePresent), 8},
		{"ProcessorMinThrottle", unsafe.Offsetof(c.ProcessorMinThrottle), 15},
		{"AoAc", unsafe.Offsetof(c.AoAc), 20},
		{"AoAcConnectivitySupported", unsafe.Offsetof(c.AoAcConnectivitySupported), 23},
		{"SystemBatteriesPresent", unsafe.Offsetof(c.SystemBatteriesPresent), 30},
		{"BatteryScale", unsafe.Offsetof(c.BatteryScale), 32},
		{"AcOnLineWake", unsafe.Offsetof(c.AcOnLineWake), 56},
		{"DefaultLowLatencyWake", unsafe.Offsetof(c.DefaultLowLatencyWake), 72},
	}
	for _, o := range offsets {
		if o.got != o.want {
			t.Errorf("offsetof(%s) = %d, want %d", o.name, o.got, o.want)
		}
	}
}

// The fields are read from the bytes GetPwrCapabilities writes: a
// modern standby laptop (AoAc, no S3) and a desktop with S3.
func TestCapabilitiesFromRawBytes(t *testing.T) {
	var raw systemPowerCapabilities
	b := (*[76]byte)(unsafe.Pointer(&raw))
	b[6], b[8], b[20] = 1, 1, 1 // S4, hiberfile, AoAc
	got := capabilitiesFrom(&raw)
	if want := (Capabilities{S4: true, HiberFile: true, AoAc: true}); got != want {
		t.Errorf("modern standby laptop: %+v, want %+v", got, want)
	}
	if !got.ModernStandby() {
		t.Error("ModernStandby() = false for AoAc")
	}
	if s := got.String(); s != "modern standby (S0 low power idle), no S3, hibernate on" {
		t.Errorf("String() = %q", s)
	}

	raw = systemPowerCapabilities{}
	b[5], b[6] = 1, 1 // S3, S4 without a hiberfile
	got = capabilitiesFrom(&raw)
	if got.ModernStandby() || !got.S3 {
		t.Errorf("desktop: %+v", got)
	}
	if s := got.String(); s != "S3 sleep, hibernate off" {
		t.Errorf("String() = %q", s)
	}
	if s := (Capabilities{}).String(); s != "no S3, no hibernate" {
		t.Errorf("String() = %q", s)
	}
}

// The real call works on whatever machine runs the tests and agrees with
// itself; what it reports depends on the hardware.
func TestReadCapabilities(t *testing.T) {
	c, err := ReadCapabilities()
	if err != nil {
		t.Fatalf("ReadCapabilities: %v", err)
	}
	t.Logf("this machine: %s", c)
}
