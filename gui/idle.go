package gui

// Session heartbeat (#77, #104). The service runs in session 0 and cannot
// measure how long the user has been away — WTSINFOEXW.LastInputTime is
// pinned near logon on Windows 10/11 console sessions — nor see the volume
// the user just changed with the keyboard. This app does run in the
// interactive session, so every 30s it samples GetLastInputInfo and the
// default playback device and posts both to the service, which publishes
// them in GET /st/v1/status.

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// idleHeartbeatInterval is how often the samples are taken and posted.
// The service treats an idle sample as stale after 90s, so two posts may
// be lost (a suspended machine, a restarting service) before the hub sees
// null.
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

// heartbeatSources are the samplers and switches behind buildHeartbeat,
// replaced by the tests.
var (
	heartbeatExposeSession = localExposeSession
	heartbeatMediaEnabled  = localMediaEnabled
	heartbeatIdle          = systemIdleSeconds
	// heartbeatAudio is the same getter `user-action audio get` uses, run
	// in-process: this app already is in the user's session.
	heartbeatAudio = useraction.ReadAudio
	heartbeatNow   = time.Now
)

// buildHeartbeat samples what the switches allow; ok is false when there
// is nothing to send.
//
// The switches are re-read from config.json on every tick — the same file
// localSecret() uses, rewritten by the service whenever a setting changes
// — so turning smartthings.expose_session or media.enabled off stops that
// part of the post within one interval, without a restart or an API round
// trip. The two are independent: the audio block serves the volume
// commands whether or not the session block is published.
func buildHeartbeat() (Heartbeat, bool) {
	var hb Heartbeat
	if heartbeatExposeSession() {
		if idle, ok := heartbeatIdle(); ok {
			hb.IdleSeconds = &idle
		}
	}
	if heartbeatMediaEnabled() {
		// A PC without a playback device (or a Core Audio hiccup) simply
		// sends no audio block; the service keeps its last value.
		if a, err := heartbeatAudio(); err == nil {
			hb.Audio = &HeartbeatAudio{
				Volume:    a.Volume,
				Muted:     a.Muted,
				Device:    a.Device,
				SampledAt: heartbeatNow().Format(time.RFC3339Nano),
			}
		}
	}
	return hb, hb.IdleSeconds != nil || hb.Audio != nil
}

// sendIdleHeartbeat posts one heartbeat, or does nothing at all.
//
// Every failure is silent: the service being down, mid-restart or simply
// not listening yet is the normal state of affairs for a tray app, and the
// next tick retries anyway.
func (u *ui) sendIdleHeartbeat() {
	hb, ok := buildHeartbeat()
	if !ok {
		return
	}
	err := u.client.SessionHeartbeat(hb)
	if !errors.Is(err, errUnauthorized) {
		return
	}
	// A secret is configured and this client has no session yet (the user
	// never opened the window, or the service restarted). config.json holds
	// the secret, so log in the way the toast handler does and retry once.
	if secret := localSecret(); secret != "" && u.client.Login(secret) == nil {
		u.client.SessionHeartbeat(hb)
	}
}
