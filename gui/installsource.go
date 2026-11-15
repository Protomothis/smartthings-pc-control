package gui

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Copies installed by the setup program (#166, installer/*.iss) update
// through the next release's setup, so Windows' app list keeps showing the
// right version; copies installed with the desktop app's [설치] button keep
// swapping the exe (selfupdate.go).
//
// Inno Setup records an installation under the uninstall key named after
// the script's AppId plus "_is1", with InstallLocation set to the folder.
// A copy counts as installed by the setup program only when that folder is
// the running exe's own: a portable copy run from Downloads on a PC that
// also has an installed one must not run the setup.

// installerAppID is AppId in installer/smartthings-pc-control.iss (a test
// keeps the two equal). Never change it: it is how setup finds the
// installation it upgrades.
const installerAppID = "{C936EE42-5942-4E0F-A9D8-FE2E3D01C480}"

// installerUninstallKey is Inno Setup's uninstall key for installerAppID
// (HKLM, 64-bit view: the setup is per-machine and 64-bit).
const installerUninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\` + installerAppID + "_is1"

// installerLocation reads InstallLocation from the setup program's
// uninstall key; false when it is not installed.
func installerLocation() (string, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, installerUninstallKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("InstallLocation")
	if err != nil || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// ownedByInstaller is the pure decision: the setup program's install
// location (Inno writes it with a trailing backslash) is exeDir,
// case-insensitively.
func ownedByInstaller(exeDir, location string) bool {
	norm := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		return strings.TrimRight(filepath.Clean(p), `\/`)
	}
	a, b := norm(exeDir), norm(location)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

// installedByInstaller reports whether the running exe is the copy the
// setup program installed. A variable so tests can pick either answer.
var installedByInstaller = func() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	loc, ok := installerLocation()
	return ok && ownedByInstaller(filepath.Dir(exe), loc)
}

// InstalledByInstaller reports whether this copy was installed by the setup
// program (and so updates through the next release's setup).
func InstalledByInstaller() bool { return installedByInstaller() }
