package gui

import (
	"errors"
	"sync"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"golang.org/x/sys/windows"
)

// windowTitle is fixed (no version suffix): windowOnScreen finds the
// window by it, and so did a second launch before the activation event.
const windowTitle = "SmartThings PC Control"

// One GUI per session. A later launch (Start menu, double-click, the
// updater's relaunch) asks the running one to open its window through a
// named auto-reset event, not by looking for the window: a minimized start
// (login autostart, the service waking the tray) creates no native window
// until it is first shown (visibility.go; Fyne only creates it on Show), so
// there is nothing to find. The running instance waits on the event and
// opens the window the way the tray's Open entry does.
const (
	instanceMutexName = "SmartThingsPCControl-GUI"
	activateEventName = "SmartThingsPCControl-GUI-Activate"
)

// guiInstance is this process's claim on the single-instance slot.
type guiInstance struct {
	// first: no other GUI holds the slot; this process is the app.
	first bool
	// activate is the activation event, 0 when it could not be opened.
	activate windows.Handle
	// activateExisted: the event was there before this process asked for
	// it, so a running instance listens on it (one older than the event
	// has none).
	activateExisted bool
}

// claimInstance opens the activation event, then claims the mutex. The
// event comes first so that whoever holds the mutex already has it: a
// launch that loses the mutex and finds no event is up against an older
// version. Neither handle is closed while this process is the app, so both
// live as long as it does.
func claimInstance(mutexName, eventName string) guiInstance {
	var inst guiInstance
	if name, err := windows.UTF16PtrFromString(eventName); err == nil {
		// Auto-reset: one activation per SetEvent, and a signal sent before
		// the waiter runs (the running instance still starting) waits for it.
		h, err := windows.CreateEvent(nil, 0, 0, name)
		if h != 0 {
			inst.activate = h
			inst.activateExisted = errors.Is(err, windows.ERROR_ALREADY_EXISTS)
		}
	}
	name, _ := windows.UTF16PtrFromString(mutexName)
	_, err := windows.CreateMutex(nil, false, name)
	// Access denied: the mutex is there, made by an elevated GUI (the
	// updater's last-resort direct relaunch); still another instance.
	inst.first = !errors.Is(err, windows.ERROR_ALREADY_EXISTS) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)
	return inst
}

// focusExistingWindow is how a launch brings up a running instance older
// than the activation event: find its window by title and restore it. A
// var so the tests do not touch a real app window on the desktop.
var focusExistingWindow = func() {
	title, err := windows.UTF16PtrFromString(windowTitle)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}

// allowForeground lets the running instance take the foreground when it
// opens its window: this launch (started by the user) may, a background
// process asking on its own may not. A var for the tests.
var allowForeground = func() {
	const asfwAny = ^uintptr(0) // ASFW_ANY, (DWORD)-1
	procAllowSetForegroundWindow.Call(asfwAny)
}

// activateRunning asks the running instance to open its window: through
// the activation event, or by its window for an older version.
func activateRunning(inst guiInstance) {
	allowForeground()
	if inst.activate != 0 && inst.activateExisted {
		if windows.SetEvent(inst.activate) == nil {
			return
		}
	}
	focusExistingWindow()
}

// watchActivation calls activate each time a later launch sets ev, until
// the returned stop is called (which waits for the watcher to end).
// activate runs on the watcher goroutine; the caller hands it to the UI.
func watchActivation(ev windows.Handle, activate func()) (stop func()) {
	stopEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil || ev == 0 {
		if stopEv != 0 {
			windows.CloseHandle(stopEv)
		}
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handles := []windows.Handle{ev, stopEv}
		for {
			r, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
			if err != nil || r != windows.WAIT_OBJECT_0 {
				return // stopped (or the wait failed)
			}
			activate()
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			if windows.SetEvent(stopEv) != nil {
				return // the watcher cannot be told; it ends with the process
			}
			<-done
			windows.CloseHandle(stopEv)
		})
	}
}

// restoreIfMinimized un-minimises a window minimised to the taskbar: Show
// only makes a hidden window visible (SW_SHOWNA), which leaves a minimised
// one on the taskbar. A no-op for a window without a native handle (the
// tests' in-memory windows). UI goroutine only.
func restoreIfMinimized(w fyne.Window) {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(func(ctx any) {
		wc, ok := ctx.(driver.WindowsWindowContext)
		if !ok || wc.HWND == 0 {
			return
		}
		if iconic, _, _ := procIsIconic.Call(wc.HWND); iconic != 0 {
			procShowWindow.Call(wc.HWND, swRestore)
		}
	})
}

var (
	procShowWindow               = modUser32.NewProc("ShowWindow")
	procSetForegroundWindow      = modUser32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow = modUser32.NewProc("AllowSetForegroundWindow")
)

const swRestore = 9 // SW_RESTORE
