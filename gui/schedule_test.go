package gui

import (
	"testing"
	"time"
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

func TestFormatMinutesClimbsTheUnits(t *testing.T) {
	ko := &ui{lang: LangKo}
	en := &ui{lang: LangEn}
	cases := []struct {
		minutes      int
		wantKo, want string
	}{
		{5, "5분", "5 min"},
		{45, "45분", "45 min"},
		{60, "1시간", "1 h"},
		{90, "1시간 30분", "1 h 30 min"},
		{720, "12시간", "12 h"},
		{1440, "1일", "1 d"},
		{2880, "2일", "2 d"},
		{4320, "3일", "3 d"},
		{1620, "1일 3시간", "1 d 3 h"},
	}
	for _, tc := range cases {
		if got := ko.formatMinutes(tc.minutes); got != tc.wantKo {
			t.Errorf("ko formatMinutes(%d) = %q, want %q", tc.minutes, got, tc.wantKo)
		}
		if got := en.formatMinutes(tc.minutes); got != tc.want {
			t.Errorf("en formatMinutes(%d) = %q, want %q", tc.minutes, got, tc.want)
		}
	}
}

// The big countdown block: mm:ss under an hour, hh:mm:ss under a day and the
// days in front of it beyond that (#89) — "72:00:00" reads as a stopwatch.
func TestFormatCountdownCarriesTheDays(t *testing.T) {
	ko := &ui{lang: LangKo}
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{-time.Second, "00:00"},
		{90 * time.Second, "01:30"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour, "1:00:00"},
		{3*time.Hour + 15*time.Minute, "3:15:00"},
		{51*time.Hour + 15*time.Minute, "2일 3:15:00"},
		{72 * time.Hour, "3일 0:00:00"},
	}
	for _, tc := range cases {
		if got := ko.formatCountdown(tc.d); got != tc.want {
			t.Errorf("formatCountdown(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := (&ui{lang: LangEn}).formatCountdown(51*time.Hour + 15*time.Minute); got != "2d 3:15:00" {
		t.Errorf("en formatCountdown = %q, want %q", got, "2d 3:15:00")
	}
}
