package service

// Debug mode (#133). With config.json's debug on, the service records an
// unrecovered panic or fatal error — every goroutine's stack — to a file
// of its own in <install dir>\crash (internal/crashdump) instead of only
// to a stderr nobody reads. The switch follows every save, without a
// restart. The records may name paths and settings, so each is created
// SYSTEM/Administrators-only like config.json; the folder itself inherits
// the install DACL (the names say nothing).

import (
	"path/filepath"
	"sync"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/crashdump"
	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// crashPrefix starts the service's crash file names.
const crashPrefix = "service"

// crashDir is the service's crash folder, next to config.json (so the
// tests keep it in their own folder); "" when that is unknown.
func crashDir() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "crash")
}

// debugMode is whether this process follows the debug setting (only the
// running service does: the installer saves config.json too) and whether
// recording is on now.
var debugMode struct {
	sync.Mutex
	follow bool
	on     bool
}

// startDebugMode makes the service follow the debug setting, starting
// with cfg's. Called once the config is loaded at start.
func startDebugMode(cfg Config) {
	debugMode.Lock()
	debugMode.follow = true
	debugMode.Unlock()
	applyDebugMode(cfg.Debug)
}

// stopDebugMode closes the crash file at a clean stop (removing it while
// it holds nothing but the header) without the "off" line of a switch.
func stopDebugMode() {
	debugMode.Lock()
	defer debugMode.Unlock()
	if debugMode.on {
		crashdump.Disable()
	}
	debugMode.follow, debugMode.on = false, false
}

// applyDebugMode turns crash recording on or off to match a saved config,
// with a line in service.log on each change. A no-op outside the running
// service and when nothing changes; a folder that cannot be used is
// logged and tried again on the next save.
func applyDebugMode(on bool) {
	debugMode.Lock()
	defer debugMode.Unlock()
	if !debugMode.follow || on == debugMode.on {
		return
	}
	dir := crashDir()
	if !on {
		crashdump.Disable()
		debugMode.on = false
		logMsg("[debug] debug mode off: crashes are no longer recorded (%s)", dir)
		return
	}
	opt := crashdump.Options{Version: Version}
	if config.PrivateFilesOn() {
		opt.Create = secureacl.CreatePrivateFile
	}
	path, err := crashdump.Enable(dir, crashPrefix, opt)
	if err != nil {
		logMsg("WARNING: [debug] debug mode is on, but crashes cannot be recorded in %s: %v", dir, err)
		return
	}
	debugMode.on = true
	logMsg("[debug] debug mode on: crashes are recorded in %s (%s)", dir, filepath.Base(path))
	for _, p := range crashdump.Unreported(dir, crashPrefix) {
		logMsg("[debug] previous crash recorded: %s", p)
	}
}
