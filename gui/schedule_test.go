package gui

import (
	"testing"
)

// #89: the schedule tab offers the same sixteen delays as the Edge driver's
// list, and the service accepts up to 4320 minutes (maxScheduleMinutes). A
// preset the service would reject is a button that only ever shows an error.
func TestSchedulePresetsMatchTheServiceCeiling(t *testing.T) {
	const maxMinutes = 4320
	want := []int{5, 10, 15, 30, 45, 60, 90, 120, 180, 240, 360, 480, 720, 1440, 2880, 4320}
	if len(schedulePresets) != len(want) {
		t.Fatalf("schedulePresets = %v, want %v", schedulePresets, want)
	}
	for i, m := range want {
		if schedulePresets[i] != m {
			t.Fatalf("schedulePresets = %v, want %v", schedulePresets, want)
		}
	}
	last := 0
	for _, m := range schedulePresets {
		if m <= last {
			t.Errorf("presets must climb: %v", schedulePresets)
		}
		if m > maxMinutes {
			t.Errorf("preset %d is past the service ceiling (%d)", m, maxMinutes)
		}
		last = m
	}
	found := false
	for _, m := range schedulePresets {
		found = found || m == defaultSchedulePreset
	}
	if !found {
		t.Errorf("the preselected delay (%d) is not in the list", defaultSchedulePreset)
	}
}
