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
		{SchedulePick{Hours: ScheduleMaxHours, Mins: 5}, "schedule.pick.toolong"},
	} {
		if _, bad := tc.p.Minutes(pickNow); bad != tc.want {
			t.Errorf("%+v: %q, want %q", tc.p, bad, tc.want)
		}
	}
	if m, bad := (SchedulePick{Hours: 168}).Minutes(pickNow); m != ScheduleMaxMinutes || bad != "" {
		t.Errorf("168h = %d %q", m, bad)
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
	// The day after at 23:00: 48h13m.
	p = SchedulePick{At: true, Day: 2, Hour: 23}
	if m, bad := p.Minutes(pickNow); m != 48*60+13 || bad != "" {
		t.Errorf("day after 23:00 = %d %q", m, bad)
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

// The last day of the strip at its last minute is still inside the week.
func TestSchedulePickWeek(t *testing.T) {
	p := SchedulePick{At: true, Day: ScheduleDays - 1, Hour: 23, Minute: 55}
	m, bad := p.Minutes(pickNow)
	if bad != "" || m != 6*24*60+68 { // 11-23 23:55 from 11-17 22:47:20
		t.Errorf("day 6 23:55 = %d %q", m, bad)
	}
	got, _ := ScheduleSummary(LangKo, p, pickNow)
	if got != "11/23(월) 23:55 · 6일 1시간 뒤" {
		t.Errorf("summary %q", got)
	}
	if got, _ := ScheduleSummary(LangEn, SchedulePick{At: true, Day: 3, Hour: 8}, pickNow); got != "11/20 (Fri) 08:00 · in 2 d 9 h" {
		t.Errorf("en summary %q", got)
	}
}

func TestScheduleDayCell(t *testing.T) {
	for _, c := range []struct {
		l         Lang
		day       int
		top, date string
	}{
		{LangKo, 0, "오늘", "17"}, {LangKo, 1, "내일", "18"}, {LangKo, 2, "목", "19"},
		{LangEn, 6, "Mon", "23"}, {LangEn, 1, "Tomorrow", "18"},
	} {
		if top, date := ScheduleDayCell(c.l, pickNow, c.day); top != c.top || date != c.date {
			t.Errorf("%s day %d = %q %q, want %q %q", c.l, c.day, top, date, c.top, c.date)
		}
	}
	// The month turns: the first shows it.
	if _, date := ScheduleDayCell(LangKo, time.Date(2026, 11, 28, 9, 0, 0, 0, time.Local), 3); date != "12/1" {
		t.Errorf("12/1 cell = %q", date)
	}
}
