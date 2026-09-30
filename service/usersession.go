package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	modWtsapi32              = syscall.NewLazyDLL("wtsapi32.dll")
	modKernel32              = syscall.NewLazyDLL("kernel32.dll")
	procWTSQueryUserToken    = modWtsapi32.NewProc("WTSQueryUserToken")
	procProcessIdToSessionId = modKernel32.NewProc("ProcessIdToSessionId")
)

// errNoUserSession means nobody is logged in interactively: there is no
// explorer.exe, or the session it runs in has no user token (#103). Callers
// test for it with errors.Is and answer 409 no_user_session.
var errNoUserSession = errors.New("no_user_session")

// errorNoToken is ERROR_NO_TOKEN, what WTSQueryUserToken returns for a
// session nobody is logged into (a disconnected or logging-off session).
const errorNoToken = syscall.Errno(1008)

// userSessionWaitDelay bounds how long a waited-for user-session command
// may keep its output pipes open after it exited or was killed — a
// grandchild that inherited them must not keep CombinedOutput blocked.
const userSessionWaitDelay = 500 * time.Millisecond

// getActiveUserSessionID finds the session ID of the logged-in user
// by looking for explorer.exe processes.
func getActiveUserSessionID() (uint32, error) {
	return getActiveUserSessionIDContext(context.Background())
}

// getActiveUserSessionIDContext is getActiveUserSessionID bounded by ctx,
// so a caller with a deadline (runUserAction) is not held up by a slow
// PowerShell start. No explorer.exe is reported as errNoUserSession.
func getActiveUserSessionIDContext(ctx context.Context) (uint32, error) {
	// Use PowerShell to get explorer.exe session IDs
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
		"(Get-Process -Name explorer -ErrorAction SilentlyContinue | Select-Object -First 1).SessionId")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to find explorer.exe: %v", err)
	}

	sidStr := strings.TrimSpace(string(output))
	if sidStr == "" {
		return 0, fmt.Errorf("%w: no explorer.exe process found", errNoUserSession)
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
		if errors.Is(err, errorNoToken) {
			return 0, fmt.Errorf("%w: session %d has no user token", errNoUserSession, sessionID)
		}
		return 0, fmt.Errorf("WTSQueryUserToken failed (session %d): %v", sessionID, err)
	}
	return token, nil
}

// userSessionCommand builds an exec.Cmd that runs under the active user's
// token, i.e. inside their interactive desktop session. The caller must
// Close the returned token once the process has been started. The command
// is killed when ctx is done (exec.CommandContext), and the error wraps
// errNoUserSession when nobody is logged in.
func userSessionCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, syscall.Token, error) {
	sessionID, err := getActiveUserSessionIDContext(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("get session: %w", err)
	}

	token, err := getUserToken(sessionID)
	if err != nil {
		return nil, 0, fmt.Errorf("get user token (session %d): %w", sessionID, err)
	}

	cmd := exec.CommandContext(ctx, name, args...)
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
	_, err := outputInUserSession(context.Background(), name, args...)
	return err
}

// outputInUserSession is runInUserSession that also returns the combined
// output and kills the child when ctx is done. The output is returned even
// when the command fails, since a failing child may still have said why on
// stdout — user-action does exactly that.
func outputInUserSession(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, token, err := userSessionCommand(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	defer token.Close()
	cmd.WaitDelay = userSessionWaitDelay

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("exec error: %w - output: %s", err, string(output))
	}
	return output, nil
}

// startInUserSession launches a command in the active user's desktop
// session without waiting for it. Used for long-lived processes such as the
// tray app, where waiting would pin a goroutine for the app's lifetime.
func startInUserSession(name string, args ...string) error {
	cmd, token, err := userSessionCommand(context.Background(), name, args...)
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
