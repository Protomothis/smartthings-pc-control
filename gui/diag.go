package gui

// Diagnostic captures (#176). When the CPU of the tray app or the service
// stays high, internal/health saves a CPU profile and a goroutine dump in
// a diag folder of each (file names: health.DiagCPUPattern and
// health.DiagGoroutinesPattern). These are the two folders, for the app's
// own capture and for "send a report" (#175), which packs everything in
// both.

import (
	"os"
	"path/filepath"

	"github.com/Protomothis/smartthings-pc-control/internal/health"
)

// TrayDiagDir is the tray app's diag folder:
// %LOCALAPPDATA%\SmartThings PC Control\diag, next to gui.log (in temp
// when LOCALAPPDATA is unset, like the crash folder).
func TrayDiagDir() string {
	base := userDataDir()
	if base == "" {
		base = filepath.Join(os.TempDir(), userDataDirName)
	}
	return health.DiagDir(base)
}

// ServiceDiagDir is the service's diag folder: diag\ in the install
// folder, which is this exe's. The service writes it as SYSTEM; Users may
// read it. "" when the exe's path is unknown.
func ServiceDiagDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return health.DiagDir(filepath.Dir(exe))
}
