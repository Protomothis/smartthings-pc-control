package gui

import (
	"os"
	"path/filepath"
	"strings"
)

// recommendedInstallDir is suggested when the exe sits somewhere volatile.
const recommendedInstallDir = `C:\Program Files\SmartThings PC Control\`

// riskyHomeSubdirs are the per-user folders people download/unpack into and
// later tidy away; the service keeps pointing at the old exe path.
var riskyHomeSubdirs = []string{"Downloads", "Desktop", "Documents"}

// exeInRiskyDir reports the directory of the running exe and whether it is
// a location the service should not be installed from (see isRiskyInstallDir).
// Unknown paths are treated as fine — the warning must never block a
// legitimate install.
func exeInRiskyDir() (exeDir string, risky bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	exeDir = filepath.Dir(exe)
	home, _ := os.UserHomeDir()
	return exeDir, isRiskyInstallDir(exeDir, home, os.TempDir())
}

// isRiskyInstallDir is the pure decision: true when exeDir is the user
// profile root itself, is inside Downloads/Desktop/Documents under home, or
// is inside the temp directory. Comparison is case-insensitive on cleaned
// paths; an empty home or temp disables that rule.
func isRiskyInstallDir(exeDir, home, temp string) bool {
	exeDir = filepath.Clean(exeDir)
	if home != "" {
		home = filepath.Clean(home)
		if strings.EqualFold(exeDir, home) {
			return true
		}
		for _, sub := range riskyHomeSubdirs {
			if pathWithin(exeDir, filepath.Join(home, sub)) {
				return true
			}
		}
	}
	if temp != "" && pathWithin(exeDir, filepath.Clean(temp)) {
		return true
	}
	return false
}

// pathWithin reports whether path equals dir or lies beneath it,
// case-insensitively. Both must already be cleaned.
func pathWithin(path, dir string) bool {
	if strings.EqualFold(path, dir) {
		return true
	}
	prefix := dir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}
