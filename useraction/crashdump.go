package useraction

// Debug mode for the user-action runs (#133). A run is short, but it is
// where the WinRT and Core Audio calls happen, and a fatal exception there
// kills the process with nothing but a "failed" line in service.log. With
// tray.json's debug switch on, the run records a crash next to the app's,
// in the user's %LOCALAPPDATA%\SmartThings PC Control\crash\, as
// useraction-<yyyyMMdd-HHmmss>-<pid>.txt; the desktop app announces new
// ones in gui.log. Off, it costs one small file read.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/crashdump"
)

// CrashPrefix starts the user-action runs' crash file names.
const CrashPrefix = "useraction"

// trayFileName is the service's user-readable settings file next to the
// exe (internal/config.TrayFileName; that package imports this one, so the
// name is repeated here).
const trayFileName = "tray.json"

// trayDebug reports tray.json's debug switch in dir; false when the file
// is missing, unreadable or older than the switch.
func trayDebug(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, trayFileName))
	if err != nil {
		return false
	}
	var t struct {
		Debug bool `json:"debug"`
	}
	return json.Unmarshal(data, &t) == nil && t.Debug
}

// userDataDirName is the app's per-user folder under %LOCALAPPDATA%; the
// desktop app's userDataDirName (gui/selfupdate.go), which a gui test
// keeps in step.
const userDataDirName = "SmartThings PC Control"

// CrashDirIn is the per-user crash folder for a %LOCALAPPDATA%, "" when
// that is unknown.
func CrashDirIn(localAppData string) string {
	if localAppData == "" {
		return ""
	}
	return filepath.Join(localAppData, userDataDirName, "crash")
}

// StartCrashDump turns the crash record on for this run when tray.json next
// to the exe says debug, and returns what closes it at a normal exit (the
// file goes again unless something was recorded). Anything that stands in
// the way just leaves it off: the run's own result matters more.
func StartCrashDump(version string) (stop func()) {
	stop = func() {}
	exe, err := os.Executable()
	if err != nil || !trayDebug(filepath.Dir(exe)) {
		return stop
	}
	// The run inherits the service's (SYSTEM's) environment; the user's
	// own block knows where their %LOCALAPPDATA% is.
	dir := CrashDirIn(envValue(userEnviron(), "LOCALAPPDATA"))
	if dir == "" {
		return stop
	}
	if _, err := crashdump.Enable(dir, CrashPrefix, crashdump.Options{Version: version}); err != nil {
		return stop
	}
	return crashdump.Disable
}

// envValue is name's value in an environment list, matched without regard
// to case as Windows does; "" when absent.
func envValue(env []string, name string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
