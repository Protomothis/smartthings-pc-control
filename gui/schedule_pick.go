package gui

import (
	"fmt"
	"time"
)

// The home 예약 card picks when a schedule runs with steppers rather than
// a wall of delay chips: either a length of time from now (hours and
// minutes) or a clock time today, tomorrow or the day after. SchedulePick
// is that choice; Minutes turns it into the delay /api/schedule takes.

// ScheduleMaxMinutes is the longest delay the service accepts (#89).
const ScheduleMaxMinutes = 3 * 24 * 60

// ScheduleMinuteStep is how far one tap of a minute stepper moves.
const ScheduleMinuteStep = 5

// ScheduleDays are the days the clock-time mode offers: today, tomorrow,
// the day after (the three days a schedule may reach).
const ScheduleDays = 3

// SchedulePick is the card's choice.
type SchedulePick struct {
	At bool // a clock time (Day, Hour, Minute) rather than a length (Hours, Minutes)

	Hours, Mins int // the length: 0…72 hours, 0…55 minutes

	Day          int // 0 today, 1 tomorrow, 2 the day after
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
// or it is beyond three days.
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
	y, mo, d := now.Date()
	return time.Date(y, mo, d+p.Day, p.Hour, p.Minute, 0, 0, now.Location())
}

// StepHours moves the length's hours by delta, within 0…72.
func (p *SchedulePick) StepHours(delta int) {
	p.Hours = clampInt(p.Hours+delta, 0, ScheduleMaxMinutes/60)
}

// StepMins moves the length's minutes by delta steps of five, wrapping
// within the hour.
func (p *SchedulePick) StepMins(delta int) {
	p.Mins = wrapInt(p.Mins+delta*ScheduleMinuteStep, 60)
}

// StepHour moves the clock hour by delta, wrapping round the day.
func (p *SchedulePick) StepHour(delta int) { p.Hour = wrapInt(p.Hour+delta, 24) }

// StepMinute moves the clock minute by delta steps of five, wrapping
// within the hour; a minute off the step (from the default) snaps to it.
func (p *SchedulePick) StepMinute(delta int) {
	m := p.Minute - p.Minute%ScheduleMinuteStep
	if p.Minute%ScheduleMinuteStep != 0 && delta < 0 {
		delta++ // the snap down was the first step
	}
	p.Minute = wrapInt(m+delta*ScheduleMinuteStep, 60)
}

// ScheduleSummary is the line under the pickers: when it runs and how far
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

// dayWord is 오늘 / 내일 / 모레 for at seen from now.
func dayWord(l Lang, at, now time.Time) string {
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
	switch days := int(at.Sub(today).Hours() / 24); {
	case days <= 0:
		return T(l, "schedule.day.0")
	case days == 1:
		return T(l, "schedule.day.1")
	case days == 2:
		return T(l, "schedule.day.2")
	default:
		return at.Format("1/2")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func clampInt(v, lo, hi int) int { return max(lo, min(v, hi)) }

func wrapInt(v, n int) int { return ((v % n) + n) % n }
