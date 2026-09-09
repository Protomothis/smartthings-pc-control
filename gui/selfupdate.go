package gui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Self update, stage 2 of issue #40.
//
// The GUI downloads and verifies the new exe (update.go), then launches
// itself elevated as `update-apply "<newExe>" <guiPid>` and quits. The
// elevated copy (ApplyUpdate) waits for the GUI to exit, stops the service,
// renames the installed exe to <exe>.old, copies the download over the
// original path, restarts the service and relaunches the GUI. Renaming (not
// deleting) is what makes this work: Windows allows renaming a mapped image,
// and the elevated process itself is running from that same image.

// oldExeSuffix marks the previous binary kept for rollback until the next
// GUI start removes it.
const oldExeSuffix = ".old"

// ParseUpdateApplyArgs validates the arguments of the hidden
// `update-apply <newExePath> <guiPid>` command.
func ParseUpdateApplyArgs(args []string) (newExe string, pid int, err error) {
	if len(args) != 2 {
		return "", 0, fmt.Errorf("usage: update-apply <newExePath> <guiPid> (got %d args)", len(args))
	}
	newExe = strings.TrimSpace(args[0])
	if newExe == "" {
		return "", 0, errors.New("update-apply: empty exe path")
	}
	if !filepath.IsAbs(newExe) {
		return "", 0, fmt.Errorf("update-apply: exe path must be absolute: %q", newExe)
	}
	if !strings.EqualFold(filepath.Ext(newExe), ".exe") {
		return "", 0, fmt.Errorf("update-apply: not an exe: %q", newExe)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || pid < 0 {
		return "", 0, fmt.Errorf("update-apply: bad pid %q", args[1])
	}
	return newExe, pid, nil
}

// ApplyUpdate replaces the running installation with newExe. It must run
// elevated (the GUI launches it via ShellExecuteEx "runas"). waitPid is the
// GUI process that spawned us; 0 skips the wait. Every step is appended to
// gui.log next to the exe. On failure after the rename the old exe is put
// back; either way the GUI is relaunched so the user is not left without it,
// and a message box reports the failure.
func ApplyUpdate(newExe string, waitPid int) error {
	err := applyUpdate(newExe, waitPid)
	if err != nil {
		updateLog("update failed: %v", err)
		messageBox("SmartThings PC Control", "업데이트 실패 / Update failed:\n\n"+err.Error()+
			"\n\ngui.log 를 확인하세요. / See gui.log for details.")
	}
	cur, cerr := os.Executable()
	if cerr == nil {
		launchGUIUnelevated(cur)
	}
	return err
}

func applyUpdate(newExe string, waitPid int) error {
	cur, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}
	cur, _ = filepath.Abs(cur)
	newExe, _ = filepath.Abs(newExe)
	updateLog("update-apply start: new=%s target=%s waitPid=%d", newExe, cur, waitPid)

	if strings.EqualFold(cur, newExe) {
		return errors.New("new exe path equals the installed path")
	}
	st, err := os.Stat(newExe)
	if err != nil {
		return fmt.Errorf("new exe: %w", err)
	}
	if st.Size() == 0 {
		return errors.New("new exe is empty")
	}

	if waitPid > 0 {
		if waitForProcessExit(uint32(waitPid), 30*time.Second) {
			updateLog("GUI pid %d exited", waitPid)
		} else {
			updateLog("GUI pid %d still running after 30s — continuing anyway", waitPid)
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

	old := cur + oldExeSuffix
	os.Remove(old) // stale leftover from a previous update, best-effort
	if err := os.Rename(cur, old); err != nil {
		if wasRunning {
			startService()
		}
		return fmt.Errorf("rename installed exe: %w", err)
	}
	updateLog("renamed %s -> %s", filepath.Base(cur), filepath.Base(old))

	if err := copyFile(newExe, cur); err != nil {
		updateLog("copy failed: %v — rolling back", err)
		os.Remove(cur)
		if rerr := os.Rename(old, cur); rerr != nil {
			updateLog("ROLLBACK FAILED: %v (old exe is at %s)", rerr, old)
		} else {
			updateLog("rollback ok")
		}
		if wasRunning {
			startService()
		}
		return fmt.Errorf("copy new exe: %w", err)
	}
	updateLog("copied new exe into place")
	// Staged download is no longer needed; ignore errors (temp dir cleanup).
	if err := os.Remove(newExe); err == nil {
		os.Remove(filepath.Dir(newExe)) // only succeeds when empty
	}

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
// Best-effort; runs in a goroutine at GUI start.
func cleanupStaleUpdateFiles() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	for _, dir := range []string{filepath.Join(filepath.Dir(exe), "update"), filepath.Join(os.TempDir(), "smartthings-pc-control", "update")} {
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

// updateLog appends a timestamped line to gui.log next to the exe (falling
// back to the temp dir when that is not writable). Truncates once the file
// grows past 1 MB — this log only ever sees a handful of lines per update.
func updateLog(format string, args ...interface{}) {
	path := "gui.log"
	if exe, err := os.Executable(); err == nil {
		path = filepath.Join(filepath.Dir(exe), "gui.log")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		path = filepath.Join(os.TempDir(), "smartthings-pc-control-gui.log")
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return
		}
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 1<<20 {
		f.Truncate(0)
	}
	fmt.Fprintf(f, "%s [update] %s\r\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// messageBox shows a blocking native message box (the elevated updater has
// no Fyne window of its own).
func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	const mbOK, mbIconError = 0x0, 0x10
	windows.MessageBox(0, m, t, mbOK|mbIconError)
}
