package gui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Relaunching the GUI from the elevated updater (issue #50).
//
// The updater runs elevated, and anything it starts directly inherits the
// admin token. Handing the launch to explorer.exe mostly works, but the
// process that comes back is not reliably the user's own: after the v0.3.3
// swap the HKCU Run entry the GUI rewrites on every start was left
// untouched. So take the desktop shell's token (explorer.exe in our
// session), duplicate it into a primary token and start the GUI with
// CreateProcessWithTokenW — the child then runs exactly like a double-click:
// the user's token, medium integrity, the user's environment block.

var procCreateProcessWithTokenW = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")

// launchGUIAsUser relaunches `exe gui` as the interactive desktop user.
// Needs SeImpersonatePrivilege (administrators have it), so this is only
// useful from the elevated updater; callers fall back on error.
func launchGUIAsUser(exe string) error {
	tok, err := shellTokenForCurrentSession()
	if err != nil {
		return err
	}
	defer tok.Close()
	return launchGUIWithToken(tok, exe)
}

// shellTokenForCurrentSession returns a primary token duplicated from an
// explorer.exe running in the caller's session. The caller closes it.
func shellTokenForCurrentSession() (windows.Token, error) {
	var mySession uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &mySession); err != nil {
		return 0, fmt.Errorf("own session id: %w", err)
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	lastErr := errors.New("no explorer.exe found in this session")
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if !strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), "explorer.exe") {
			continue
		}
		var sid uint32
		if windows.ProcessIdToSessionId(pe.ProcessID, &sid) != nil || sid != mySession {
			continue
		}
		tok, err := duplicatePrimaryToken(pe.ProcessID)
		if err != nil {
			lastErr = err
			continue
		}
		return tok, nil
	}
	return 0, lastErr
}

// duplicatePrimaryToken copies pid's access token as a primary token that
// CreateProcessWithTokenW accepts.
func duplicatePrimaryToken(pid uint32) (windows.Token, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)

	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		return 0, fmt.Errorf("open token of pid %d: %w", pid, err)
	}
	defer tok.Close()

	var primary windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_ALL_ACCESS, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return 0, fmt.Errorf("duplicate token: %w", err)
	}
	return primary, nil
}

// launchGUIWithToken starts `exe gui` under token, with that user's
// environment block and the exe's folder as working directory.
func launchGUIWithToken(token windows.Token, exe string) error {
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, token, false); err != nil {
		return fmt.Errorf("environment block: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)

	appName, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cmdLine, err := windows.UTF16PtrFromString(fmt.Sprintf(`"%s" gui`, exe))
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err != nil {
		return err
	}
	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation

	const logonWithProfile = 0x1 // LOGON_WITH_PROFILE: load the user's profile (HKCU)
	r1, _, callErr := procCreateProcessWithTokenW.Call(
		uintptr(token),
		logonWithProfile,
		uintptr(unsafe.Pointer(appName)),
		uintptr(unsafe.Pointer(cmdLine)),
		uintptr(windows.CREATE_UNICODE_ENVIRONMENT),
		uintptr(unsafe.Pointer(env)),
		uintptr(unsafe.Pointer(dir)),
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if r1 == 0 {
		return fmt.Errorf("CreateProcessWithTokenW: %w", callErr)
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}
