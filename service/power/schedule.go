// Package power is the timing side of the power commands: the single
// schedule slot every front end shares (the app, the WebUI, SmartThings,
// Telegram and the remote grace period), the hint that tells a later stop
// which command caused it, and the Windows power calls. What a command
// does, and what a schedule's life announces, the service fills in.
package power

import (
	"fmt"
	"sync"
	"time"
)

const (
	// MaxScheduleMinutes is the longest delay a schedule may carry, in
	// minutes: three days (#89). Every front end shares it — the SmartThings
	// driver's `schedule(minutes)` definition, /api/schedule, the WebUI form,
	// the Telegram `/shutdown N` argument and the app's schedule tab — so a
	// delay one of them offers is a delay the others can show and cancel.
	MaxScheduleMinutes = 4320
	// MaxScheduleDelay is the same ceiling as a duration.
	MaxScheduleDelay = MaxScheduleMinutes * time.Minute
)

// Origin records who asked for a schedule. It decides whether the service
// must wake the tray app: a remote (SmartThings) grace schedule needs a
// toast the user can see, while schedules from the app or WebUI already
// come from a UI the user is looking at.
type Origin int

const (
	// OriginUI: created from the desktop app or the browser WebUI.
	OriginUI Origin = iota
	// OriginRemote: a SmartThings command deferred by the grace period.
	OriginRemote
	// OriginTelegram: "/shutdown 30" and friends from the Telegram bot (#61).
	// The user asked from their phone, so no tray toast is needed.
	OriginTelegram
	// OriginSmartThings: an explicit schedule from the Edge driver
	// (POST /st/v1/command with minutes > 0, #67). Like Telegram, the user
	// is acting from their phone, so no tray toast is needed — a grace
	// deferral of an immediate SmartThings command stays OriginRemote,
	// because there the toast is the point.
	OriginSmartThings
)

// WakesTrayApp reports whether a schedule from this origin should launch
// the tray app so the [Run now]/[Cancel] toast appears.
func (o Origin) WakesTrayApp() bool {
	return o == OriginRemote
}

// String is the wire form used by /api/schedule ("ui", "remote",
// "telegram" or "smartthings").
func (o Origin) String() string {
	switch o {
	case OriginRemote:
		return "remote"
	case OriginTelegram:
		return "telegram"
	case OriginSmartThings:
		return "smartthings"
	}
	return "ui"
}

// Replaced is the summary of a schedule that a newer one cancelled.
type Replaced struct {
	Command string `json:"command"`
	Origin  string `json:"origin"`
}

// Task is one schedule as the views and the hooks see it.
type Task struct {
	Command   string
	ExecuteAt time.Time
	// Origin says who created the schedule (app/WebUI vs. a remote grace
	// deferral); the GUI labels the countdown with it (#54).
	Origin Origin
	// Replaced describes the schedule this one displaced, if any, so the
	// UI can tell the user their own timer was overridden.
	Replaced *Replaced
	// Seq identifies this schedule for the lifetime of the process, so the
	// Telegram grace message remembered for it (#62) is never edited on
	// behalf of a later schedule.
	Seq uint64
}

// Hooks are what a schedule's life sets off (notifications, the Telegram
// grace message). Replaced, Created and Ended run with the scheduler's
// lock held, in the order the slot changes, and must not call back into
// the scheduler; Fired runs without it. A nil hook is skipped.
type Hooks struct {
	// Replaced: old is cancelled because command (from origin) takes the
	// slot.
	Replaced func(old Task, command string, origin Origin)
	// Created: t is armed and fires after delay.
	Created func(t Task, delay time.Duration)
	// Fired: t's timer ran out; its command runs right after.
	Fired func(t Task)
	// Ended: t was cancelled on behalf of by; runNow when the caller
	// executes its command itself (/now, the runnow: button).
	Ended func(t Task, by string, runNow bool)
}

// Scheduler is the single schedule slot: an existing schedule is
// cancelled and remembered as Replaced on the new one, so the UI can say
// what was overridden (#54).
type Scheduler struct {
	// Lookup returns what runs command; ok is false for a command the
	// catalogue does not have.
	Lookup func(command string) (run func(), ok bool)
	Hooks  Hooks
	// Log receives one line per change.
	Log func(format string, args ...any)

	mu    sync.Mutex
	task  *Task
	timer *time.Timer
	seq   uint64 // the last Seq handed out
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// Schedule arms the timer for command.
func (s *Scheduler) Schedule(command string, delay time.Duration, origin Origin) error {
	if delay <= 0 {
		return fmt.Errorf("invalid delay: %s", delay)
	}
	// #89: the ceiling every front end shares. The Edge driver, /api/schedule
	// and the Telegram bot all check it before they get here; this is the last
	// guard, so no caller can arm a timer the others could never show.
	if delay > MaxScheduleDelay {
		return fmt.Errorf("delay too long: %s (at most %d minutes)", delay, MaxScheduleMinutes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.Lookup(command)
	if !ok {
		return fmt.Errorf("unknown command: %s", command)
	}

	// Cancel existing schedule
	var replaced *Replaced
	if s.task != nil && s.timer != nil {
		s.timer.Stop()
		old := *s.task
		replaced = &Replaced{Command: old.Command, Origin: old.Origin.String()}
		s.logf("Schedule replaced: %s (%s) -> %s (%s)", old.Command, old.Origin, command, origin)
		if s.Hooks.Replaced != nil {
			s.Hooks.Replaced(old, command, origin)
		}
		s.task, s.timer = nil, nil
	}

	s.seq++
	t := Task{
		Command:   command,
		ExecuteAt: time.Now().Add(delay),
		Origin:    origin,
		Replaced:  replaced,
		Seq:       s.seq,
	}
	s.timer = time.AfterFunc(delay, func() { s.fire(t, run) })
	s.task = &t

	s.logf("Scheduled: %s in %s (at %s, origin %s)", command, FormatDelay(delay), t.ExecuteAt.Format("15:04:05"), origin)
	if s.Hooks.Created != nil {
		s.Hooks.Created(t, delay)
	}
	return nil
}

// fire runs t when its timer ran out, then clears the slot unless a newer
// schedule took it meanwhile.
func (s *Scheduler) fire(t Task, run func()) {
	s.logf("Scheduled command executing: %s", t.Command)
	if s.Hooks.Fired != nil {
		s.Hooks.Fired(t)
	}
	if run != nil {
		run()
	}
	s.mu.Lock()
	if s.task != nil && s.task.Seq == t.Seq {
		s.task, s.timer = nil, nil
	}
	s.mu.Unlock()
}

// Current returns the active schedule; ok is false when the slot is empty.
func (s *Scheduler) Current() (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil {
		return Task{}, false
	}
	return *s.task, true
}

// End stops the timer on behalf of by and clears the slot, returning the
// command it would have run. runNow says whether the caller executes the
// command itself; ok is false when nothing was scheduled. Taking the
// schedule under the lock means the caller never races a concurrent
// cancel or replacement.
func (s *Scheduler) End(by string, runNow bool) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.task == nil {
		return "", false
	}
	s.timer.Stop()
	t := *s.task
	s.logf("Schedule cancelled: %s", t.Command)
	if s.Hooks.Ended != nil {
		s.Hooks.Ended(t, by, runNow)
	}
	s.task, s.timer = nil, nil
	return t.Command, true
}

// ActiveSeq returns the Seq of the current schedule when it runs command on
// behalf of origin; the grace-message hook uses it to tie a sent Telegram
// message to the schedule it announced (#62).
func (s *Scheduler) ActiveSeq(command string, origin Origin) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || s.task.Command != command || s.task.Origin != origin {
		return 0, false
	}
	return s.task.Seq, true
}

// FormatDelay renders a delay for log lines and notifications: whole
// minutes as "5 min", anything shorter (or not a whole minute) as seconds
// ("30 sec"). #89: schedules now reach three days, and "4320 min" is not a
// number anyone reads as three days, so from an hour on it climbs the
// units — "2 h", "1 h 30 min", "1 d", "1 d 3 h".
func FormatDelay(d time.Duration) string {
	if d < time.Minute || d%time.Minute != 0 {
		return fmt.Sprintf("%d sec", int(d/time.Second))
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	case minutes < 1440:
		if rest := minutes % 60; rest != 0 {
			return fmt.Sprintf("%d h %d min", minutes/60, rest)
		}
		return fmt.Sprintf("%d h", minutes/60)
	default:
		// The odd minutes are noise at a day's distance.
		if hours := (minutes % 1440) / 60; hours != 0 {
			return fmt.Sprintf("%d d %d h", minutes/1440, hours)
		}
		return fmt.Sprintf("%d d", minutes/1440)
	}
}
