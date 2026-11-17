package gui

import (
	"testing"
	"time"
)

var pickNow = time.Date(2026, 11, 17, 22, 47, 20, 0, time.Local)

func TestSchedulePickLength(t *testing.T) {
	p := SchedulePick{Hours: 1, Mins: 30}
	if m, bad := p.Minutes(pickNow); m != 90 || bad != "" {
		t.Errorf("1h30 = %d %q", m, bad)
	}
	for _, tc := range []struct {
		p    SchedulePick
		want string
	}{
		{SchedulePick{}, "schedule.pick.zero"},
		{SchedulePick{Hours: 72, Mins: 5}, "schedule.pick.toolong"},
	} {
		if _, bad := tc.p.Minutes(pickNow); bad != tc.want {
			t.Errorf("%+v: %q, want %q", tc.p, bad, tc.want)
		}
	}
	if m, bad := (SchedulePick{Hours: 72}).Minutes(pickNow); m != ScheduleMaxMinutes || bad != "" {
		t.Errorf("72h = %d %q", m, bad)
	}
}

func TestSchedulePickClockTime(t *testing.T) {
	// 23:30 today from 22:47:20: 42m40s, rounded up to 43.
	p := SchedulePick{At: true, Day: 0, Hour: 23, Minute: 30}
	if m, bad := p.Minutes(pickNow); m != 43 || bad != "" {
		t.Errorf("23:30 today = %d %q", m, bad)
	}
	// 07:00 today has passed; tomorrow it is 8h13m away.
	p = SchedulePick{At: true, Hour: 7}
	if _, bad := p.Minutes(pickNow); bad != "schedule.pick.past" {
		t.Errorf("07:00 today: %q", bad)
	}
	p.Day = 1
	if m, _ := p.Minutes(pickNow); m != 8*60+13 {
		t.Errorf("07:00 tomorrow = %d", m)
	}
	// The day after at 23:00 is past three days from 22:47? No: 48h13m.
	p = SchedulePick{At: true, Day: 2, Hour: 23}
	if m, bad := p.Minutes(pickNow); m != 48*60+13 || bad != "" {
		t.Errorf("day after 23:00 = %d %q", m, bad)
	}
}

func TestSchedulePickSteps(t *testing.T) {
	var p SchedulePick
	p.StepHours(-1)
	if p.Hours != 0 {
		t.Errorf("hours below 0: %d", p.Hours)
	}
	p.Hours = 72
	p.StepHours(1)
	if p.Hours != 72 {
		t.Errorf("hours above 72: %d", p.Hours)
	}
	p.Mins = 55
	p.StepMins(1)
	if p.Mins != 0 {
		t.Errorf("55+5 = %d, want 0", p.Mins)
	}
	p.StepMins(-1)
	if p.Mins != 55 {
		t.Errorf("0-5 = %d, want 55", p.Mins)
	}
	p.Hour = 23
	p.StepHour(1)
	if p.Hour != 0 {
		t.Errorf("23+1 = %d", p.Hour)
	}
	// Off-step minutes snap: 47 → 50 up, 45 down.
	p.Minute = 47
	p.StepMinute(1)
	if p.Minute != 50 {
		t.Errorf("47 up = %d", p.Minute)
	}
	p.Minute = 47
	p.StepMinute(-1)
	if p.Minute != 45 {
		t.Errorf("47 down = %d", p.Minute)
	}
}

func TestDefaultSchedulePick(t *testing.T) {
	p := DefaultSchedulePick(pickNow) // 22:47 → an hour on, the next half hour: 00:00 tomorrow
	if p.At || p.Mins != 30 || p.Hours != 0 {
		t.Errorf("length = %+v", p)
	}
	if p.Day != 1 || p.Hour != 0 || p.Minute != 0 {
		t.Errorf("clock = day %d %02d:%02d", p.Day, p.Hour, p.Minute)
	}
	p = DefaultSchedulePick(time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local))
	if p.Day != 0 || p.Hour != 10 || p.Minute != 0 {
		t.Errorf("from 09:00: day %d %02d:%02d", p.Day, p.Hour, p.Minute)
	}
}

func TestScheduleSummary(t *testing.T) {
	got, ok := ScheduleSummary(LangKo, SchedulePick{At: true, Day: 1, Hour: 7}, pickNow)
	if !ok || got != "내일 07:00 · 8시간 13분 뒤" {
		t.Errorf("ko = %q %v", got, ok)
	}
	got, ok = ScheduleSummary(LangEn, SchedulePick{Mins: 30}, pickNow)
	if !ok || got != "Today 23:17 · in 30 min" {
		t.Errorf("en = %q %v", got, ok)
	}
	if got, ok := ScheduleSummary(LangKo, SchedulePick{At: true, Hour: 7}, pickNow); ok || got != "이미 지난 시각이에요" {
		t.Errorf("past = %q %v", got, ok)
	}
}
