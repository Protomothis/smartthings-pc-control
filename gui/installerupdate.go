package gui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// Updating a copy the setup program installed (#166).
//
// The tray downloads the release's setup, checks it against the signed
// manifest (export_update.go: ApplyInstallerUpdate), stages it with that
// manifest and launches itself elevated as `update-install "<setup>"
// <guiPid> "<update.json>" "<update.json.sig>"`, then quits. Like
// update-apply (selfupdate.go, #126) the elevated copy trusts none of that:
// it copies the three files into the admin-only <install>\update, verifies
// the signature and the copy's SHA-256/size against the manifest's
// installer entry, waits for the tray to exit and starts the verified setup
// silently. It does not wait for the setup: it runs from the installed exe,
// which the setup replaces. The setup stops the service and every tray,
// installs, runs `install` and starts the tray again (installer/*.iss,
// /RELAUNCH=1). The verified setup stays in <install>\update until the next
// update or the uninstall clears that folder.

// verifiedSetupName is the re-verified setup in <install>\update.
const verifiedSetupName = "SmartThingsPCControl-Setup.verified.exe"

// setupLogName is the setup's own log next to it, for a failed update.
const setupLogName = "setup.log"

// installerAsset is the setup program for this arch.
func installerAsset(m *release.Manifest) (*release.ManifestAsset, error) {
	asset, ok := m.InstallerFor(runtime.GOARCH)
	if !ok {
		return nil, fmt.Errorf("manifest %s has no installer for %s", m.Version, runtime.GOARCH)
	}
	return asset, nil
}

// silentSetupArgs is the command line the verified setup runs with: no
// wizard and no message boxes (their default answers), no reboot, start
// the tray again afterwards (/RELAUNCH, see the .iss) and log to logPath.
func silentSetupArgs(logPath string) []string {
	return []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", "/RELAUNCH=1", "/LOG=" + logPath}
}

// startSetup starts the verified setup and returns without waiting. A
// variable so tests never run anything.
var startSetup = func(setup string, args []string) error {
	cmd := exec.Command(setup, args...)
	cmd.Dir = filepath.Dir(setup)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// InstallUpdate runs the verified setup of a newer release (see the top of
// this file). It must run elevated; current is this, the installed, exe's
// version. On failure nothing was installed: the error is logged and
// shown, and the tray relaunched. On success the setup relaunches it.
func InstallUpdate(a UpdateApplyArgs, current string) error {
	err := installUpdate(a, current)
	if err != nil {
		finishUpdateApply(err)
	}
	return err
}

func installUpdate(a UpdateApplyArgs, current string) error {
	cur, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}
	cur, _ = filepath.Abs(cur)
	return runVerifiedSetup(a, cur, current)
}

// runVerifiedSetup is installUpdate for the installed exe cur.
func runVerifiedSetup(a UpdateApplyArgs, cur, current string) error {
	a.NewExe, _ = filepath.Abs(a.NewExe)
	updateLog("update-install start: setup=%s install=%s waitPid=%d", a.NewExe, filepath.Dir(cur), a.Pid)
	if strings.EqualFold(cur, a.NewExe) {
		return fmt.Errorf("setup path equals the installed exe")
	}

	verified, err := stageVerified(a, cur, current, updatePublicKey, installerAsset, verifiedSetupName)
	if err != nil {
		return err
	}
	// The user-writable staged copies are no longer needed either way.
	for _, p := range []string{a.NewExe, a.Manifest, a.Signature} {
		os.Remove(p)
	}
	os.Remove(filepath.Dir(a.NewExe)) // only succeeds when empty

	if a.Pid > 0 {
		if waitForProcessExit(uint32(a.Pid), 30*time.Second) {
			updateLog("GUI pid %d exited", a.Pid)
		} else {
			updateLog("GUI pid %d still running after 30s — the setup closes it", a.Pid)
		}
	}
	logPath := filepath.Join(filepath.Dir(verified), setupLogName)
	if err := startSetup(verified, silentSetupArgs(logPath)); err != nil {
		os.RemoveAll(filepath.Dir(verified))
		return fmt.Errorf("start setup: %w", err)
	}
	updateLog("started %s silently (log: %s)", verifiedSetupName, logPath)
	return nil
}

// RelaunchTray starts the tray app as the desktop user. The setup program
// runs this (hidden `relaunch-gui`) after installing: it is elevated, and
// the tray must not inherit that.
func RelaunchTray() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	launchGUIUnelevated(exe)
}
