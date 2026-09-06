package service

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

var (
	modWtsapi32          = syscall.NewLazyDLL("wtsapi32.dll")
	modKernel32          = syscall.NewLazyDLL("kernel32.dll")
	procWTSQueryUserToken = modWtsapi32.NewProc("WTSQueryUserToken")
	procProcessIdToSessionId = modKernel32.NewProc("ProcessIdToSessionId")
)

// getActiveUserSessionID finds the session ID of the logged-in user
// by looking for explorer.exe processes.
func getActiveUserSessionID() (uint32, error) {
	// Use PowerShell to get explorer.exe session IDs
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-Process -Name explorer -ErrorAction SilentlyContinue | Select-Object -First 1).SessionId")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to find explorer.exe: %v", err)
	}

	sidStr := strings.TrimSpace(string(output))
	if sidStr == "" {
		return 0, fmt.Errorf("no explorer.exe process found (no user logged in?)")
	}

	sid, err := strconv.ParseUint(sidStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid session id %q: %v", sidStr, err)
	}

	return uint32(sid), nil
}

// getUserToken returns a token handle for the user logged into the given session.
func getUserToken(sessionID uint32) (syscall.Token, error) {
	var token syscall.Token
	r1, _, err := procWTSQueryUserToken.Call(
		uintptr(sessionID),
		uintptr(unsafe.Pointer(&token)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("WTSQueryUserToken failed (session %d): %v", sessionID, err)
	}
	return token, nil
}

// userSessionCommand builds an exec.Cmd that runs under the active user's
// token, i.e. inside their interactive desktop session. The caller must
// Close the returned token once the process has been started.
func userSessionCommand(name string, args ...string) (*exec.Cmd, syscall.Token, error) {
	sessionID, err := getActiveUserSessionID()
	if err != nil {
		return nil, 0, fmt.Errorf("get session: %w", err)
	}

	token, err := getUserToken(sessionID)
	if err != nil {
		return nil, 0, fmt.Errorf("get user token (session %d): %w", sessionID, err)
	}

	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Token: token,
	}
	return cmd, token, nil
}

// runInUserSession executes a command in the active user's desktop session
// and waits for it to finish. This is needed for commands like turnscreenoff
// that require access to the interactive desktop (Session 0 isolation
// prevents services from accessing it).
func runInUserSession(name string, args ...string) error {
	cmd, token, err := userSessionCommand(name, args...)
	if err != nil {
		return err
	}
	defer token.Close()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec error: %v - output: %s", err, string(output))
	}
	return nil
}

// startInUserSession launches a command in the active user's desktop
// session without waiting for it. Used for long-lived processes such as the
// tray app, where waiting would pin a goroutine for the app's lifetime.
func startInUserSession(name string, args ...string) error {
	cmd, token, err := userSessionCommand(name, args...)
	if err != nil {
		return err
	}
	defer token.Close()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start error: %v", err)
	}
	// Detach: the child outlives this call and nobody will Wait on it.
	return cmd.Process.Release()
}

// launchTrayApp starts this executable as the tray app ("gui --minimized")
// in the active user's session so the grace-period toast can be shown. The
// GUI's single-instance guard makes this a silent no-op when the tray app
// is already running.
func launchTrayApp() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}
	return startInUserSession(exe, "gui", "--minimized")
}

// runPowerShellInUserSession runs a PowerShell script in the active user's session.
func runPowerShellInUserSession(script string) {
	err := runInUserSession("powershell", "-NoProfile", "-Command", script)
	if err != nil {
		logMsg("powershell (user session) error: %v", err)
	} else {
		logMsg("powershell (user session) ok")
	}
}
