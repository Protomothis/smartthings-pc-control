package service

// Private files (#131). The install folder lockdown (#126) leaves Users
// read & execute on everything in it, so config.json — the WebUI/API
// secret, among others — was readable by every local account, and the
// tray app read the secret from it on purpose. Now:
//
//   - config.json and state.json carry a DACL of their own, SYSTEM and
//     Administrators only: locked at every service start and written that
//     way on every save (secureacl.WritePrivateFile).
//   - tray.json, next to them and readable by Users, holds the few
//     non-secret settings the tray needs before or without a session: the
//     port (it must find the API first) and the switches its heartbeat
//     follows every 30 s. The service rewrites it with every config save
//     and at start.
//   - the tray gets its session from POST /api/local-login
//     (local_login.go) instead of the secret.
//
// service.log stays readable: the secret is masked there (logx.MaskSecret), the
// bot token never reaches it (telegram's maskErr), and the app's Log tab
// opens the file itself.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// configFileName is the settings file next to the exe.
const configFileName = "config.json"

// trayConfigFileName is the user-readable subset of config.json for the
// tray app (gui/toast.go readLocalConfig).
const trayConfigFileName = "tray.json"

// privateFileNames are the files next to the exe only SYSTEM and
// Administrators may read.
var privateFileNames = []string{configFileName, stateFileName}

// privateFilesOn reports whether this process restricts them: the service
// (LocalSystem) and the elevated installer do. A console run or a test as
// a plain user would lock itself out of its own config, so it writes them
// as before. A var for the tests.
var privateFilesOn = sync.OnceValue(func() bool {
	tok := windows.GetCurrentProcessToken()
	if tok.IsElevated() {
		return true
	}
	u, err := tok.GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
})

// writePrivateFile writes one of privateFileNames. A file that was saved
// but could not be restricted (a FAT drive, say) is logged, not failed:
// losing the user's settings would be worse.
func writePrivateFile(path string, data []byte) error {
	if !privateFilesOn() {
		return os.WriteFile(path, data, 0o644)
	}
	err := secureacl.WritePrivateFile(path, data)
	if errors.Is(err, secureacl.ErrNotPrivate) {
		logMsg("WARNING: %s saved, but other users may still read it: %v", filepath.Base(path), err)
		return nil
	}
	return err
}

// lockPrivateFiles restricts the private files in dir and describes each
// change or failure in one line for the log; nothing when all were
// already private. Missing files are skipped (saved private later).
func lockPrivateFiles(dir string) (msgs []string) {
	if dir == "" || !privateFilesOn() {
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
	for _, m := range lockPrivateFiles(installDir()) {
		logMsg("%s", m)
	}
}

// trayConfig is tray.json: the non-secret settings the tray reads without a
// session. The JSON shape is config.json's, so the tray decodes both alike.
type trayConfig struct {
	Port        int `json:"port"`
	SmartThings struct {
		ExposeSession bool `json:"expose_session"`
	} `json:"smartthings"`
	Media struct {
		Enabled    bool `json:"enabled"`
		NowPlaying bool `json:"now_playing"`
	} `json:"media"`
}

// trayConfigFrom picks the tray's settings out of cfg.
func trayConfigFrom(cfg Config) trayConfig {
	var t trayConfig
	t.Port = cfg.Port
	t.SmartThings.ExposeSession = cfg.SmartThings.ExposeSession
	t.Media.Enabled = cfg.Media.Enabled
	t.Media.NowPlaying = cfg.Media.NowPlaying
	return t
}

// writeTrayConfig rewrites tray.json in dir for cfg. Unlike config.json it
// inherits the folder's "Users: read". Best effort: a failure is logged,
// and the tray then falls back to the default port and its switches' off
// state until the next save or start.
func writeTrayConfig(dir string, cfg Config) {
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(trayConfigFrom(cfg), "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, trayConfigFileName), data, 0o644); err != nil {
		logMsg("WARNING: %s not written: %v", trayConfigFileName, err)
	}
}
