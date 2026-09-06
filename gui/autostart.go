package gui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Login autostart is a per-user Run entry (HKCU, no elevation). The tray
// app must be running for the grace-period toast to appear, so this is on
// by default and re-pointed at the current exe on every GUI start.
const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "SmartThingsPCControl"
	// minimizedFlag starts the app in the tray without showing the window.
	minimizedFlag = "--minimized"
)

func autostartCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`"%s" gui %s`, exe, minimizedFlag), nil
}

// AutostartEnabled reports whether a Run entry exists for this app.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	return err == nil && strings.TrimSpace(v) != ""
}

// SetAutostart writes or removes the Run entry. Writing always uses the
// current exe path, so a moved exe is picked up on the next start.
func SetAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	cmd, err := autostartCommand()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValueName, cmd)
}
