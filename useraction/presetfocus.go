package useraction

// The "focus" preset (#146): bring an open window to the front — restore
// it when minimized, then make it the foreground window.
//
// Its path names the window one of two ways, and the rule lives here once
// for the subcommand (Parse), config.ValidatePreset and the GUI editor:
//
//	obs64.exe          a program's file name (no folder, ends in .exe,
//	                   any case): a window of that program
//	title:Notepad      "title:" or "제목:" and part of the window title,
//	제목:메모장         compared without regard to case
//
// Only visible, unowned top-level windows with a title count — what the
// taskbar shows. Windows lists them front to back, so of several matches
// the one used most recently wins. A cloaked window (one on another
// virtual desktop, or a suspended Store app's frame) is taken only when no
// uncloaked one matches. Store apps run inside ApplicationFrameHost.exe,
// so they are found by title, not by program name.
//
// Windows lets a process take the foreground only under conditions
// (SetForegroundWindow, "Remarks"); this one was started by the service a
// moment ago and meets none of them at first. It tries, in order: plain
// SetForegroundWindow; again after an empty mouse input, since "the
// calling process received the last input event" is one of the documented
// conditions (an empty mouse event, unlike the Alt-key trick, cannot open
// a menu); and last attaching to the foreground window's input queue
// (AttachThreadInput), which shares its foreground right. A failure says
// so with focus_failed; no match is window_not_found.

import (
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrPresetFocusInvalid: neither a program file name nor title:….
var ErrPresetFocusInvalid = errors.New(`preset: focus takes a program file name such as obs64.exe, or "title:" and part of a window title`)

// PresetFocusTitlePrefixes start a focus path that matches by title.
var PresetFocusTitlePrefixes = []string{"title:", "제목:"}

// focusTarget is a parsed focus path: a lower-case program file name or a
// lower-case title fragment.
type focusTarget struct {
	process string
	title   string
}

// ValidatePresetFocus checks a focus preset's path: nil or
// ErrPresetFocusInvalid.
func ValidatePresetFocus(path string) error {
	_, err := parsePresetFocus(path)
	return err
}

func parsePresetFocus(path string) (focusTarget, error) {
	for _, p := range PresetFocusTitlePrefixes {
		if len(path) >= len(p) && strings.EqualFold(path[:len(p)], p) {
			t := strings.TrimSpace(path[len(p):])
			if t == "" {
				return focusTarget{}, ErrPresetFocusInvalid
			}
			return focusTarget{title: strings.ToLower(t)}, nil
		}
	}
	// The activity watch list's rule for a program (config.ValidateActivityWatch).
	if strings.TrimSpace(path) != path || strings.ContainsAny(path, `\/:*?"<>|`) ||
		len(path) <= 4 || !strings.EqualFold(path[len(path)-4:], ".exe") {
		return focusTarget{}, ErrPresetFocusInvalid
	}
	return focusTarget{process: strings.ToLower(path)}, nil
}

// windowInfo is one candidate window.
type windowInfo struct {
	hwnd    windows.HWND
	title   string
	process string // the program's file name, lower case; "" when unknown
	cloaked bool
}

func (t focusTarget) matches(w windowInfo) bool {
	if t.process != "" {
		return w.process == t.process
	}
	return strings.Contains(strings.ToLower(w.title), t.title)
}

// pickWindow is the first (frontmost) match among ws, an uncloaked one
// before any cloaked one.
func pickWindow(ws []windowInfo, t focusTarget) (windows.HWND, bool) {
	var cloaked windows.HWND
	for _, w := range ws {
		if !t.matches(w) {
			continue
		}
		if !w.cloaked {
			return w.hwnd, true
		}
		if cloaked == 0 {
			cloaked = w.hwnd
		}
	}
	return cloaked, cloaked != 0
}

var (
	procGetWindow            = modUser32.NewProc("GetWindow")
	procGetWindowLongW       = modUser32.NewProc("GetWindowLongW")
	procGetWindowTextW       = modUser32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = modUser32.NewProc("GetWindowTextLengthW")
	procIsIconic             = modUser32.NewProc("IsIconic")
	procShowWindow           = modUser32.NewProc("ShowWindow")
	procSetForegroundWindow  = modUser32.NewProc("SetForegroundWindow")
	procBringWindowToTop     = modUser32.NewProc("BringWindowToTop")
	procAttachThreadInput    = modUser32.NewProc("AttachThreadInput")
)

const (
	gwOwner         = 4
	gwlExStyle      = ^uintptr(19) // -20
	wsExToolWindow  = 0x00000080
	swRestore       = 9
	dwmwaCloaked    = 14
	maxWindowTitle  = 512
	processQueryLtd = windows.PROCESS_QUERY_LIMITED_INFORMATION
)

// listWindows lists the candidate windows front to back. Replaced by the
// tests.
var listWindows = func() ([]windowInfo, error) {
	var out []windowInfo
	names := map[uint32]string{}
	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		if w, ok := candidateWindow(hwnd, names); ok {
			out = append(out, w)
		}
		return 1
	})
	if err := windows.EnumWindows(cb, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// candidateWindow describes hwnd when it is a taskbar-style window: visible,
// unowned, not a tool window, with a title. names caches program names by
// process id.
func candidateWindow(hwnd windows.HWND, names map[uint32]string) (windowInfo, bool) {
	if !windows.IsWindowVisible(hwnd) {
		return windowInfo{}, false
	}
	if owner, _, _ := procGetWindow.Call(uintptr(hwnd), gwOwner); owner != 0 {
		return windowInfo{}, false
	}
	if ex, _, _ := procGetWindowLongW.Call(uintptr(hwnd), gwlExStyle); ex&wsExToolWindow != 0 {
		return windowInfo{}, false
	}
	n, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd))
	if n == 0 {
		return windowInfo{}, false
	}
	buf := make([]uint16, min(int(n), maxWindowTitle)+1)
	got, _, _ := procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if got == 0 {
		return windowInfo{}, false
	}
	w := windowInfo{hwnd: hwnd, title: windows.UTF16ToString(buf[:got])}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil && pid != 0 {
		name, ok := names[pid]
		if !ok {
			name = processFileName(pid)
			names[pid] = name
		}
		w.process = name
	}
	var cloaked uint32
	if windows.DwmGetWindowAttribute(hwnd, dwmwaCloaked, unsafe.Pointer(&cloaked), uint32(unsafe.Sizeof(cloaked))) == nil {
		w.cloaked = cloaked != 0
	}
	return w, true
}

// processFileName is the lower-case file name of pid's image, "" when it
// cannot be read (a protected process).
func processFileName(pid uint32) string {
	h, err := windows.OpenProcess(processQueryLtd, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return strings.ToLower(PresetFileName(windows.UTF16ToString(buf[:size])))
}

// isForeground reports whether hwnd is the foreground window now.
func isForeground(hwnd windows.HWND) bool {
	return windows.GetForegroundWindow() == hwnd
}

func setForeground(hwnd windows.HWND) bool {
	r, _, _ := procSetForegroundWindow.Call(uintptr(hwnd))
	return r != 0 && isForeground(hwnd)
}

// bringToFront restores hwnd if minimized and makes it the foreground
// window (see the file comment for the steps). Replaced by the tests.
var bringToFront = func(hwnd windows.HWND) error {
	runtime.LockOSThread() // AttachThreadInput is per thread
	defer runtime.UnlockOSThread()
	if iconic, _, _ := procIsIconic.Call(uintptr(hwnd)); iconic != 0 {
		procShowWindow.Call(uintptr(hwnd), swRestore)
	}
	if setForeground(hwnd) {
		return nil
	}
	_, _ = sendMouseInput([]pointerInput{{typ: inputMouse}}) // only to qualify as the last input; the retry says whether it helped
	if setForeground(hwnd) {
		return nil
	}
	if fg := windows.GetForegroundWindow(); fg != 0 {
		fgThread, _ := windows.GetWindowThreadProcessId(fg, nil)
		me := windows.GetCurrentThreadId()
		if fgThread != 0 && fgThread != me {
			if r, _, _ := procAttachThreadInput.Call(uintptr(me), uintptr(fgThread), 1); r != 0 {
				procBringWindowToTop.Call(uintptr(hwnd))
				procSetForegroundWindow.Call(uintptr(hwnd))
				procAttachThreadInput.Call(uintptr(me), uintptr(fgThread), 0)
			}
		}
	}
	if isForeground(hwnd) {
		return nil
	}
	return errors.New("SetForegroundWindow was refused (foreground lock)")
}

// focusPresetWindow serves a focus preset. Its errors never name the
// program or the title.
func focusPresetWindow(path string) error {
	t, err := parsePresetFocus(path)
	if err != nil {
		return badArgs("%s", err.Error())
	}
	ws, err := listWindows()
	if err != nil {
		return &Error{Code: CodeFailed, Reason: PresetFocusFailed, Message: "could not bring the window to the front", Detail: "EnumWindows: " + err.Error()}
	}
	hwnd, ok := pickWindow(ws, t)
	if !ok {
		return &Error{Code: CodeFailed, Reason: PresetWindowNotFound, Message: "no matching window is open"}
	}
	if err := bringToFront(hwnd); err != nil {
		return &Error{Code: CodeFailed, Reason: PresetFocusFailed, Message: "could not bring the window to the front", Detail: err.Error()}
	}
	return nil
}
