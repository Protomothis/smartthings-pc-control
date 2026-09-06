package gui

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const serviceName = "RemoteShutdownService"

type svcState int

const (
	svcUnknown svcState = iota
	svcNotInstalled
	svcStopped
	svcRunning
)

// queryServiceState checks the Windows service via `sc query` (no admin
// rights needed). The GUI runs with -H=windowsgui, so hide the child console.
func queryServiceState() svcState {
	cmd := exec.Command("sc", "query", serviceName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return svcNotInstalled // sc exits non-zero when the service doesn't exist
	}
	if strings.Contains(string(out), "RUNNING") {
		return svcRunning
	}
	return svcStopped
}

// SHELLEXECUTEINFOW for ShellExecuteExW.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const seeMaskNoCloseProcess = 0x40

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevated launches file elevated (UAC prompt). When wait is true it
// blocks until the process exits, so callers can refresh state right
// afterwards. Returns an error when the user declines the UAC prompt. Call
// from a goroutine.
func runElevated(file, args string, wait bool) error {
	verbP, _ := syscall.UTF16PtrFromString("runas")
	fileP, _ := syscall.UTF16PtrFromString(file)
	argP, _ := syscall.UTF16PtrFromString(args)

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verbP,
		lpFile:       fileP,
		lpParameters: argP,
		nShow:        int32(windows.SW_HIDE),
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return err
	}
	if info.hProcess != 0 {
		if wait {
			windows.WaitForSingleObject(info.hProcess, windows.INFINITE)
		}
		windows.CloseHandle(info.hProcess)
	}
	return nil
}

// runElevatedWait launches file elevated and waits for it to exit.
func runElevatedWait(file, args string) error {
	return runElevated(file, args, true)
}

// runElevatedSelfWait runs this exe elevated with args and waits.
func runElevatedSelfWait(args string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return runElevatedWait(exe, args)
}

// runElevatedSelf runs this exe elevated with args and returns as soon as
// the process has started (after the UAC prompt is accepted). Used by the
// self-updater, whose elevated child waits for *this* process to exit.
func runElevatedSelf(args string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return runElevated(exe, args, false)
}
