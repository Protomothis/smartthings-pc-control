package gui

// Idle-time heartbeat (#77). The service runs in session 0 and cannot
// measure how long the user has been away — WTSINFOEXW.LastInputTime is
// pinned near logon on Windows 10/11 console sessions. This app does run in
// the interactive session, so it samples GetLastInputInfo and posts the
// result to the service, which publishes it in GET /st/v1/status while the
// sample is fresh.

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// idleHeartbeatInterval is how often the idle time is sampled and posted.
// The service treats a sample as stale after 90s, so two posts may be lost
// (a suspended machine, a restarting service) before the hub sees null.
const idleHeartbeatInterval = 30 * time.Second

var (
	modUser32            = windows.NewLazySystemDLL("user32.dll")
	modKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGetLastInputInfo = modUser32.NewProc("GetLastInputInfo")
	procGetTickCount     = modKernel32.NewProc("GetTickCount")
)

// lastInputInfo is LASTINPUTINFO: { UINT cbSize; DWORD dwTime; }, where
// dwTime is the GetTickCount value of the last input event.
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// idleSecondsFrom converts a last-input tick and the current tick into
// seconds. Both are 32-bit millisecond counters that wrap every ~49.7 days;
// subtracting them as uint32 wraps the same way, so a wrap between the two
// samples still yields the real elapsed time instead of ~49 days.
func idleSecondsFrom(lastInput, now uint32) int64 {
	return int64((now - lastInput) / 1000)
}

// systemIdleSeconds reports how long the interactive session has had no
// keyboard or mouse input; ok is false when the call fails (which happens
// on the lock screen under some policies).
func systemIdleSeconds() (int64, bool) {
	info := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if r1, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); r1 == 0 {
		return 0, false
	}
	tick, _, _ := procGetTickCount.Call()
	return idleSecondsFrom(info.dwTime, uint32(tick)), true
}

// sendIdleHeartbeat posts one idle sample, or does nothing at all.
//
// The opt-in is re-read from config.json on every tick — the same file
// localSecret() uses, rewritten by the service whenever the flag changes —
// so turning smartthings.expose_session off stops the posts within one
// interval without a restart and without an API round trip.
//
// Every failure is silent: the service being down, mid-restart or simply
// not listening yet is the normal state of affairs for a tray app, and the
// next tick retries anyway.
func (u *ui) sendIdleHeartbeat() {
	if !localExposeSession() {
		return
	}
	idle, ok := systemIdleSeconds()
	if !ok {
		return
	}
	err := u.client.SessionHeartbeat(idle)
	if !errors.Is(err, errUnauthorized) {
		return
	}
	// A secret is configured and this client has no session yet (the user
	// never opened the window, or the service restarted). config.json holds
	// the secret, so log in the way the toast handler does and retry once.
	if secret := localSecret(); secret != "" && u.client.Login(secret) == nil {
		u.client.SessionHeartbeat(idle)
	}
}
