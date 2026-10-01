package gui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// recommendedInstallDir is suggested when the exe sits somewhere volatile.
const recommendedInstallDir = `C:\Program Files\SmartThings PC Control\`

// riskyHomeSubdirs are the per-user folders people download/unpack into and
// later tidy away; the service keeps pointing at the old exe path.
var riskyHomeSubdirs = []string{"Downloads", "Desktop", "Documents"}

// installDirRisk is why the install button asks before installing from the
// exe's current folder.
type installDirRisk int

const (
	installDirOK       installDirRisk = iota
	installDirVolatile                // Downloads, Desktop, Documents, temp, profile root (isRiskyInstallDir)
	installDirWritable                // ordinary users can modify it; installing locks it down (#126)
	installDirUnsafe                  // ordinary users can modify it and it cannot be locked (drive root, shared folder)
)

// installDirRiskBodyKey is the dialog text for each risk.
var installDirRiskBodyKey = map[installDirRisk]string{
	installDirVolatile: "svc.location.body",
	installDirWritable: "svc.location.acl.body",
	installDirUnsafe:   "svc.location.shared.body",
}

// exeInstallDirRisk reports the directory of the running exe and why it is
// a questionable place to install the service from. Unknown paths and
// unreadable permissions are treated as fine — the warning must never block
// a legitimate install.
func exeInstallDirRisk() (exeDir string, risk installDirRisk) {
	exe, err := os.Executable()
	if err != nil {
		return "", installDirOK
	}
	exeDir = filepath.Dir(exe)
	home, _ := os.UserHomeDir()
	if isRiskyInstallDir(exeDir, home, os.TempDir()) {
		return exeDir, installDirVolatile
	}
	if !nonAdminWritable(exeDir, exe) {
		return exeDir, installDirOK
	}
	return exeDir, classifyWritable(secureacl.Lockable(exeDir) == nil)
}

// classifyWritable is the decision for a folder ordinary users can modify:
// the installer and the service lock a dedicated folder down themselves, a
// shared one has to be left as it is.
func classifyWritable(lockable bool) installDirRisk {
	if lockable {
		return installDirWritable
	}
	return installDirUnsafe
}

// nonAdminWritable is the effective check behind the path heuristic: the
// SYSTEM service runs exe from dir, so if Users, Authenticated Users,
// Everyone or INTERACTIVE may write to either, any logged-in user can get
// code running as SYSTEM. This is what flags a folder made directly under
// C:\ (it inherits "Authenticated Users: Modify"); once the service has
// tightened the folder it no longer does. Read errors count as not
// writable.
func nonAdminWritable(paths ...string) bool {
	for _, p := range paths {
		if w, err := secureacl.NonAdminWritable(p); err == nil && w {
			return true
		}
	}
	return false
}

// isRiskyInstallDir is the pure decision: true when exeDir is the user
// profile root itself, is inside Downloads/Desktop/Documents under home, or
// is inside the temp directory. Comparison is case-insensitive on cleaned
// paths; an empty home or temp disables that rule. Who may write to the
// folder is checked separately (nonAdminWritable).
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
