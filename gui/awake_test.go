package gui

import (
	"testing"
)

// The service takes 0–1440 minutes (#111); a preset outside that is a
// toggle that only ever shows an error.
func TestAwakePresetsFitTheService(t *testing.T) {
	for _, m := range awakePresets {
		if m < 0 || m > 1440 {
			t.Errorf("preset %d is outside 0..1440", m)
		}
	}
	if awakePresetIndex(defaultAwakePreset) < 0 {
		t.Errorf("the preselected duration (%d) is not a preset", defaultAwakePreset)
	}
	if awakePresetIndex(0) != len(awakePresets)-1 {
		t.Error("'until turned off' should be the last entry")
	}
	if awakePresetIndex(45) != -1 {
		t.Error("45 is not a preset")
	}
}

// awakePresetIndex is the position of minutes in awakePresets, or -1.
func awakePresetIndex(minutes int) int {
	for i, m := range awakePresets {
		if m == minutes {
			return i
		}
	}
	return -1
}
