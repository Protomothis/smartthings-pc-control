package service

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// Install folder lockdown (#126). The service runs as SYSTEM from wherever
// the exe was put and reads config.json / state.json next to it; a folder
// made directly under C:\ lets every logged-in user modify all of that.
// The elevated installer and every service start give the folder a
// protected DACL (SYSTEM and Administrators full control, Users read &
// execute). Running at start is what fixes installs made before #126:
// SYSTEM can always rewrite its own folder's permissions.

// installDir is the folder of the running exe, or "" when unknown.
func installDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// lockInstallDir applies the lockdown to dir and describes the outcome in
// one line for the log (or the install console); "" means nothing changed.
func lockInstallDir(dir string) (msg string, warn bool) {
	if dir == "" {
		return "", false
	}
	changed, err := secureacl.LockInstallDir(dir)
	switch {
	case errors.Is(err, secureacl.ErrNotDedicated):
		return "install folder permissions left unchanged: " + err.Error() +
			" — move the exe into its own folder such as C:\\Program Files\\SmartThings PC Control", true
	case err != nil:
		// A partial result is still reported: the root may be fixed even
		// when one file below it could not be reset.
		return "could not fully tighten install folder permissions (" + dir + "): " + err.Error(), true
	case changed:
		return "install folder permissions tightened: " + dir +
			" (SYSTEM/Administrators full control, Users read & execute)", false
	}
	return "", false
}

// secureInstallDirAtStart is the service-start half: it logs only when it
// changed something or could not.
func secureInstallDirAtStart() {
	msg, warn := lockInstallDir(installDir())
	if msg == "" {
		return
	}
	if warn {
		msg = "WARNING: " + msg
	}
	logMsg("%s", msg)
}

// staleOldExeRetries bounds cleanupStaleUpdateFiles: the elevated updater
// runs from <exe>.old and starts this service before it relaunches the
// tray and exits, so the file stays locked for a few seconds.
const staleOldExeRetries = 60

// cleanupStaleUpdateFiles removes what a self-update leaves in the install
// folder: <exe>.old (the previous binary, still mapped by the exiting
// updater) and abandoned *.part downloads in update\ from versions that
// staged there. The tray used to do this, but it can no longer write into
// the locked folder. Best effort; run in a goroutine.
func cleanupStaleUpdateFiles() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	upd := filepath.Join(filepath.Dir(exe), "update")
	parts, _ := filepath.Glob(filepath.Join(upd, "*.part"))
	for _, p := range parts {
		os.Remove(p)
	}
	os.Remove(upd) // only succeeds when empty
	old := exe + ".old"
	for i := 0; i < staleOldExeRetries; i++ {
		if _, err := os.Stat(old); err != nil {
			return
		}
		if os.Remove(old) == nil {
			logMsg("removed %s left by the last update", filepath.Base(old))
			return
		}
		time.Sleep(time.Second)
	}
}
