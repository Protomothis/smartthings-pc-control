package service

// Keep-awake (#111, docs/design/media-notify.md §12). "잠들지 않기" stops
// Windows from going to sleep on its idle timer for a while — a download, a
// render, a remote session — and lets go on its own when the time is up.
//
// SetThreadExecutionState is a per-thread request: Windows honours it for as
// long as the thread that made it keeps it (or until the thread exits). A Go
// goroutine can hop between OS threads, so one goroutine locked to its thread
// owns the request for the lifetime of the service, and every change is
// handed to it (awakeController.run). The service's own thread in session 0
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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"

	"golang.org/x/sys/windows"
)

const (
	// SetThreadExecutionState flags (WinBase.h).
	esContinuous      uint32 = 0x80000000
	esSystemRequired  uint32 = 0x00000001
	esDisplayRequired uint32 = 0x00000002

	// awakeCheckEvery re-checks the wall clock. The expiry timer runs on
	// the monotonic clock, which on Windows stops while the PC sleeps; a
	// period that ran out during a manual suspend is caught by this tick
	// (and by every status read) instead of an hour late.
	awakeCheckEvery = 30 * time.Second
)

var procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// setThreadExecutionState is the real setter. It must run on the thread that
// is to hold the request — awakeController.run guarantees that.
func setThreadExecutionState(flags uint32) error {
	prev, _, err := procSetThreadExecutionState.Call(uintptr(flags))
	if prev == 0 {
		return fmt.Errorf("SetThreadExecutionState(%#x): %v", flags, err)
	}
	return nil
}

// awakeView is the state as the API reports it. Until is zero while off and
// while on without a time limit.
type awakeView struct {
	On    bool
	Until time.Time
}

// stAwake is the /st/v1/status "awake" block: until is RFC3339, or "" when
// off or on until turned off.
type stAwake struct {
	On    bool   `json:"on"`
	Until string `json:"until"`
}

func (v awakeView) wire() stAwake {
	out := stAwake{On: v.On}
	if v.On && !v.Until.IsZero() {
		out.Until = v.Until.Format(time.RFC3339)
	}
	return out
}

// awakeReq is one change for the owning goroutine.
type awakeReq struct {
	flags uint32
	reply chan error
}

// awakeController is the keep-awake state machine. Everything that touches
// Windows goes through setState on the owner goroutine; the clock, the timer
// and the change hook are fields so the tests can drive expiry by hand.
type awakeController struct {
	mu    sync.Mutex
	on    bool
	until time.Time // zero: until turned off
	// seq identifies the current period; a timer armed for an earlier one
	// does nothing when it fires.
	seq       uint64
	stopTimer func() bool
	// held is the flag set the owner thread holds now; 0 means none.
	held uint32

	now         func() time.Time
	afterFunc   func(time.Duration, func()) func() bool
	setState    func(uint32) error
	keepDisplay func() bool
	onChange    func(awakeView)

	reqs  chan awakeReq
	start sync.Once
}

// newAwakeController builds the controller the service uses.
func newAwakeController() *awakeController {
	return &awakeController{
		now: time.Now,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
		setState:    setThreadExecutionState,
		keepDisplay: func() bool { return getConfig().Awake.KeepDisplay },
		onChange:    emitAwakeChanged,
		reqs:        make(chan awakeReq),
	}
}

// awake is the service's controller; tests swap it (see stubAwake).
var (
	awake   = newAwakeController()
	awakeMu sync.RWMutex
)

func currentAwake() *awakeController {
	awakeMu.RLock()
	defer awakeMu.RUnlock()
	return awake
}

// run owns the execution state. The goroutine is locked to its OS thread
// and never unlocks: should it ever return, the thread exits with it and
// Windows drops the request — the failure mode is "may sleep", never
// "stays awake forever".
func (c *awakeController) run() {
	runtime.LockOSThread()
	for req := range c.reqs {
		req.reply <- c.setState(req.flags)
	}
}

// apply hands flags to the owner thread and waits for the result. The
// caller holds c.mu, which keeps changes in order.
func (c *awakeController) apply(flags uint32) error {
	c.start.Do(func() { go c.run() })
	reply := make(chan error, 1)
	c.reqs <- awakeReq{flags: flags, reply: reply}
	return <-reply
}

// wantFlags is the request for the "on" state under the live config.
func (c *awakeController) wantFlags() uint32 {
	flags := esContinuous | esSystemRequired
	if c.keepDisplay != nil && c.keepDisplay() {
		flags |= esDisplayRequired
	}
	return flags
}

// holdLocked makes the owner thread hold flags (no call when it already
// does). c.mu is held.
func (c *awakeController) holdLocked(flags uint32) error {
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
func (c *awakeController) releaseLocked() error {
	if c.held == 0 {
		return nil
	}
	if err := c.apply(esContinuous); err != nil {
		return err
	}
	c.held = 0
	return nil
}

func (c *awakeController) stopTimerLocked() {
	if c.stopTimer != nil {
		c.stopTimer()
		c.stopTimer = nil
	}
}

func (c *awakeController) viewLocked() awakeView {
	return awakeView{On: c.on, Until: c.until}
}

// validAwakeMinutes reports whether minutes is a period TurnOn accepts:
// 0 (until turned off) through config.AwakeMaxMinutes.
func validAwakeMinutes(minutes int) bool {
	return minutes >= 0 && minutes <= config.AwakeMaxMinutes
}

// TurnOn keeps the PC awake for minutes (0: until turned off). Calling it
// while already on starts a new period from now — that is how a period is
// extended or shortened.
func (c *awakeController) TurnOn(minutes int) (awakeView, error) {
	if !validAwakeMinutes(minutes) {
		return awakeView{}, fmt.Errorf("minutes must be between 0 and %d", config.AwakeMaxMinutes)
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
		c.until = c.now().Add(d)
		seq := c.seq
		c.stopTimer = c.afterFunc(d, func() { c.expire(seq) })
	}
	v := c.viewLocked()
	c.mu.Unlock()
	if minutes == 0 {
		logMsg("Keep-awake: on until turned off")
	} else {
		logMsg("Keep-awake: on for %s (until %s)", formatDelay(time.Duration(minutes)*time.Minute), v.Until.Format("15:04"))
	}
	c.changed(v)
	return v, nil
}

// TurnOff lets the PC sleep again; wasOn is false when it already could.
func (c *awakeController) TurnOff() (v awakeView, wasOn bool, err error) {
	c.mu.Lock()
	wasOn = c.on
	err = c.offLocked()
	v = c.viewLocked()
	c.mu.Unlock()
	if wasOn {
		logMsg("Keep-awake: off")
		c.changed(v)
	}
	return v, wasOn, err
}

// offLocked clears the period and the request. The state goes off even if
// Windows refuses the clear, so the API never claims "on" for a request
// the service is no longer tracking. c.mu is held.
func (c *awakeController) offLocked() error {
	c.stopTimerLocked()
	c.seq++
	c.on = false
	c.until = time.Time{}
	return c.releaseLocked()
}

// expire ends the period seq when its timer fires.
func (c *awakeController) expire(seq uint64) {
	c.mu.Lock()
	if !c.on || c.seq != seq {
		c.mu.Unlock()
		return
	}
	if left := c.until.Sub(c.now()); left > 0 {
		// The monotonic timer beat the wall clock (a clock change); wait
		// out the rest.
		c.stopTimer = c.afterFunc(left, func() { c.expire(seq) })
		c.mu.Unlock()
		return
	}
	c.expireLocked()
}

// expireLocked turns a period that ran out off and unlocks c.mu.
func (c *awakeController) expireLocked() {
	if err := c.offLocked(); err != nil {
		logMsg("Keep-awake: clearing the execution state failed: %v", err)
	}
	v := c.viewLocked()
	c.mu.Unlock()
	logMsg("Keep-awake: period ended")
	c.changed(v)
}

// dueLocked reports whether the current period is past its end.
func (c *awakeController) dueLocked() bool {
	return c.on && !c.until.IsZero() && !c.now().Before(c.until)
}

// Tick is the periodic check: a period whose wall-clock end has passed goes
// off, and a keep_display change is applied to a running period.
func (c *awakeController) Tick() {
	c.mu.Lock()
	if c.dueLocked() {
		c.expireLocked()
		return
	}
	if c.on {
		if err := c.holdLocked(c.wantFlags()); err != nil {
			logMsg("Keep-awake: updating the execution state failed: %v", err)
		}
	}
	c.mu.Unlock()
}

// View returns the current state, ending a period that is already over
// (see awakeCheckEvery) so a status read never reports a stale "on".
func (c *awakeController) View() awakeView {
	c.mu.Lock()
	if c.dueLocked() {
		c.expireLocked()
		return awakeView{}
	}
	v := c.viewLocked()
	c.mu.Unlock()
	return v
}

// Shutdown releases the request as the service stops. No event: the hub
// has just been told power.stopping, and the state is not carried over.
func (c *awakeController) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.offLocked(); err != nil {
		logMsg("Keep-awake: clearing the execution state at stop failed: %v", err)
	}
}

func (c *awakeController) changed(v awakeView) {
	if c.onChange != nil {
		c.onChange(v)
	}
}

// emitAwakeChanged is the awake.changed push (§3.5 taps only: device state,
// not a notification).
func emitAwakeChanged(v awakeView) {
	w := v.wire()
	on := "false"
	if w.On {
		on = "true"
	}
	emitDevice("awake", "changed", map[string]string{"on": on, "until": w.Until})
}

// startAwake runs the wall-clock check until stop closes, then releases the
// request. startupHooks calls it.
func startAwake(stop <-chan struct{}) {
	go func() {
		tick := time.NewTicker(awakeCheckEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				currentAwake().Shutdown()
				return
			case <-tick.C:
				currentAwake().Tick()
			}
		}
	}()
}

// awakeMinutesOrDefault resolves an optional period: nil means the
// configured default.
func awakeMinutesOrDefault(minutes *int) int {
	if minutes != nil {
		return *minutes
	}
	return getConfig().Awake.WithDefaults().DefaultMinutes
}

// ---- local API for the desktop app -----------------------------------------

// awakeAPIView is GET/POST/DELETE /api/awake: the state plus what the app's
// command tab needs to draw it. RemainingSeconds is 0 while off and while on
// until turned off.
type awakeAPIView struct {
	Status           string `json:"status"`
	On               bool   `json:"on"`
	Until            string `json:"until"`
	RemainingSeconds int    `json:"remaining_seconds"`
	DefaultMinutes   int    `json:"default_minutes"`
	KeepDisplay      bool   `json:"keep_display"`
}

func awakeAPIBody(v awakeView, cfg Config, now time.Time) awakeAPIView {
	a := cfg.Awake.WithDefaults()
	out := awakeAPIView{
		Status:         "ok",
		On:             v.On,
		Until:          v.wire().Until,
		DefaultMinutes: a.DefaultMinutes,
		KeepDisplay:    a.KeepDisplay,
	}
	if v.On && !v.Until.IsZero() {
		if left := v.Until.Sub(now); left > 0 {
			out.RemainingSeconds = int(left.Round(time.Second) / time.Second)
		}
	}
	return out
}

// handleAwakeAPI serves /api/awake on the WebUI port, behind the same
// session and CSRF checks as /api/schedule:
//
//	GET                         the state
//	POST {"minutes": n}         turn on for n minutes (0 = until turned off,
//	                            key absent = awake.default_minutes)
//	DELETE                      turn off
func handleAwakeAPI(w http.ResponseWriter, r *http.Request) {
	liveCfg := getConfig()
	if !checkAuth(r, liveCfg.Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ctl := currentAwake()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, awakeAPIBody(ctl.View(), liveCfg, ctl.now()))
		return
	case http.MethodPost, http.MethodDelete:
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var (
		view awakeView
		err  error
	)
	if r.Method == http.MethodDelete {
		view, _, err = ctl.TurnOff()
	} else {
		var body struct {
			Minutes *int `json:"minutes"`
		}
		if r.Body != nil {
			if derr := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); derr != nil && derr != io.EOF {
				writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
				return
			}
		}
		minutes := awakeMinutesOrDefault(body.Minutes)
		if !validAwakeMinutes(minutes) {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("Minutes must be between 0 and %d", config.AwakeMaxMinutes))
			return
		}
		view, err = ctl.TurnOn(minutes)
	}
	if err != nil {
		logMsg("Keep-awake via app failed: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "Keep-awake failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, awakeAPIBody(view, getConfig(), ctl.now()))
}
