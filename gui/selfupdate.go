package gui

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// Self update, stage 2 of issue #40.
//
// The GUI downloads and verifies the new exe (update.go), stages it with
// the signed manifest it was checked against, then launches itself
// elevated as `update-apply "<newExe>" <guiPid> "<update.json>"
// "<update.json.sig>"` and quits. The elevated copy (ApplyUpdate) does not
// trust the GUI's check: the staged files sit in the user's own folder, so
// any process of that user could swap them after the GUI looked (#126). It
// copies all three into <install>\update (admin-only), verifies the
// signature with the embedded key and the copied exe's SHA-256/size, and
// only then waits for the GUI to exit, stops the service, renames the
// installed exe to <exe>.old, copies the verified copy over the original
// path, restarts the service and relaunches the GUI. Renaming (not
// deleting) is what makes this work: Windows allows renaming a mapped
// image, and the elevated process itself is running from that same image.
//
// update-apply always runs from the installed exe — the GUI launches its
// own image (runElevatedSelf) — so a tray only ever talks to an updater of
// its own version: an older tray runs the older update-apply. The 2-argument
// form of releases before #126 is therefore refused rather than supported;
// accepting it would install a file nobody re-verified.

// oldExeSuffix marks the previous binary kept for rollback until the
// restarted service removes it (the install folder is locked to
// administrators since #126, so the tray usually cannot).
const oldExeSuffix = ".old"

// updatePublicKey verifies the staged manifest in the elevated updater;
// tests swap in their own key.
var updatePublicKey = release.PublicKey

// lockUpdateDir gives <install>\update the admin-only install DACL before
// anything is copied into it, so the copy cannot be swapped between the
// check and the install even when the install folder itself could not be
// locked. Tests replace it: a non-elevated test run would lock itself out.
var lockUpdateDir = func(dir string) error {
	_, err := secureacl.Apply(dir)
	return err
}

// Limits for the staged manifest files (the release assets are far smaller).
const (
	maxStagedManifest = 64 * 1024
	maxStagedSig      = 1024
)

// verifiedExeName is the name of the re-verified copy in <install>\update.
const verifiedExeName = "smartthings-pc-control.verified.exe"

// UpdateApplyArgs is the parsed command line of the hidden update-apply
// command.
type UpdateApplyArgs struct {
	NewExe    string // staged download (user-writable)
	Pid       int    // GUI to wait for; 0 = don't wait
	Manifest  string // staged update.json
	Signature string // staged update.json.sig
}

// ParseUpdateApplyArgs validates the arguments of the hidden
// `update-apply <newExePath> <guiPid> <manifestPath> <sigPath>` command.
func ParseUpdateApplyArgs(args []string) (UpdateApplyArgs, error) {
	var a UpdateApplyArgs
	if len(args) == 2 {
		return a, errors.New("update-apply: the signed manifest is missing (this updater needs <newExePath> <guiPid> <update.json> <update.json.sig>); restart the app and update again")
	}
	if len(args) != 4 {
		return a, fmt.Errorf("usage: update-apply <newExePath> <guiPid> <update.json> <update.json.sig> (got %d args)", len(args))
	}
	abs := func(what, p string) (string, error) {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", fmt.Errorf("update-apply: empty %s path", what)
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("update-apply: %s path must be absolute: %q", what, p)
		}
		return p, nil
	}
	var err error
	if a.NewExe, err = abs("exe", args[0]); err != nil {
		return UpdateApplyArgs{}, err
	}
	if !strings.EqualFold(filepath.Ext(a.NewExe), ".exe") {
		return UpdateApplyArgs{}, fmt.Errorf("update-apply: not an exe: %q", a.NewExe)
	}
	a.Pid, err = strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || a.Pid < 0 {
		return UpdateApplyArgs{}, fmt.Errorf("update-apply: bad pid %q", args[1])
	}
	if a.Manifest, err = abs("manifest", args[2]); err != nil {
		return UpdateApplyArgs{}, err
	}
	if a.Signature, err = abs("signature", args[3]); err != nil {
		return UpdateApplyArgs{}, err
	}
	return a, nil
}

// ApplyUpdate replaces the running installation with the staged exe once
// it has been re-verified (see the top of this file). It must run elevated
// (the GUI launches it via ShellExecuteEx "runas"); current is the version
// of this, the installed, exe. Every step is appended to gui.log
// (guiLogPath). A failed check leaves the installed exe untouched; a
// failure after the rename puts the old exe back. Either way the GUI is
// relaunched so the user is not left without it, and a message box reports
// the failure.
func ApplyUpdate(a UpdateApplyArgs, current string) error {
	err := applyUpdate(a, current)
	finishUpdateApply(err)
	return err
}

// AbortUpdateApply reports update-apply arguments that did not parse the
// way a failed update is reported — the GUI has already quit, so it must
// not just vanish — and relaunches the GUI.
func AbortUpdateApply(err error) { finishUpdateApply(err) }

// finishUpdateApply logs and shows err (if any) and relaunches the GUI.
func finishUpdateApply(err error) {
	if err != nil {
		updateLog("update failed: %v", err)
		messageBox("SmartThings PC Control", "업데이트 실패 / Update failed:\n\n"+err.Error()+
			"\n\n로그 / Log: "+guiLogPath())
	}
	if cur, cerr := os.Executable(); cerr == nil {
		launchGUIUnelevated(cur)
	}
}

func applyUpdate(a UpdateApplyArgs, current string) error {
	cur, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}
	cur, _ = filepath.Abs(cur)
	a.NewExe, _ = filepath.Abs(a.NewExe)
	updateLog("update-apply start: new=%s target=%s waitPid=%d", a.NewExe, cur, a.Pid)

	if strings.EqualFold(cur, a.NewExe) {
		return errors.New("new exe path equals the installed path")
	}

	// Everything below installs only this re-verified copy, never the
	// user-writable file the GUI staged.
	verified, err := stageVerifiedUpdate(a, cur, current, updatePublicKey)
	if err != nil {
		return err
	}
	updDir := filepath.Dir(verified)
	defer os.RemoveAll(updDir) // also on failure; idempotent

	if a.Pid > 0 {
		if waitForProcessExit(uint32(a.Pid), 30*time.Second) {
			updateLog("GUI pid %d exited", a.Pid)
		} else {
			updateLog("GUI pid %d still running after 30s — continuing anyway", a.Pid)
		}
	}

	wasRunning := queryServiceState() == svcRunning
	if wasRunning {
		updateLog("stopping service %s", serviceName)
		if err := stopServiceAndWait(30 * time.Second); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
		updateLog("service stopped")
	}

	if err := swapExe(verified, cur); err != nil {
		if wasRunning {
			startService()
		}
		return err
	}
	// The verified copy and the GUI's staged files are no longer needed.
	// <install>\update goes before the service starts, so its start-up
	// permission check never races the delete.
	os.RemoveAll(updDir)
	for _, p := range []string{a.NewExe, a.Manifest, a.Signature} {
		os.Remove(p)
	}
	os.Remove(filepath.Dir(a.NewExe)) // only succeeds when empty

	if wasRunning {
		if err := startService(); err != nil {
			// Not fatal for the update itself; the GUI shows service state
			// and offers Start.
			updateLog("service start failed: %v", err)
		} else {
			updateLog("service started")
		}
	}
	updateLog("update-apply done")
	return nil
}

// stageVerifiedUpdate copies the GUI's staged exe, manifest and signature
// into a fresh, admin-only <install>\update and verifies the copies: the
// manifest signature against pub, that the manifest is newer than current
// and allows updating from it, and the copied exe's SHA-256 and size
// against the manifest asset for this arch. It returns the path of the
// verified exe. On any failure the folder is removed again and nothing
// else has been touched; a hash mismatch wraps errHashMismatch, a bad
// signature release.ErrBadSignature.
func stageVerifiedUpdate(a UpdateApplyArgs, cur, current string, pub ed25519.PublicKey) (verified string, err error) {
	updDir := filepath.Join(filepath.Dir(cur), "update")
	if err := prepareUpdateDir(updDir); err != nil {
		return "", fmt.Errorf("prepare %s: %w", updDir, err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(updDir)
		}
	}()

	manPath := filepath.Join(updDir, release.ManifestName)
	sigPath := filepath.Join(updDir, release.ManifestSigName)
	verified = filepath.Join(updDir, verifiedExeName)
	if err := copyLimited(a.Manifest, manPath, maxStagedManifest); err != nil {
		return "", fmt.Errorf("copy manifest: %w", err)
	}
	if err := copyLimited(a.Signature, sigPath, maxStagedSig); err != nil {
		return "", fmt.Errorf("copy signature: %w", err)
	}
	if err := copyFile(a.NewExe, verified); err != nil {
		return "", fmt.Errorf("copy new exe: %w", err)
	}
	updateLog("copied staged files into %s", updDir)

	data, err := os.ReadFile(manPath)
	if err != nil {
		return "", err
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return "", err
	}
	m, err := release.VerifyManifest(data, sig, pub)
	if err != nil {
		return "", err
	}
	if !release.IsNewer(current, m.Version) {
		return "", fmt.Errorf("manifest is for %s, which is not newer than the installed %s", m.Version, current)
	}
	if !m.Allows(current) {
		return "", fmt.Errorf("%s cannot be updated to directly from %s (min_version %s)", m.Version, current, m.MinVersion)
	}
	asset, ok := m.AssetFor(runtime.GOARCH)
	if !ok {
		return "", fmt.Errorf("manifest %s has no asset for %s", m.Version, runtime.GOARCH)
	}
	sum, size, err := fileSHA256(verified)
	if err != nil {
		return "", err
	}
	if err := asset.Verify(sum, size); err != nil {
		return "", fmt.Errorf("%w: %v", errHashMismatch, err)
	}
	updateLog("verified %s against the signed manifest %s", verifiedExeName, m.Version)
	return verified, nil
}

// prepareUpdateDir makes dir a fresh, empty, locked directory. Whatever was
// there before — leftovers, a file, or a link or junction someone planted
// while the install folder was still writable — is removed first (RemoveAll
// removes a link itself, never its target).
func prepareUpdateDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	if err := lockUpdateDir(dir); err != nil {
		return fmt.Errorf("restrict permissions: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return errors.New("not a plain directory")
	}
	return nil
}

// copyLimited copies src to dst, refusing a source larger than max bytes.
func copyLimited(src, dst string, max int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, max+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > max {
		return fmt.Errorf("%s is larger than %d bytes", filepath.Base(src), max)
	}
	return os.WriteFile(dst, data, 0o644)
}

// swapExe renames cur to cur.old and copies verified into its place,
// putting the old exe back when the copy fails.
func swapExe(verified, cur string) error {
	old := cur + oldExeSuffix
	os.Remove(old) // stale leftover from a previous update, best-effort
	if err := os.Rename(cur, old); err != nil {
		return fmt.Errorf("rename installed exe: %w", err)
	}
	updateLog("renamed %s -> %s", filepath.Base(cur), filepath.Base(old))

	if err := copyFile(verified, cur); err != nil {
		updateLog("copy failed: %v — rolling back", err)
		os.Remove(cur)
		if rerr := os.Rename(old, cur); rerr != nil {
			updateLog("ROLLBACK FAILED: %v (old exe is at %s)", rerr, old)
		} else {
			updateLog("rollback ok")
		}
		return fmt.Errorf("copy new exe: %w", err)
	}
	updateLog("copied new exe into place")
	return nil
}

// waitForProcessExit blocks until pid exits or timeout elapses. Returns true
// when the process is gone (including when it could not be opened, which
// on Windows means it no longer exists or is not ours to touch).
func waitForProcessExit(pid uint32, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// scCommand runs sc.exe with a hidden console window.
func scCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("sc", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// serviceReportsStopped parses `sc query` for the literal STOPPED state
// (queryServiceState folds STOP_PENDING into "stopped", which is not good
// enough before overwriting the service binary).
func serviceReportsStopped() bool {
	out, err := scCommand("query", serviceName).Output()
	if err != nil {
		return true // not installed
	}
	return strings.Contains(string(out), "STOPPED")
}

// stopServiceAndWait issues `sc stop` and polls until the SCM reports
// STOPPED or timeout passes.
func stopServiceAndWait(timeout time.Duration) error {
	if out, err := scCommand("stop", serviceName).CombinedOutput(); err != nil {
		// 1062 = not started; treat as already stopped.
		if !strings.Contains(string(out), "1062") {
			return fmt.Errorf("sc stop: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if serviceReportsStopped() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("service did not stop within %s", timeout)
}

func startService() error {
	out, err := scCommand("start", serviceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc start: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyFile copies src to dst (created/truncated with 0755) and fsyncs.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// launchGUIUnelevated starts exe as a normal (non-admin) GUI. We are running
// elevated, and a child inherits our token — so start it with the desktop
// user's own token (relaunch.go, #50). If that fails, hand the launch to
// explorer.exe, which runs at the desktop's medium integrity level and
// starts the target with its own token; and if even that fails, launch
// directly: an elevated GUI is still better than no GUI at all.
func launchGUIUnelevated(exe string) {
	if err := launchGUIAsUser(exe); err == nil {
		updateLog("relaunched GUI with the desktop user's token")
		return
	} else {
		updateLog("user-token launch failed: %v — trying explorer.exe", err)
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "explorer.exe"), exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err == nil {
		updateLog("relaunched GUI via explorer.exe")
		// explorer.exe returns quickly; don't hold the exe open.
		go cmd.Wait()
		return
	} else {
		updateLog("explorer.exe launch failed: %v — launching directly", err)
	}
	direct := exec.Command(exe, "gui")
	if err := direct.Start(); err != nil {
		updateLog("direct GUI launch failed: %v", err)
		return
	}
	direct.Process.Release()
}

// cleanupStaleUpdateFiles removes <exe>.old and abandoned ".part" downloads
// left by a previous update. Deleting .old fails while the elevated updater
// (mapped from that file) is still exiting, so retry for a few seconds.
// In a locked install folder (#126) the deletes there simply fail; the
// service removes those files instead. Best-effort; runs in a goroutine at
// GUI start.
func cleanupStaleUpdateFiles() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dirs := []string{filepath.Join(filepath.Dir(exe), "update"), filepath.Join(os.TempDir(), "smartthings-pc-control", "update")}
	if d := userDataDir(); d != "" {
		dirs = append(dirs, filepath.Join(d, "update"))
	}
	for _, dir := range dirs {
		parts, _ := filepath.Glob(filepath.Join(dir, "*.part"))
		for _, p := range parts {
			os.Remove(p)
		}
	}
	old := exe + oldExeSuffix
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(old); err != nil {
			return
		}
		if os.Remove(old) == nil {
			updateLog("removed %s", filepath.Base(old))
			return
		}
		time.Sleep(time.Second)
	}
}

// userDataDirName is the tray's own folder under %LOCALAPPDATA%.
const userDataDirName = "SmartThings PC Control"

// tempGUILogName is gui.log's fallback name in the temp dir.
const tempGUILogName = "smartthings-pc-control-gui.log"

// userDataDir is %LOCALAPPDATA%\SmartThings PC Control, the only place the
// tray (running as the logged-in user) writes files: gui.log and staged
// update downloads. The install folder is locked to administrators (#126),
// so nothing the tray does may need write access there. "" when
// LOCALAPPDATA is unset.
func userDataDir() string { return userDataDirIn(os.Getenv("LOCALAPPDATA")) }

// userDataDirIn is userDataDir for a given %LOCALAPPDATA%.
func userDataDirIn(localAppData string) string {
	if localAppData == "" {
		return ""
	}
	return filepath.Join(localAppData, userDataDirName)
}

// guiLogPathIn is gui.log inside dataDir, or a file in temp when dataDir is
// unknown.
func guiLogPathIn(dataDir, temp string) string {
	if dataDir == "" {
		return filepath.Join(temp, tempGUILogName)
	}
	return filepath.Join(dataDir, "gui.log")
}

// guiLogPath is where guiLog writes: %LOCALAPPDATA%\SmartThings PC
// Control\gui.log. Until #126 it was next to the exe.
func guiLogPath() string { return guiLogPathIn(userDataDir(), os.TempDir()) }

// updateLog appends a timestamped line to gui.log (guiLogPath; falling back
// to the temp dir when that is not writable). Truncates once the file
// grows past 1 MB — this log only ever sees a handful of lines per update.
func updateLog(format string, args ...interface{}) {
	guiLog("update", format, args...)
}

// guiLog is updateLog under another tag ("[tag] ..."), for the rare line
// the tray app itself has to leave.
func guiLog(tag, format string, args ...interface{}) {
	path := guiLogPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		path = filepath.Join(os.TempDir(), tempGUILogName)
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return
		}
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 1<<20 {
		f.Truncate(0)
	}
	fmt.Fprintf(f, "%s [%s] %s\r\n", time.Now().Format("2006-01-02 15:04:05"), tag, fmt.Sprintf(format, args...))
}

// messageBox shows a blocking native message box (the elevated updater has
// no Fyne window of its own).
func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	const mbOK, mbIconError = 0x0, 0x10
	windows.MessageBox(0, m, t, mbOK|mbIconError)
}
