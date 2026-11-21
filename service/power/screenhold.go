package power

// Screen-off hold (#161). On a modern standby (S0 low power idle) laptop
// the display going off is where standby starts: Windows then works
// through its phases — first waiting for power requests (the "NoCS" phase),
// later suspending desktop apps (the desktop activity moderator) and
// throttling services. Once the tray app is suspended a remote "screen on"
// cannot run in the user session at all, and Windows leaves standby only on
// real input, the power button or the lid.
//
// Microsoft documents that outstanding power requests hold the system in
// that first phase — the display off, everything still running —
// indefinitely on AC power and for at most five minutes after the sleep
// timeout on battery, and that they are dropped when the user starts the
// sleep (power button, lid, Start menu Sleep):
//
//   - learn.microsoft.com/windows-hardware/design/device-experiences/prepare-software-for-modern-standby
//   - learn.microsoft.com/windows/win32/api/winbase/nf-winbase-powersetrequest
//
// So the service's screen off holds a PowerRequestSystemRequired while the
// display is off: the PC behaves like a desktop whose monitor is off, and
// the screen on still finds the tray app running. The hold ends as soon as
// the display is on again, for whatever reason (our screen on, real input,
// opening the lid), which GUID_CONSOLE_DISPLAY_STATE reports. Without that
// watch the hold is not taken: a hold nobody ends would keep a plugged-in
// laptop from ever sleeping on its idle timer.

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// HoldOffTimeout ends a hold whose display never reported off: the screen
// off did not take (a monitor that ignores SC_MONITORPOWER), and a hold
// waiting for a display-on that already happened would never end.
const HoldOffTimeout = time.Minute

// ScreenHoldHooks are the hold's reach into Windows; the tests fake them.
type ScreenHoldHooks struct {
	// Set takes the system-required request; Clear drops it.
	Set   func() error
	Clear func() error
	// Watch reports the console display state (true: on or dimmed) to
	// onChange until stop is called. Windows reports the current state
	// right after registering.
	Watch func(onChange func(on bool)) (stop func(), err error)
	// AfterFunc is time.AfterFunc's shape (HoldOffTimeout).
	AfterFunc func(time.Duration, func()) func() bool
	// Logf writes service.log.
	Logf func(format string, args ...any)
}

// ScreenHold keeps a modern standby laptop out of standby while the
// display is off after a remote screen off.
type ScreenHold struct {
	Hooks ScreenHoldHooks

	mu        sync.Mutex
	held      bool
	sawOff    bool
	seq       uint64
	stopWatch func()
	stopTimer func() bool
}

// NewScreenHold builds a hold on h, filling the real Windows calls in for
// the nil hooks.
func NewScreenHold(h ScreenHoldHooks) *ScreenHold {
	if h.Set == nil || h.Clear == nil {
		r := &powerRequest{reason: "SmartThings PC Control: the display was turned off remotely; keeping the PC awake so it can be turned on again"}
		if h.Set == nil {
			h.Set = r.set
		}
		if h.Clear == nil {
			h.Clear = r.clear
		}
	}
	if h.Watch == nil {
		h.Watch = watchConsoleDisplay
	}
	if h.AfterFunc == nil {
		h.AfterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if h.Logf == nil {
		h.Logf = func(string, ...any) {}
	}
	return &ScreenHold{Hooks: h}
}

// Held reports whether the request is held now.
func (s *ScreenHold) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// Hold takes the request ahead of a screen off. Holding again starts over
// (the display is about to go off again). An error means nothing is held.
//
// The watch starts outside s.mu: Windows may report the current state
// from inside the registration.
func (s *ScreenHold) Hold() error {
	s.mu.Lock()
	s.endLocked()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	stop, err := s.Hooks.Watch(func(on bool) { s.displayChanged(seq, on) })
	if err != nil {
		return fmt.Errorf("watching the display state: %w", err)
	}
	if err := s.Hooks.Set(); err != nil {
		stop()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq != seq {
		// A Release or another Hold came in meanwhile; it wins.
		stop()
		if err := s.Hooks.Clear(); err != nil {
			s.Hooks.Logf("Screen-off hold: clearing the power request failed: %v", err)
		}
		return nil
	}
	s.held, s.sawOff, s.stopWatch = true, false, stop
	s.stopTimer = s.Hooks.AfterFunc(HoldOffTimeout, func() { s.offTimeout(seq) })
	return nil
}

// Release drops the request (no-op when none is held); why goes to the log.
func (s *ScreenHold) Release(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++ // a Hold still starting its watch gives up
	if s.held {
		s.Hooks.Logf("Screen-off hold: released (%s)", why)
	}
	s.endLocked()
}

// endLocked stops the watch and the timer and clears the request. s.mu is
// held.
func (s *ScreenHold) endLocked() {
	if s.stopTimer != nil {
		s.stopTimer()
		s.stopTimer = nil
	}
	if s.stopWatch != nil {
		s.stopWatch()
		s.stopWatch = nil
	}
	if s.held {
		if err := s.Hooks.Clear(); err != nil {
			s.Hooks.Logf("Screen-off hold: clearing the power request failed: %v", err)
		}
		s.held = false
	}
	s.sawOff = false
}

// displayChanged follows the console display: off arms the hold, on after
// that ends it. The first report (the display still on, before the screen
// off has taken) is ignored. The real watch calls it from its own
// goroutine, in order, never from inside the Windows notification.
func (s *ScreenHold) displayChanged(seq uint64, on bool) {
	s.mu.Lock()
	if s.seq != seq || !s.held {
		s.mu.Unlock()
		return
	}
	if !on {
		s.sawOff = true
		if s.stopTimer != nil {
			s.stopTimer()
			s.stopTimer = nil
		}
		s.mu.Unlock()
		return
	}
	end := s.sawOff
	s.mu.Unlock()
	if end {
		s.releaseIf(seq, "the display is on again")
	}
}

// offTimeout ends a hold whose display never went off.
func (s *ScreenHold) offTimeout(seq uint64) {
	s.mu.Lock()
	end := s.seq == seq && s.held && !s.sawOff
	s.mu.Unlock()
	if end {
		s.releaseIf(seq, fmt.Sprintf("the display did not report off within %s", HoldOffTimeout))
	}
}

// releaseIf releases the hold seq if it is still the current one.
func (s *ScreenHold) releaseIf(seq uint64, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq != seq || !s.held {
		return
	}
	s.Hooks.Logf("Screen-off hold: released (%s)", why)
	s.endLocked()
}

// ---- Windows -------------------------------------------------------------

var (
	modKernel32                            = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest                 = modKernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest                    = modKernel32.NewProc("PowerSetRequest")
	procPowerClearRequest                  = modKernel32.NewProc("PowerClearRequest")
	procPowerSettingRegisterNotification   = modPowrprof.NewProc("PowerSettingRegisterNotification")
	procPowerSettingUnregisterNotification = modPowrprof.NewProc("PowerSettingUnregisterNotification")
)

const (
	powerRequestContextVersion      = 0
	powerRequestContextSimpleString = 0x1
	powerRequestSystemRequired      = 1

	deviceNotifyCallback  = 2
	pbtPowerSettingChange = 0x8013
)

// guidConsoleDisplayState is GUID_CONSOLE_DISPLAY_STATE
// {6FE69556-704A-47A0-8F24-C28D936FDA47}: 0 off, 1 on, 2 dimmed.
var guidConsoleDisplayState = windows.GUID{
	Data1: 0x6FE69556, Data2: 0x704A, Data3: 0x47A0,
	Data4: [8]byte{0x8F, 0x24, 0xC2, 0x8D, 0x93, 0x6F, 0xDA, 0x47},
}

// reasonContext is REASON_CONTEXT with the simple string member. The
// union's other member (the localized reason) is the larger one; the
// padding keeps the structure at least as large on 386 and amd64.
type reasonContext struct {
	version uint32
	flags   uint32
	reason  *uint16
	_       [3]uintptr
}

// powerRequest is one PowerCreateRequest object, created on first use and
// kept for the life of the process (the handle closes with it, which also
// drops the request).
type powerRequest struct {
	reason string
	mu     sync.Mutex
	h      windows.Handle
}

func (r *powerRequest) handle() (windows.Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.h != 0 {
		return r.h, nil
	}
	reason, err := windows.UTF16PtrFromString(r.reason)
	if err != nil {
		return 0, err
	}
	ctx := reasonContext{version: powerRequestContextVersion, flags: powerRequestContextSimpleString, reason: reason}
	h, _, callErr := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	if windows.Handle(h) == windows.InvalidHandle || h == 0 {
		return 0, fmt.Errorf("PowerCreateRequest: %v", callErr)
	}
	r.h = windows.Handle(h)
	return r.h, nil
}

func (r *powerRequest) set() error {
	h, err := r.handle()
	if err != nil {
		return err
	}
	if ok, _, callErr := procPowerSetRequest.Call(uintptr(h), powerRequestSystemRequired); ok == 0 {
		return fmt.Errorf("PowerSetRequest(SystemRequired): %v", callErr)
	}
	return nil
}

func (r *powerRequest) clear() error {
	h, err := r.handle()
	if err != nil {
		return err
	}
	if ok, _, callErr := procPowerClearRequest.Call(uintptr(h), powerRequestSystemRequired); ok == 0 {
		return fmt.Errorf("PowerClearRequest(SystemRequired): %v", callErr)
	}
	return nil
}

// deviceNotifySubscribeParameters is DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS.
type deviceNotifySubscribeParameters struct {
	callback uintptr
	context  uintptr
}

// powerBroadcastSetting is the head of POWERBROADCAST_SETTING.
type powerBroadcastSetting struct {
	powerSetting windows.GUID
	dataLength   uint32
	data         [4]byte
}

// The one display watch at a time (the hold makes at most one) and its
// callback, created once: syscall callbacks are never freed. The callback
// only queues the state; a goroutine per watch delivers it in order, so
// the Windows notification never waits on the hold's lock (unregistering
// waits for running callbacks).
var (
	displayWatch struct {
		mu     sync.Mutex
		states chan bool
	}
	displayCallbackOnce sync.Once
	displayCallback     uintptr
	displayParams       deviceNotifySubscribeParameters
)

// displayStateCallback is the DEVICE_NOTIFY_CALLBACK_ROUTINE.
func displayStateCallback(_, typ, setting uintptr) uintptr {
	if typ != pbtPowerSettingChange || setting == 0 {
		return 0
	}
	// setting is a pointer Windows passed in; read through it without vet's
	// uintptr-to-pointer warning (the memory is not Go's).
	pbs := *(**powerBroadcastSetting)(unsafe.Pointer(&setting))
	if pbs.powerSetting != guidConsoleDisplayState || pbs.dataLength < 1 {
		return 0
	}
	on := pbs.data[0] != 0
	displayWatch.mu.Lock()
	if displayWatch.states != nil {
		select {
		case displayWatch.states <- on:
		default: // a stalled reader; the hold's timeout covers it
		}
	}
	displayWatch.mu.Unlock()
	return 0
}

// watchConsoleDisplay is ScreenHoldHooks.Watch: PowerSettingRegisterNotification
// for GUID_CONSOLE_DISPLAY_STATE with a callback, which a service may use
// (no window, no service status handle).
func watchConsoleDisplay(onChange func(on bool)) (func(), error) {
	displayCallbackOnce.Do(func() {
		displayCallback = windows.NewCallback(displayStateCallback)
		displayParams = deviceNotifySubscribeParameters{callback: displayCallback}
	})
	states := make(chan bool, 16)
	done := make(chan struct{})
	displayWatch.mu.Lock()
	displayWatch.states = states
	displayWatch.mu.Unlock()
	detach := func() {
		displayWatch.mu.Lock()
		if displayWatch.states == states {
			displayWatch.states = nil
		}
		displayWatch.mu.Unlock()
	}
	var reg uintptr
	r, _, _ := procPowerSettingRegisterNotification.Call(
		uintptr(unsafe.Pointer(&guidConsoleDisplayState)),
		deviceNotifyCallback,
		uintptr(unsafe.Pointer(&displayParams)),
		uintptr(unsafe.Pointer(&reg)),
	)
	if r != 0 {
		detach()
		return nil, fmt.Errorf("PowerSettingRegisterNotification: %v", windows.Errno(r))
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case on := <-states:
				onChange(on)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			detach()
			close(done)
			_, _, _ = procPowerSettingUnregisterNotification.Call(reg)
		})
	}, nil
}
