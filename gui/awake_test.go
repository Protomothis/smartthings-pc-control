package gui

import (
	"testing"
	"time"
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

func TestAwakePresetLabels(t *testing.T) {
	ko := &ui{lang: LangKo}
	want := []string{"30분", "1시간", "2시간", "4시간", "끌 때까지"}
	for i, m := range awakePresets {
		if got := ko.awakePresetLabel(m); got != want[i] {
			t.Errorf("label(%d) = %q, want %q", m, got, want[i])
		}
	}
	if got := (&ui{lang: LangEn}).awakePresetLabel(0); got != "Until turned off" {
		t.Errorf("en label(0) = %q", got)
	}
}

func TestAwakeStatusText(t *testing.T) {
	now := time.Date(2026, 9, 30, 13, 48, 0, 0, time.Local)
	ko := &ui{lang: LangKo}
	en := &ui{lang: LangEn}
	until := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	for _, tc := range []struct {
		u    *ui
		a    Awake
		want string
	}{
		{ko, Awake{}, "꺼짐 — 평소 절전 설정을 따릅니다"},
		{ko, Awake{On: true}, "켜짐 · 끌 때까지"},
		{ko, Awake{On: true, Until: until(42 * time.Minute), RemainingSeconds: 42 * 60}, "42분 남음 · 14:30까지"},
		// Seconds round up, never "0분".
		{ko, Awake{On: true, Until: until(20 * time.Second), RemainingSeconds: 20}, "1분 남음 · 13:48까지"},
		{ko, Awake{On: true, Until: until(90*time.Minute + 30*time.Second), RemainingSeconds: 90*60 + 30}, "1시간 31분 남음 · 15:18까지"},
		{ko, Awake{On: true, Until: until(12 * time.Hour), RemainingSeconds: 12 * 3600}, "12시간 남음 · 내일 01:48까지"},
		{en, Awake{On: true, Until: until(2 * time.Hour), RemainingSeconds: 7200}, "2 h left · until 15:48"},
		{en, Awake{On: true}, "On · until turned off"},
	} {
		if got := tc.u.awakeStatusText(tc.a, now); got != tc.want {
			t.Errorf("%s %+v = %q, want %q", tc.u.lang, tc.a, got, tc.want)
		}
	}
}
