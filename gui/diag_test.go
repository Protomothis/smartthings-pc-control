package gui

import (
	"os"
	"path/filepath"
	"testing"
)

// The tray's diag folder sits next to gui.log, or in temp without
// LOCALAPPDATA; the service's next to the exe.
func TestDiagDirs(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\u\AppData\Local`)
	if got := TrayDiagDir(); got != `C:\Users\u\AppData\Local\SmartThings PC Control\diag` {
		t.Errorf("TrayDiagDir = %q", got)
	}
	t.Setenv("LOCALAPPDATA", "")
	if got := TrayDiagDir(); got != filepath.Join(os.TempDir(), userDataDirName, "diag") {
		t.Errorf("TrayDiagDir without LOCALAPPDATA = %q", got)
	}
	exe, _ := os.Executable()
	if got := ServiceDiagDir(); got != filepath.Join(filepath.Dir(exe), "diag") {
		t.Errorf("ServiceDiagDir = %q", got)
	}
}
