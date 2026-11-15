package ui

// Windows glue for the theme: the system light/dark setting, the dark
// native title bar and the display language.

import (
	"strings"
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/event"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// systemDark reports whether Windows is set to dark mode for apps.
func systemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

var setWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

// darkTitleBar makes the native title bar match the window's theme.
func darkTitleBar(hwnd uintptr, dark bool) {
	if hwnd == 0 || setWindowAttribute.Find() != nil {
		return
	}
	const useImmersiveDarkMode = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE, Windows 10 20H1+
	var on int32
	if dark {
		on = 1
	}
	setWindowAttribute.Call(hwnd, useImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), unsafe.Sizeof(on))
}

// windowHandle extracts the HWND from Gio's view event.
func windowHandle(e event.Event) (uintptr, bool) {
	v, ok := e.(app.Win32ViewEvent)
	return v.HWND, ok
}

var getUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

// systemLang is the Windows display language as one of ours: Korean for
// ko-*, English for everything else.
func systemLang() gui.Lang {
	if getUserDefaultLocaleName.Find() != nil {
		return gui.LangEn
	}
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	n, _, _ := getUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return gui.LangEn
	}
	if strings.HasPrefix(strings.ToLower(windows.UTF16ToString(buf)), "ko") {
		return gui.LangKo
	}
	return gui.LangEn
}

// restoreWindow un-minimises the window and brings it to the front, as
// the Fyne app's restoreIfMinimized did. Gio's ActionRaise does the same
// on the window's own thread; this runs straight away from whichever
// goroutine asked (the tray, a later launch, a toast button), so the
// foreground right that a later launch hands over with
// AllowSetForegroundWindow is used before it lapses. A no-op without a
// window.
func restoreWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procSetForegroundWindow.Call(hwnd)
}

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const swRestore = 9 // SW_RESTORE
