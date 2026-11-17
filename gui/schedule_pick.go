package gui

import (
	"fmt"
	"strings"
	"time"
)

// The home 예약 card picks when a schedule runs with time wheels rather
// than a wall of delay chips: either a length of time from now (hours and
// minutes) or a clock time on one of the next seven days (#172). The
// service runs it at that clock time and keeps it across a restart (#173).
// SchedulePick is that choice; Minutes and When turn it into what
// /api/schedule takes.

// ScheduleMaxMinutes is the longest delay the service accepts (#173).
const ScheduleMaxMinutes = 7 * 24 * 60

// ScheduleMinuteStep is the minute wheels' step.
const ScheduleMinuteStep = 5

// ScheduleDays are the days the clock-time mode offers: today and the six
// after it, none past.
const ScheduleDays = 7

// ScheduleMaxHours is the length's longest hour value.
const ScheduleMaxHours = ScheduleMaxMinutes / 60

// SchedulePick is the card's choice.
type SchedulePick struct {
	At bool // a clock time (Day, Hour, Minute) rather than a length (Hours, Minutes)

	Hours, Mins int // the length: 0…ScheduleMaxHours hours, 0…55 minutes

	Day          int // 0 today … ScheduleDays-1
	Hour, Minute int // the clock time, 24-hour
}

// DefaultSchedulePick is 30 minutes from now; switching to the clock time
// starts at the next full half hour an hour on.
func DefaultSchedulePick(now time.Time) SchedulePick {
	at := now.Add(time.Hour).Truncate(30 * time.Minute)
	if at.Before(now.Add(time.Hour)) {
		at = at.Add(30 * time.Minute)
	}
	day := 0
	if !sameDay(at, now) {
		day = 1
	}
	return SchedulePick{Mins: 30, Day: day, Hour: at.Hour(), Minute: at.Minute()}
}

// Minutes is the delay from now, and the locale key of what is wrong when
// there is none to give: the time has passed, it is under a minute away,
// or it is beyond a week.
func (p SchedulePick) Minutes(now time.Time) (int, string) {
	var m int
	if p.At {
		d := p.When(now).Sub(now)
		if d <= 0 {
			return 0, "schedule.pick.past"
		}
		// Rounded up: 23:30 picked at 23:29:10 is a minute away, not none.
		m = int((d + time.Minute - 1) / time.Minute)
	} else {
		m = p.Hours*60 + p.Mins
	}
	switch {
	case m < 1:
		return 0, "schedule.pick.zero"
	case m > ScheduleMaxMinutes:
		return 0, "schedule.pick.toolong"
	}
	return m, ""
}

// When is the moment the pick names (the clock time on its day, or now
// plus the length).
func (p SchedulePick) When(now time.Time) time.Time {
	if !p.At {
		return now.Add(time.Duration(p.Hours*60+p.Mins) * time.Minute)
	}
	return dayStart(now, p.Day).Add(time.Duration(p.Hour)*time.Hour + time.Duration(p.Minute)*time.Minute)
}

// ScheduleSummary is the line under the wheels: when it runs and how far
// off that is ("오늘 23:30 · 1시간 32분 뒤"), or why it cannot.
func ScheduleSummary(l Lang, p SchedulePick, now time.Time) (string, bool) {
	m, problem := p.Minutes(now)
	if problem != "" {
		return T(l, problem), false
	}
	at := p.When(now)
	if !p.At {
		// The clock time a length lands on, rounded the way it will run.
		at = now.Add(time.Duration(m) * time.Minute)
	}
	return fmt.Sprintf(T(l, "schedule.pick.summary"), dayWord(l, at, now), at.Format("15:04"), FormatMinutes(l, m)), true
}

// ScheduleDayCell is what the date strip shows for day (0 today): a word
// over the day of the month — 오늘 17, 내일 18, 목 19 … — with the month
// added on the first of one ("12/1").
func ScheduleDayCell(l Lang, now time.Time, day int) (top, date string) {
	d := dayStart(now, day)
	switch day {
	case 0, 1:
		top = T(l, fmt.Sprintf("schedule.day.%d", day))
	default:
		top = weekday(l, d)
	}
	date = fmt.Sprint(d.Day())
	if d.Day() == 1 {
		date = d.Format("1/2")
	}
	return top, date
}

// dayWord is 오늘 / 내일 / 모레 for at seen from now, and the date with
// its weekday further on ("11/21(금)").
func dayWord(l Lang, at, now time.Time) string {
	switch days := int(at.Sub(dayStart(now, 0)).Hours() / 24); {
	case days <= 0:
		return T(l, "schedule.day.0")
	case days <= 2:
		return T(l, fmt.Sprintf("schedule.day.%d", days))
	default:
		return fmt.Sprintf(T(l, "schedule.date"), int(at.Month()), at.Day(), weekday(l, at))
	}
}

// weekday is the short weekday name of t ("목", "Thu").
func weekday(l Lang, t time.Time) string {
	names := strings.Split(T(l, "schedule.weekdays"), ",")
	if len(names) != 7 {
		return t.Weekday().String()[:3]
	}
	return names[t.Weekday()]
}

// dayStart is midnight of the day days after now's.
func dayStart(now time.Time, days int) time.Time {
	y, mo, d := now.Date()
	return time.Date(y, mo, d+days, 0, 0, 0, 0, now.Location())
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
