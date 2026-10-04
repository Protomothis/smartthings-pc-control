package gui

// Debug mode (#133). The app records a crash — an unrecovered panic or a
// fatal error such as a Windows exception in the GL driver, with every
// goroutine's stack — to %LOCALAPPDATA%\SmartThings PC Control\crash\
// gui-<yyyyMMdd-HHmmss>-<pid>.txt (internal/crashdump) while the service
// config's debug switch is on. It reads the switch from tray.json at start,
// so a crash before the service answers is caught too, and then follows
// every adopted config (onConfig). The user-action runs write their
// useraction-* records into the same folder (useraction/crashdump.go); the
// app announces those as well, since nothing else would.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"fyne.io/fyne/v2/dialog"

	"github.com/Protomothis/smartthings-pc-control/internal/crashdump"
	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// Crash file prefixes in the per-user folder.
const (
	guiCrashPrefix        = "gui"
	userActionCrashPrefix = "useraction"
)

// crashDirIn is the crash folder inside dataDir, or one in temp when
// dataDir is unknown (like guiLogPathIn).
func crashDirIn(dataDir, temp string) string {
	if dataDir == "" {
		return filepath.Join(temp, userDataDirName, "crash")
	}
	return filepath.Join(dataDir, "crash")
}

// guiCrashDir is %LOCALAPPDATA%\SmartThings PC Control\crash, next to
// gui.log.
func guiCrashDir() string { return crashDirIn(userDataDir(), os.TempDir()) }

// serviceCrashDir is where the service keeps its (admin-only) records:
// crash\ in the install folder, which is this exe's.
func serviceCrashDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "crash")
}

// debugState is whether this process follows the debug switch (only the
// app's own Run does, never the tests) and whether recording is on.
var debugState struct {
	sync.Mutex
	follow  bool
	on      bool
	version string
}

// startDebugMode makes the app follow the debug switch, starting with on
// (tray.json's value). Called once by Run.
func startDebugMode(version string, on bool) {
	debugState.Lock()
	debugState.follow, debugState.version = true, version
	debugState.Unlock()
	setDebugMode(on)
}

// stopDebugMode closes the crash file when the app quits normally,
// removing it while it holds only the header.
func stopDebugMode() {
	debugState.Lock()
	defer debugState.Unlock()
	if debugState.on {
		crashdump.Disable()
	}
	debugState.follow, debugState.on = false, false
}

// setDebugMode turns recording on or off, with a line in gui.log on each
// change. Idempotent, cheap when nothing changes, and a no-op until
// startDebugMode. A var so the tests see what the forms ask for.
var setDebugMode = func(on bool) {
	debugState.Lock()
	defer debugState.Unlock()
	if !debugState.follow || on == debugState.on {
		return
	}
	dir := guiCrashDir()
	if !on {
		crashdump.Disable()
		debugState.on = false
		guiLog("debug", "debug mode off: crashes are no longer recorded (%s)", dir)
		return
	}
	path, err := crashdump.Enable(dir, guiCrashPrefix, crashdump.Options{Version: debugState.version})
	if err != nil {
		guiLog("debug", "WARNING: debug mode is on, but crashes cannot be recorded in %q: %v", dir, err)
		return
	}
	debugState.on = true
	guiLog("debug", "debug mode on: crashes are recorded in %s (%s)", dir, filepath.Base(path))
	for _, prefix := range []string{guiCrashPrefix, userActionCrashPrefix} {
		for _, p := range crashdump.Unreported(dir, prefix) {
			guiLog("debug", "previous crash recorded: %s", p)
		}
	}
}

// openCrashFolder opens the per-user crash folder in Explorer, making it
// first when debug mode has not yet.
func (u *ui) openCrashFolder() {
	dir := guiCrashDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	// explorer.exe is a GUI app: no console to hide.
	if err := exec.Command(systool.Explorer(), dir).Start(); err != nil {
		dialog.ShowError(err, u.win)
	}
}
