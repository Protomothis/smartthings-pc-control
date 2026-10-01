package service

// Private files (#131). The install folder lockdown (#126) leaves Users
// read & execute on everything in it, so config.json — the WebUI/API
// secret, among others — was readable by every local account, and the
// tray app read the secret from it on purpose. Now:
//
//   - config.json and state.json carry a DACL of their own, SYSTEM and
//     Administrators only: locked at every service start (here) and written
//     that way on every save (config.WritePrivateFile).
//   - tray.json, next to them and readable by Users, holds the few
//     non-secret settings the tray needs before or without a session
//     (config.WriteTrayFile). The service rewrites it with every config
//     save and at start.
//   - the tray gets its session from POST /api/local-login
//     (local_login.go) instead of the secret.
//
// service.log stays readable: the secret is masked there (logx.MaskSecret),
// the bot token never reaches it (telegram's maskErr), and the app's Log
// tab opens the file itself.

import (
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// privateFileNames are the files next to the exe only SYSTEM and
// Administrators may read.
var privateFileNames = []string{config.FileName, stateFileName}

// lockPrivateFiles restricts the private files in dir and describes each
// change or failure in one line for the log; nothing when all were
// already private. Missing files are skipped (saved private later).
func lockPrivateFiles(dir string) (msgs []string) {
	if dir == "" || !config.PrivateFilesOn() {
		return nil
	}
	for _, name := range privateFileNames {
		changed, err := secureacl.LockFile(filepath.Join(dir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			msgs = append(msgs, "WARNING: could not restrict "+name+" to SYSTEM/Administrators: "+err.Error())
		case changed:
			msgs = append(msgs, name+": access restricted to SYSTEM/Administrators (other users can no longer read it)")
		}
	}
	return msgs
}

// securePrivateFilesAtStart is the service-start half, run right after the
// folder lockdown.
func securePrivateFilesAtStart() {
	for _, m := range lockPrivateFiles(configDir()) {
		logMsg("%s", m)
	}
}
