package power

// Keep-awake (#111, docs/design/media-notify.md §12). "잠들지 않기" stops
// Windows from going to sleep on its idle timer for a while — a download, a
// render, a remote session — and lets go on its own when the time is up.
//
// SetThreadExecutionState is a per-thread request: Windows honours it for as
// long as the thread that made it keeps it (or until the thread exits). A Go
// goroutine can hop between OS threads, so one goroutine locked to its thread
// owns the request for the lifetime of the service, and every change is
// handed to it (Awake.run). The service's own thread in session 0
// is enough: the request is system-wide, no user session is involved.
//
// Only the *idle* timer is held off. An explicit shutdown, restart, suspend
// or hibernate — from SmartThings, Telegram, the app or the Start menu —
// still happens: ES_SYSTEM_REQUIRED does not veto those, and nothing here
// tries to. The display may still turn off unless awake.keep_display is set
// (ES_DISPLAY_REQUIRED).
//
// The state is deliberately not persisted: after a service restart (which
// usually means a reboot) the PC is allowed to sleep again, the safe side.

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/status"

	"golang.org/x/sys/windows"
)

const (
	// SetThreadExecutionState flags (WinBase.h).
	esContinuous      uint32 = 0x80000000
	esSystemRequired  uint32 = 0x00000001
	esDisplayRequired uint32 = 0x00000002

	// AwakeCheckEvery re-checks the wall clock. The expiry timer runs on
	// the monotonic clock, which on Windows stops while the PC sleeps; a
	// period that ran out during a manual suspend is caught by this tick
	// (and by every status read) instead of an hour late.
	AwakeCheckEvery = 30 * time.Second
)

var procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// setThreadExecutionState is the real setter. It must run on the thread that
// is to hold the request — Awake.run guarantees that.
func setThreadExecutionState(flags uint32) error {
	prev, _, err := procSetThreadExecutionState.Call(uintptr(flags))
	if prev == 0 {
		return fmt.Errorf("SetThreadExecutionState(%#x): %v", flags, err)
	}
	return nil
}

// awakeReq is one change for the owning goroutine.
type awakeReq struct {
	flags uint32
	reply chan error
}

// AwakeHooks are the keep-awake controller's reach outside itself: the
// clock, the timer, the SetThreadExecutionState call, the keep_display
// setting and the change observer. NewAwake fills in the real clock, timer
// and setter when they are nil; the tests drive expiry by hand with their
// own.
type AwakeHooks struct {
	Now       func() time.Time
	AfterFunc func(time.Duration, func()) func() bool
	// SetState is called on the owner goroutine only (Awake.run).
	SetState    func(flags uint32) error
	KeepDisplay func() bool
	OnChange    func(status.AwakeView)
}

// Awake is the keep-awake state machine. Everything that touches Windows
// goes through Hooks.SetState on the owner goroutine.
type Awake struct {
	Hooks AwakeHooks

	mu    sync.Mutex
	on    bool
	until time.Time // zero: until turned off
	// seq identifies the current period; a timer armed for an earlier one
	// does nothing when it fires.
	seq       uint64
	stopTimer func() bool
	// held is the flag set the owner thread holds now; 0 means none.
	held uint32

	reqs  chan awakeReq
	start sync.Once
}

// NewAwake builds a controller on h.
func NewAwake(h AwakeHooks) *Awake {
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.AfterFunc == nil {
		h.AfterFunc = func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		}
	}
	if h.SetState == nil {
		h.SetState = setThreadExecutionState
	}
	return &Awake{Hooks: h, reqs: make(chan awakeReq)}
}

// Now is the controller's clock.
func (c *Awake) Now() time.Time { return c.Hooks.Now() }

// run owns the execution state. The goroutine is locked to its OS thread
// and never unlocks: should it ever return, the thread exits with it and
// Windows drops the request — the failure mode is "may sleep", never
// "stays awake forever".
func (c *Awake) run() {
	runtime.LockOSThread()
	for req := range c.reqs {
		req.reply <- c.Hooks.SetState(req.flags)
	}
}

// apply hands flags to the owner thread and waits for the result. The
// caller holds c.mu, which keeps changes in order.
func (c *Awake) apply(flags uint32) error {
	c.start.Do(func() { go c.run() })
	reply := make(chan error, 1)
	c.reqs <- awakeReq{flags: flags, reply: reply}
	return <-reply
}

// wantFlags is the request for the "on" state under the live config.
func (c *Awake) wantFlags() uint32 {
	flags := esContinuous | esSystemRequired
	if c.Hooks.KeepDisplay != nil && c.Hooks.KeepDisplay() {
		flags |= esDisplayRequired
	}
	return flags
}

// holdLocked makes the owner thread hold flags (no call when it already
// does). c.mu is held.
func (c *Awake) holdLocked(flags uint32) error {
	if flags == c.held {
		return nil
	}
	if err := c.apply(flags); err != nil {
		return err
	}
	c.held = flags
	return nil
}

// releaseLocked clears the request (ES_CONTINUOUS alone). c.mu is held.
func (c *Awake) releaseLocked() error {
	if c.held == 0 {
		return nil
	}
	if err := c.apply(esContinuous); err != nil {
		return err
	}
	c.held = 0
	return nil
}

func (c *Awake) stopTimerLocked() {
	if c.stopTimer != nil {
		c.stopTimer()
		c.stopTimer = nil
	}
}

func (c *Awake) viewLocked() status.AwakeView {
	return status.AwakeView{On: c.on, Until: c.until}
}

// TurnOn keeps the PC awake for minutes (0: until turned off). Calling it
// while already on starts a new period from now — that is how a period is
// extended or shortened.
func (c *Awake) TurnOn(minutes int) (status.AwakeView, error) {
	if !config.ValidAwakeMinutes(minutes) {
		return status.AwakeView{}, fmt.Errorf("minutes must be between 0 and %d", config.AwakeMaxMinutes)
	}
	c.mu.Lock()
	if err := c.holdLocked(c.wantFlags()); err != nil {
		v := c.viewLocked()
		c.mu.Unlock()
		return v, err
	}
	c.stopTimerLocked()
	c.seq++
	c.on = true
	c.until = time.Time{}
	if minutes > 0 {
		d := time.Duration(minutes) * time.Minute
		c.until = c.Hooks.Now().Add(d)
		seq := c.seq
		c.stopTimer = c.Hooks.AfterFunc(d, func() { c.expire(seq) })
	}
	v := c.viewLocked()
	c.mu.Unlock()
	if minutes == 0 {
		logx.Printf("Keep-awake: on until turned off")
	} else {
		logx.Printf("Keep-awake: on for %s (until %s)", FormatDelay(time.Duration(minutes)*time.Minute), v.Until.Format("15:04"))
	}
	c.changed(v)
	return v, nil
}

// TurnOff lets the PC sleep again; wasOn is false when it already could.
func (c *Awake) TurnOff() (v status.AwakeView, wasOn bool, err error) {
	c.mu.Lock()
	wasOn = c.on
	err = c.offLocked()
	v = c.viewLocked()
	c.mu.Unlock()
	if wasOn {
		logx.Printf("Keep-awake: off")
		c.changed(v)
	}
	return v, wasOn, err
}

// offLocked clears the period and the request. The state goes off even if
// Windows refuses the clear, so the API never claims "on" for a request
// the service is no longer tracking. c.mu is held.
func (c *Awake) offLocked() error {
	c.stopTimerLocked()
	c.seq++
	c.on = false
	c.until = time.Time{}
	return c.releaseLocked()
}

// expire ends the period seq when its timer fires.
func (c *Awake) expire(seq uint64) {
	c.mu.Lock()
	if !c.on || c.seq != seq {
		c.mu.Unlock()
		return
	}
	if left := c.until.Sub(c.Hooks.Now()); left > 0 {
		// The monotonic timer beat the wall clock (a clock change); wait
		// out the rest.
		c.stopTimer = c.Hooks.AfterFunc(left, func() { c.expire(seq) })
		c.mu.Unlock()
		return
	}
	c.expireLocked()
}

// expireLocked turns a period that ran out off and unlocks c.mu.
func (c *Awake) expireLocked() {
	if err := c.offLocked(); err != nil {
		logx.Printf("Keep-awake: clearing the execution state failed: %v", err)
	}
	v := c.viewLocked()
	c.mu.Unlock()
	logx.Printf("Keep-awake: period ended")
	c.changed(v)
}

// dueLocked reports whether the current period is past its end.
func (c *Awake) dueLocked() bool {
	return c.on && !c.until.IsZero() && !c.Hooks.Now().Before(c.until)
}

// Tick is the periodic check: a period whose wall-clock end has passed goes
// off, and a keep_display change is applied to a running period.
func (c *Awake) Tick() {
	c.mu.Lock()
	if c.dueLocked() {
		c.expireLocked()
		return
	}
	if c.on {
		if err := c.holdLocked(c.wantFlags()); err != nil {
			logx.Printf("Keep-awake: updating the execution state failed: %v", err)
		}
	}
	c.mu.Unlock()
}

// View returns the current state, ending a period that is already over
// (see AwakeCheckEvery) so a status read never reports a stale "on".
func (c *Awake) View() status.AwakeView {
	c.mu.Lock()
	if c.dueLocked() {
		c.expireLocked()
		return status.AwakeView{}
	}
	v := c.viewLocked()
	c.mu.Unlock()
	return v
}

// Shutdown releases the request as the service stops. No event: the hub
// has just been told power.stopping, and the state is not carried over.
func (c *Awake) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.offLocked(); err != nil {
		logx.Printf("Keep-awake: clearing the execution state at stop failed: %v", err)
	}
}

func (c *Awake) changed(v status.AwakeView) {
	if c.Hooks.OnChange != nil {
		c.Hooks.OnChange(v)
	}
}
