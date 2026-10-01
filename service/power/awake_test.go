package power

// Tests for the keep-awake state machine (#111, docs/design/media-notify.md
// §12), moved here with the controller (#127).

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// fakeAwake is the test harness around an awakeController: a settable
// clock, timers that fire only when the test says so, a recorder in place
// of SetThreadExecutionState and one for the change hook.
type fakeAwake struct {
	ctl *Awake

	mu       sync.Mutex
	now      time.Time
	timers   []*fakeTimer
	calls    []uint32
	changes  []status.AwakeView
	failNext error
	display  bool
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func newFakeAwake() *fakeAwake {
	fa := &fakeAwake{now: time.Date(2026, 9, 30, 14, 0, 0, 0, time.Local)}
	fa.ctl = NewAwake(AwakeHooks{
		Now: func() time.Time {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			return fa.now
		},
		AfterFunc: func(d time.Duration, f func()) func() bool {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			t := &fakeTimer{d: d, f: f}
			fa.timers = append(fa.timers, t)
			return func() bool { t.stopped = true; return true }
		},
		SetState: func(flags uint32) error {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			if err := fa.failNext; err != nil {
				fa.failNext = nil
				return err
			}
			fa.calls = append(fa.calls, flags)
			return nil
		},
		KeepDisplay: func() bool {
			fa.mu.Lock()
			defer fa.mu.Unlock()
			return fa.display
		},
		OnChange: func(v status.AwakeView) {
			fa.mu.Lock()
			fa.changes = append(fa.changes, v)
			fa.mu.Unlock()
		},
	})
	return fa
}

func (fa *fakeAwake) advance(d time.Duration) {
	fa.mu.Lock()
	fa.now = fa.now.Add(d)
	fa.mu.Unlock()
}

// fireLast runs the newest timer's callback, as time.AfterFunc would.
func (fa *fakeAwake) fireLast() {
	fa.mu.Lock()
	t := fa.timers[len(fa.timers)-1]
	fa.mu.Unlock()
	t.f()
}

func (fa *fakeAwake) callLog() []uint32 {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]uint32(nil), fa.calls...)
}

func (fa *fakeAwake) changeLog() []status.AwakeView {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]status.AwakeView(nil), fa.changes...)
}

const (
	flagsOn        = esContinuous | esSystemRequired
	flagsOnDisplay = esContinuous | esSystemRequired | esDisplayRequired
)

func equalFlags(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAwakeOnExtendOffAndExpiry(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl

	if v := c.View(); v.On {
		t.Fatalf("starts on: %+v", v)
	}

	// On for an hour: the owner thread holds SYSTEM_REQUIRED.
	v, err := c.TurnOn(60)
	if err != nil {
		t.Fatal(err)
	}
	wantUntil := fa.now.Add(time.Hour)
	if !v.On || !v.Until.Equal(wantUntil) {
		t.Fatalf("TurnOn(60) = %+v, want on until %v", v, wantUntil)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn}) {
		t.Fatalf("setState calls = %#x", got)
	}
	if len(fa.timers) != 1 || fa.timers[0].d != time.Hour {
		t.Fatalf("timers = %+v", fa.timers)
	}

	// Extend: a new period from now, the old timer stopped, no second
	// SetThreadExecutionState (the flags did not change).
	fa.advance(10 * time.Minute)
	v, _ = c.TurnOn(120)
	if !v.Until.Equal(fa.now.Add(2 * time.Hour)) {
		t.Errorf("extend: until = %v", v.Until)
	}
	if !fa.timers[0].stopped {
		t.Error("the first period's timer was not stopped")
	}
	if got := fa.callLog(); len(got) != 1 {
		t.Errorf("extend re-applied the same flags: %#x", got)
	}
	// The stale timer firing late does nothing.
	fa.timers[0].f()
	if !c.View().On {
		t.Fatal("a stale timer turned keep-awake off")
	}

	// Expiry: the current timer fires once its end has passed.
	fa.advance(2 * time.Hour)
	fa.fireLast()
	if v := c.View(); v.On {
		t.Fatalf("still on after expiry: %+v", v)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("expiry did not clear with ES_CONTINUOUS: %#x", got)
	}
	changes := fa.changeLog()
	if len(changes) != 3 || changes[0].On != true || changes[1].On != true || changes[2].On != false {
		t.Errorf("changes = %+v, want on, on (extend), off", changes)
	}

	// Off while off is not a change.
	if _, wasOn, err := c.TurnOff(); wasOn || err != nil {
		t.Errorf("TurnOff while off: wasOn=%v err=%v", wasOn, err)
	}
	if len(fa.changeLog()) != 3 {
		t.Error("TurnOff while off emitted a change")
	}
}

func TestAwakeIndefiniteAndExplicitOff(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl

	v, err := c.TurnOn(0)
	if err != nil || !v.On || !v.Until.IsZero() {
		t.Fatalf("TurnOn(0) = %+v, %v; want on with no end", v, err)
	}
	if len(fa.timers) != 0 {
		t.Errorf("an indefinite period armed a timer: %+v", fa.timers)
	}
	if w := v.Wire(); w.On != true || w.Until != "" {
		t.Errorf("wire = %+v, want {on:true until:\"\"}", w)
	}
	// A day later it is still on.
	fa.advance(48 * time.Hour)
	c.Tick()
	if !c.View().On {
		t.Fatal("an indefinite period ended on its own")
	}

	_, wasOn, err := c.TurnOff()
	if !wasOn || err != nil {
		t.Fatalf("TurnOff: wasOn=%v err=%v", wasOn, err)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("setState calls = %#x", got)
	}
	if w := c.View().Wire(); w.On || w.Until != "" {
		t.Errorf("off wire = %+v", w)
	}
}

// The monotonic timer stops while the PC sleeps; the wall clock does not.
// A period that ran out during a manual suspend ends on the next tick or
// status read, not an hour late.
func TestAwakeWallClockExpiry(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl
	c.TurnOn(30)

	fa.advance(31 * time.Minute) // the timer has not fired
	if v := c.View(); v.On {
		t.Fatalf("View reported a period that is over: %+v", v)
	}
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("setState calls = %#x", got)
	}
	// The timer firing afterwards finds nothing to do.
	fa.fireLast()
	if got := fa.changeLog(); len(got) != 2 {
		t.Errorf("changes = %+v, want on then one off", got)
	}

	c.TurnOn(30)
	fa.advance(45 * time.Minute)
	c.Tick()
	if c.View().On {
		t.Error("Tick did not end an expired period")
	}
}

// A timer that fires before the wall-clock end (the clock was set back)
// re-arms for the rest instead of ending early.
func TestAwakeEarlyTimerRearms(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl
	c.TurnOn(60)
	fa.advance(50 * time.Minute)
	fa.fireLast()
	if !c.View().On {
		t.Fatal("an early timer ended the period")
	}
	if last := fa.timers[len(fa.timers)-1]; last.d != 10*time.Minute {
		t.Errorf("re-armed for %v, want 10m", last.d)
	}
}

func TestAwakeKeepDisplayFlag(t *testing.T) {
	fa := newFakeAwake()
	fa.display = true
	c := fa.ctl
	c.TurnOn(0)
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOnDisplay}) {
		t.Fatalf("keep_display: setState calls = %#x", got)
	}
	// Turning keep_display off in the settings reaches a running period on
	// the next tick.
	fa.mu.Lock()
	fa.display = false
	fa.mu.Unlock()
	c.Tick()
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOnDisplay, flagsOn}) {
		t.Errorf("after the settings change: setState calls = %#x", got)
	}
	c.Tick()
	if got := fa.callLog(); len(got) != 2 {
		t.Errorf("an unchanged tick re-applied: %#x", got)
	}
}

func TestAwakeRejectsBadMinutesAndSetterFailure(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl
	for _, m := range []int{-1, config.AwakeMaxMinutes + 1} {
		if _, err := c.TurnOn(m); err == nil {
			t.Errorf("TurnOn(%d) accepted", m)
		}
	}
	if _, err := c.TurnOn(config.AwakeMaxMinutes); err != nil {
		t.Errorf("TurnOn(%d) rejected: %v", config.AwakeMaxMinutes, err)
	}
	c.TurnOff()

	fa.mu.Lock()
	fa.failNext = errors.New("access denied")
	fa.mu.Unlock()
	if _, err := c.TurnOn(30); err == nil {
		t.Fatal("a failing SetThreadExecutionState was not reported")
	}
	if c.View().On {
		t.Error("state says on although Windows refused the request")
	}
}

// Shutdown clears the request without an event; nothing carries over.
func TestAwakeShutdownReleases(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl
	c.TurnOn(60)
	c.Shutdown()
	if got := fa.callLog(); !equalFlags(got, []uint32{flagsOn, esContinuous}) {
		t.Errorf("setState calls = %#x", got)
	}
	if c.View().On {
		t.Error("still on after Shutdown")
	}
	if got := fa.changeLog(); len(got) != 1 {
		t.Errorf("Shutdown emitted a change: %+v", got)
	}
}

// Every SetThreadExecutionState call happens on one goroutine locked to
// its thread; the controller never calls the setter itself.
func TestAwakeSetterRunsOnOwnerThread(t *testing.T) {
	fa := newFakeAwake()
	c := fa.ctl
	var mu sync.Mutex
	owners := map[string]bool{}
	inner := c.Hooks.SetState
	c.Hooks.SetState = func(flags uint32) error {
		mu.Lock()
		owners[goroutineID()] = true
		mu.Unlock()
		return inner(flags)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				c.TurnOn(10)
			} else {
				c.TurnOff()
			}
		}(i)
	}
	wg.Wait()
	c.TurnOn(10)
	c.TurnOff()
	if len(owners) != 1 {
		t.Errorf("setState ran on %d goroutines, want 1", len(owners))
	}
}

// goroutineID parses "goroutine N [" from the stack header.
func goroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	s := strings.TrimPrefix(string(buf), "goroutine ")
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
