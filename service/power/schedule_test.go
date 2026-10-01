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
