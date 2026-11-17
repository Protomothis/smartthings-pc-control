package power

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a scheduler whose hooks write one line each.
type recorder struct {
	mu    sync.Mutex
	lines []string
	ran   chan string
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func newRecorder() (*Scheduler, *recorder) {
	r := &recorder{ran: make(chan string, 4)}
	s := &Scheduler{
		Lookup: func(command string) (func(), bool) {
			if command == "nope" {
				return nil, false
			}
			return func() { r.ran <- command }, true
		},
		Hooks: Hooks{
			Replaced: func(old Task, command string, origin Origin) {
				r.add("replaced %s/%s by %s/%s", old.Command, old.Origin, command, origin)
			},
			Created: func(t Task, delay time.Duration) { r.add("created %s/%s %s #%d", t.Command, t.Origin, delay, t.Seq) },
			Fired:   func(t Task) { r.add("fired %s #%d", t.Command, t.Seq) },
			Ended:   func(t Task, by string, runNow bool) { r.add("ended %s by %s now=%v", t.Command, by, runNow) },
		},
	}
	return s, r
}

func TestSchedulerLifecycle(t *testing.T) {
	s, r := newRecorder()
	if err := s.Schedule("shutdown", time.Hour, OriginUI); err != nil {
		t.Fatal(err)
	}
	if err := s.Schedule("restart", time.Hour, OriginRemote); err != nil {
		t.Fatal(err)
	}
	cur, ok := s.Current()
	if !ok || cur.Command != "restart" || cur.Seq != 2 || !reflect.DeepEqual(cur.Replaced, &Replaced{Command: "shutdown", Origin: "ui"}) {
		t.Errorf("current = %+v, %v", cur, ok)
	}
	if seq, ok := s.ActiveSeq("restart", OriginRemote); !ok || seq != 2 {
		t.Errorf("ActiveSeq = %d, %v", seq, ok)
	}
	if _, ok := s.ActiveSeq("restart", OriginUI); ok {
		t.Error("ActiveSeq matched another origin")
	}
	if cmd, ok := s.End("tray", true); !ok || cmd != "restart" {
		t.Errorf("End = %q, %v", cmd, ok)
	}
	if _, ok := s.End("tray", false); ok {
		t.Error("End on an empty slot")
	}
	want := []string{
		"created shutdown/ui 1h0m0s #1",
		"replaced shutdown/ui by restart/remote",
		"created restart/remote 1h0m0s #2",
		"ended restart by tray now=true",
	}
	if got := r.got(); !reflect.DeepEqual(got, want) {
		t.Errorf("hooks:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSchedulerFires(t *testing.T) {
	s, r := newRecorder()
	if err := s.Schedule("lock", 10*time.Millisecond, OriginTelegram); err != nil {
		t.Fatal(err)
	}
	select {
	case cmd := <-r.ran:
		if cmd != "lock" {
			t.Errorf("ran %q", cmd)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the timer never fired")
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := s.Current(); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slot was not cleared after the run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.got(); len(got) != 2 || got[1] != "fired lock #1" {
		t.Errorf("hooks %q", got)
	}
}

func TestSchedulerRefuses(t *testing.T) {
	s, r := newRecorder()
	for _, c := range []struct {
		cmd   string
		delay time.Duration
	}{{"shutdown", 0}, {"shutdown", -time.Second}, {"shutdown", MaxScheduleDelay + time.Minute}, {"nope", time.Minute}} {
		if err := s.Schedule(c.cmd, c.delay, OriginUI); err == nil {
			t.Errorf("Schedule(%s, %s) accepted", c.cmd, c.delay)
		}
	}
	if err := s.Schedule("shutdown", MaxScheduleDelay, OriginUI); err != nil {
		t.Errorf("the ceiling itself refused: %v", err)
	}
	s.End("api", false)
	if len(r.got()) != 2 {
		t.Errorf("hooks %q", r.got())
	}
}

func TestHint(t *testing.T) {
	var h Hint
	now := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { return now }
	if got := h.Reason("unknown"); got != "unknown" {
		t.Errorf("no hint: %q", got)
	}
	h.Note("hibernate")
	h.Note("lock") // not a power command: leaves the hint
	if got := h.Reason("suspend"); got != "hibernate" {
		t.Errorf("hint = %q", got)
	}
	now = now.Add(2*time.Minute + time.Second)
	if got := h.Reason("suspend"); got != "suspend" {
		t.Errorf("stale hint = %q", got)
	}
}

func TestOrigin(t *testing.T) {
	for o, want := range map[Origin]string{OriginUI: "ui", OriginRemote: "remote", OriginTelegram: "telegram", OriginSmartThings: "smartthings"} {
		if o.String() != want || o.WakesTrayApp() != (o == OriginRemote) {
			t.Errorf("%d: %s, wakes %v", o, o, o.WakesTrayApp())
		}
	}
}

// fakeClock drives a Scheduler's clock and timers by hand.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeWait
	saved  []*Task
}

type fakeWait struct {
	d       time.Duration
	f       func()
	stopped bool
}

func newClocked() (*Scheduler, *recorder, *fakeClock) {
	s, r := newRecorder()
	c := &fakeClock{now: time.Date(2026, 11, 17, 22, 0, 0, 0, time.Local)}
	s.Now = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
	s.AfterFunc = func(d time.Duration, f func()) func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		w := &fakeWait{d: d, f: f}
		c.timers = append(c.timers, w)
		return func() bool { w.stopped = true; return true }
	}
	s.Save = func(t *Task) { c.saved = append(c.saved, t) }
	return s, r, c
}

func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// fireLast runs the newest timer, as if its wait were over.
func (c *fakeClock) fireLast() *fakeWait {
	c.mu.Lock()
	w := c.timers[len(c.timers)-1]
	c.mu.Unlock()
	if !w.stopped {
		w.f()
	}
	return w
}

// A long wait is cut into ScheduleCheckEvery pieces, each checking the
// wall clock: a PC that slept through the time runs the schedule at the
// next check, not hours late (#173).
func TestSchedulerWallClock(t *testing.T) {
	s, r, c := newClocked()
	if err := s.Schedule("shutdown", 2*time.Hour, OriginUI); err != nil {
		t.Fatal(err)
	}
	if d := c.timers[0].d; d != ScheduleCheckEvery {
		t.Fatalf("first wait %v, want %v", d, ScheduleCheckEvery)
	}
	// A piece ends early on the wall clock: the wait goes on.
	c.advance(ScheduleCheckEvery)
	c.fireLast()
	if _, ok := s.Current(); !ok || len(c.timers) != 2 {
		t.Fatalf("ran early: timers %d", len(c.timers))
	}
	// The PC sleeps past the time; the next piece finds it due.
	c.advance(3 * time.Hour)
	c.fireLast()
	select {
	case cmd := <-r.ran:
		if cmd != "shutdown" {
			t.Errorf("ran %q", cmd)
		}
	default:
		t.Fatal("a due schedule did not run")
	}
	if _, ok := s.Current(); ok {
		t.Error("slot not cleared")
	}
	if last := c.saved[len(c.saved)-1]; last != nil {
		t.Errorf("saved %+v after the run, want nil", last)
	}
	// The last piece is only as long as the time left.
	s.Schedule("lock", 10*time.Second, OriginUI)
	if d := c.timers[len(c.timers)-1].d; d != 10*time.Second {
		t.Errorf("short wait %v", d)
	}
}

func TestSchedulerScheduleAt(t *testing.T) {
	s, _, c := newClocked()
	at := c.now.Add(6*24*time.Hour + 9*time.Hour + 30*time.Minute)
	if err := s.ScheduleAt("restart", at, OriginUI); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current(); !cur.ExecuteAt.Equal(at) {
		t.Errorf("ExecuteAt %v, want %v", cur.ExecuteAt, at)
	}
	for _, bad := range []time.Time{c.now, c.now.Add(-time.Minute), c.now.Add(MaxScheduleDelay + 2*time.Minute)} {
		if err := s.ScheduleAt("restart", bad, OriginUI); err == nil {
			t.Errorf("ScheduleAt(%v) accepted", bad)
		}
	}
	// A week on the dot, picked a moment before the service read it.
	if err := s.ScheduleAt("restart", c.now.Add(MaxScheduleDelay+30*time.Second), OriginUI); err != nil {
		t.Errorf("a week refused: %v", err)
	}
}

// Save sees every change; a remote grace deferral is never saved.
func TestSchedulerSaves(t *testing.T) {
	s, _, c := newClocked()
	s.Schedule("shutdown", time.Hour, OriginTelegram)
	s.Schedule("restart", time.Minute, OriginRemote)
	s.Schedule("lock", time.Hour, OriginSmartThings)
	s.End("app", false)
	var got []string
	for _, t := range c.saved {
		if t == nil {
			got = append(got, "nil")
			continue
		}
		got = append(got, t.Command+"/"+t.Origin.String())
	}
	want := []string{"shutdown/telegram", "nil", "lock/smartthings", "nil"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("saved %v, want %v", got, want)
	}
}

// Restore arms a saved schedule quietly; one that is past is refused.
func TestSchedulerRestore(t *testing.T) {
	s, r, c := newClocked()
	at := c.now.Add(3 * 24 * time.Hour)
	if err := s.Restore("shutdown", at, OriginTelegram); err != nil {
		t.Fatal(err)
	}
	cur, ok := s.Current()
	if !ok || !cur.ExecuteAt.Equal(at) || cur.Origin != OriginTelegram {
		t.Errorf("current %+v %v", cur, ok)
	}
	if got := r.got(); len(got) != 0 {
		t.Errorf("restore announced: %q", got)
	}
	s.End("api", false)
	if err := s.Restore("shutdown", c.now.Add(-time.Minute), OriginUI); err == nil {
		t.Error("a missed schedule was restored")
	}
	if err := s.Restore("shutdown", c.now, OriginUI); err == nil {
		t.Error("a schedule due this instant was restored")
	}
	if ParseOrigin("telegram") != OriginTelegram || ParseOrigin("smartthings") != OriginSmartThings || ParseOrigin("") != OriginUI || ParseOrigin("remote") != OriginRemote {
		t.Error("ParseOrigin")
	}
}
