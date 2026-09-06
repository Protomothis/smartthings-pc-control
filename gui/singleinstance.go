package gui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowTitle is fixed (no version suffix) so a second launch can find the
// existing window by title and bring it to the front.
const windowTitle = "SmartThings PC Control"

// instanceMutex is held for the lifetime of the process.
var instanceMutex windows.Handle

// acquireSingleInstance returns false when another GUI instance is running.
func acquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString("SmartThingsPCControl-GUI")
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		return false
	}
	instanceMutex = h
	return true
}

// focusExistingWindow restores and foregrounds the already-running GUI.
func focusExistingWindow() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	title, _ := syscall.UTF16PtrFromString(windowTitle)
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return
	}
	const swRestore = 9
	user32.NewProc("ShowWindow").Call(hwnd, swRestore)
	user32.NewProc("SetForegroundWindow").Call(hwnd)
}
