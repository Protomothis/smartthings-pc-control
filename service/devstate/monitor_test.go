package devstate

import (
	"errors"
	"testing"
)

// fakeBattery is a monitor fed from a slice, recording the changes it
// reports.
type fakeBattery struct {
	readings []PowerStatus
	err      error
	changes  []BatteryInfo
}

func (f *fakeBattery) monitor() *BatteryMonitor {
	return &BatteryMonitor{
		Last: UnknownBattery,
		Read: func() (PowerStatus, error) {
			if f.err != nil {
				return PowerStatus{}, f.err
			}
			s := f.readings[0]
			if len(f.readings) > 1 {
				f.readings = f.readings[1:]
			}
			return s, nil
		},
		OnChange: func(b BatteryInfo) { f.changes = append(f.changes, b) },
	}
}

func TestBatteryChangeDetection(t *testing.T) {
	laptop := func(percent byte, charging bool) PowerStatus {
		s := PowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: percent}
		if charging {
			s.ACLineStatus, s.BatteryFlag = 1, 1|8
		}
		return s
	}
	f := &fakeBattery{readings: []PowerStatus{
		laptop(80, false), // first reading: fills the cache, no event
		laptop(80, false), // same: no event
		laptop(79, false), // percent moved
		laptop(79, true),  // plugged in
		laptop(79, true),
	}}
	m := f.monitor()
	if got := m.Info(); got != UnknownBattery {
		t.Errorf("before the first reading = %+v", got)
	}
	var changed []bool
	for i := 0; i < 5; i++ {
		changed = append(changed, m.Poll())
	}
	want := []bool{false, false, true, true, false}
	for i := range want {
		if changed[i] != want[i] {
			t.Errorf("poll %d changed = %v, want %v", i, changed[i], want[i])
		}
	}
	if len(f.changes) != 2 || f.changes[0].Percent != 79 || f.changes[0].Charging || !f.changes[1].Charging {
		t.Errorf("changes = %+v", f.changes)
	}
	if got := m.Info(); got != (BatteryInfo{Present: true, Percent: 79, Charging: true, AC: true}) {
		t.Errorf("info = %+v", got)
	}

	// A failed read keeps the last value and is not a change.
	f.err = errors.New("nope")
	if m.Poll() {
		t.Error("a failed read reported a change")
	}
	if m.Info().Percent != 79 {
		t.Errorf("a failed read lost the last value: %+v", m.Info())
	}
}

// A desktop never changes, so it never pushes.
func TestBatteryDesktopNeverChanges(t *testing.T) {
	f := &fakeBattery{readings: []PowerStatus{{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}}}
	m := f.monitor()
	for i := 0; i < 3; i++ {
		m.Poll()
	}
	if len(f.changes) != 0 {
		t.Errorf("desktop changes = %+v", f.changes)
	}
}
