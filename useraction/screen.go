package useraction

// The screen handler (#121): `user-action screen <off|on>` broadcasts
// WM_SYSCOMMAND/SC_MONITORPOWER to the top-level windows of the user's
// desktop — the service in session 0 has no desktop to send it on. This
// replaced a PowerShell one-liner that used SendMessage: a broadcast with
// SendMessage waits for every top-level window in turn, so one hung window
// blocked it forever and pinned the service goroutine waiting on it.
//
// SendMessageTimeoutW with SMTO_ABORTIFHUNG skips windows Windows already
// considers hung and gives every other one screenWindowTimeout. Per the
// documentation a broadcast may still take that long for each slow
// window; the service's 3 s budget for one user-action run kills the child
// if it ever adds up to more. The monitor itself reacts as soon as the
// first window's DefWindowProc handles the message, so a broadcast that
// timed out on some window has still done its job.
//
// Turning the screen on with SC_MONITORPOWER -1 (the documented "power
// on") is ignored by many Windows 8+ machines with modern standby or
// DisplayPort monitors; user input is what reliably wakes a display. So
// `screen on` first sends a mouse move of zero — the cursor does not move,
// but it counts as input and resets the idle timer — and then the
// broadcast.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procSendMessageTimeoutW = modUser32.NewProc("SendMessageTimeoutW")

const (
	hwndBroadcast   = 0xFFFF
	wmSysCommand    = 0x0112
	scMonitorPower  = 0xF170
	smtoNormal      = 0x0000
	smtoAbortIfHung = 0x0002

	// SC_MONITORPOWER lParam values.
	monitorPowerOn  = -1
	monitorPowerOff = 2

	// screenWindowTimeout is SendMessageTimeoutW's uTimeout in ms. For a
	// broadcast it applies to each window in turn, so it is kept short: a
	// few slow (not hung) windows at 2 s each would overrun the service's
	// 3 s user-action budget. The monitor reacts to the first window that
	// handles the message; the rest only need to not block.
	screenWindowTimeout = 300

	inputMouse      = 0
	mouseeventfMove = 0x0001
)

// sendMessageTimeout is the SendMessageTimeoutW call; tests replace it. It
// returns the function's result and, when that is 0, the last error.
var sendMessageTimeout = func(hwnd, msg, wParam, lParam uintptr, flags, timeoutMs uint32) (uintptr, error) {
	var result uintptr
	r, _, err := procSendMessageTimeoutW.Call(hwnd, msg, wParam, lParam,
		uintptr(flags), uintptr(timeoutMs), uintptr(unsafe.Pointer(&result)))
	if r != 0 {
		err = nil
	}
	return r, err
}

// mouseInput is MOUSEINPUT.
type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// pointerInput is INPUT with the mouse member, the union's largest, so no
// padding: 28 bytes on 386 and 40 on amd64, like keyboardInput.
type pointerInput struct {
	typ uint32
	mi  mouseInput
}

// sendMouseInput is the SendInput call for mouse events; tests replace it.
var sendMouseInput = func(inputs []pointerInput) (uint32, error) {
	n, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	return uint32(n), err
}

func init() {
	Register(ActionScreen, screenHandler)
}

// broadcastMonitorPower sends SC_MONITORPOWER with lParam to every
// top-level window, bounded per window. A timeout is not a failure (see
// the file comment).
func broadcastMonitorPower(lParam int) error {
	r, err := sendMessageTimeout(hwndBroadcast, wmSysCommand, scMonitorPower, uintptr(lParam),
		smtoAbortIfHung|smtoNormal, screenWindowTimeout)
	// ERROR_TIMEOUT: some window did not answer in time.
	if r == 0 && err != nil && !errors.Is(err, windows.ERROR_TIMEOUT) && !errors.Is(err, windows.ERROR_SUCCESS) {
		return Failed("SendMessageTimeout(SC_MONITORPOWER): %v", err)
	}
	return nil
}

// screenHandler serves `user-action screen <off|on>`:
//
//	{"ok":true,"screen":"off"}
//	{"ok":true,"screen":"on","input":true}
//
// input says whether the wake-up mouse move was accepted; false (input
// blocked, the secure desktop) is not an error since the broadcast follows.
func screenHandler(req Request) (map[string]any, error) {
	switch req.Verb {
	case "off":
		if err := broadcastMonitorPower(monitorPowerOff); err != nil {
			return nil, err
		}
		return map[string]any{"screen": "off"}, nil
	case "on":
		n, _ := sendMouseInput([]pointerInput{{typ: inputMouse, mi: mouseInput{dwFlags: mouseeventfMove}}})
		if err := broadcastMonitorPower(monitorPowerOn); err != nil {
			return nil, err
		}
		return map[string]any{"screen": "on", "input": n == 1}, nil
	}
	return nil, badArgs("screen: %q is not off or on", req.Verb)
}
