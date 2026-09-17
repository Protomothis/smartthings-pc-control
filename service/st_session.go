package service

// Opt-in session information for GET /st/v1/status (edge-driver doc §4.2,
// "smartthings.expose_session"). A service runs in session 0, so the lock
// state and the user name come from one WTSQuerySessionInformation call with
// WTSSessionInfoEx, which the terminal-services stack fills in on the
// service's behalf.
//
// The idle time is NOT read here (#77). WTSINFOEXW.LastInputTime stays pinned
// near logon on Windows 10/11 console sessions, so what it yields is the
// uptime, not the idle time (measured: 16780s reported against a real 136s).
// Only a process inside the interactive session can call GetLastInputInfo, so
// the tray app samples it and posts it to the service (st_idle.go).
//
// Everything here is best effort: when the query fails (nobody logged in, an
// older Windows, a hardened policy) the caller reports null rather than
// guessing, and the status endpoint still answers.

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procWTSQuerySessionInformationW = modWtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = modWtsapi32.NewProc("WTSFreeMemory")
)

const (
	// wtsSessionInfoEx is WTS_INFO_CLASS.WTSSessionInfoEx.
	wtsSessionInfoEx = 25
	// wtsSessionStateLock/Unlock are the documented WTS_SESSIONSTATE_*
	// values of WTSINFOEX_LEVEL1_W.SessionFlags.
	wtsSessionStateLock   = 0
	wtsSessionStateUnlock = 1
)

// Byte offsets inside WTSINFOEXW as the 64-bit ABI lays it out:
//
//	DWORD Level;                       // 0
//	                                   // 4 padding (the union aligns to 8)
//	union { WTSINFOEX_LEVEL1_W ... }   // 8
//
// and inside WTSINFOEX_LEVEL1_W, relative to the union:
//
//	ULONG  SessionId;                  //   0
//	DWORD  SessionState;               //   4
//	LONG   SessionFlags;               //   8
//	WCHAR  WinStationName[33];         //  12  (66 bytes)
//	WCHAR  UserName[21];               //  78  (42 bytes)
//	WCHAR  DomainName[18];             // 120  (36 bytes)
//	                                   // 156  4 padding before the times
//	LARGE_INTEGER LogonTime;           // 160
//	LARGE_INTEGER ConnectTime;         // 168
//	LARGE_INTEGER DisconnectTime;      // 176
//	LARGE_INTEGER LastInputTime;       // 184  (unusable, see the file header)
//	LARGE_INTEGER CurrentTime;         // 192
//	DWORD IncomingBytes; ...           // 200
const (
	wtsExLevelOffset    = 0
	wtsExDataOffset     = 8
	wtsExFlagsOffset    = wtsExDataOffset + 8
	wtsExUserNameOffset = wtsExDataOffset + 78
	wtsExUserNameChars  = 21
	wtsExCurrentOffset  = wtsExDataOffset + 192
	// wtsExMinBytes is everything up to and including CurrentTime; a
	// shorter reply means this is not the struct we think it is. The
	// fields read below all sit well before it, so this is a sanity check
	// on the shape of the reply rather than a bound on the reads.
	wtsExMinBytes = wtsExCurrentOffset + 8
)

// sessionInfo is what WTS can tell the status endpoint about the session.
// The idle time is not part of it (#77): it arrives by heartbeat from the
// tray app and is merged in by stSessionInfo.
type sessionInfo struct {
	Locked bool
	User   string
}

// querySessionInfo describes the interactive user session, or fails when
// there is none to describe.
func querySessionInfo() (sessionInfo, error) {
	sessionID, err := getActiveUserSessionID()
	if err != nil {
		return sessionInfo{}, err
	}
	return querySessionInfoID(sessionID)
}

// querySessionInfoID is querySessionInfo for one known session id.
func querySessionInfoID(sessionID uint32) (sessionInfo, error) {
	// buf is a *byte (not a uintptr) so the returned address stays a real
	// pointer: converting a uintptr back to a pointer is never safe.
	var buf *byte
	var size uint32
	r1, _, err := procWTSQuerySessionInformationW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		uintptr(sessionID),
		uintptr(wtsSessionInfoEx),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&size)),
	)
	if r1 == 0 || buf == nil {
		return sessionInfo{}, fmt.Errorf("WTSQuerySessionInformation(session %d): %v", sessionID, err)
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))

	if size < wtsExMinBytes {
		return sessionInfo{}, fmt.Errorf("WTSINFOEX is %d bytes, want at least %d", size, wtsExMinBytes)
	}
	raw := unsafe.Slice(buf, size)
	if level := readU32(raw, wtsExLevelOffset); level != 1 {
		return sessionInfo{}, fmt.Errorf("WTSINFOEX level %d, want 1", level)
	}

	info := sessionInfo{User: readUTF16(raw, wtsExUserNameOffset, wtsExUserNameChars)}
	switch readU32(raw, wtsExFlagsOffset) {
	case wtsSessionStateLock:
		info.Locked = true
	case wtsSessionStateUnlock:
		info.Locked = false
	default:
		return sessionInfo{}, fmt.Errorf("unexpected WTSINFOEX session flags")
	}
	return info, nil
}

func readU32(b []byte, off int) uint32 {
	return *(*uint32)(unsafe.Pointer(&b[off]))
}

// readUTF16 decodes a fixed-length, NUL-padded UTF-16 field.
func readUTF16(b []byte, off, chars int) string {
	u := unsafe.Slice((*uint16)(unsafe.Pointer(&b[off])), chars)
	for i, c := range u {
		if c == 0 {
			return windows.UTF16ToString(u[:i])
		}
	}
	return windows.UTF16ToString(u)
}

// ---- lock/unlock watcher (§4.5) --------------------------------------------

// sessionPollInterval is how often the lock state is sampled. A service in
// session 0 gets no WM_WTSSESSION_CHANGE and SERVICE_CONTROL_SESSIONCHANGE
// reports console connect/disconnect, not the lock screen — so there is no
// event to subscribe to and the state has to be polled. One
// WTSQuerySessionInformation call every 5s is cheap enough (§4.5 asks for
// "immediate", and 5s is the resolution the driver gets), and the poll only
// runs while smartthings.expose_session is on.
const sessionPollInterval = 5 * time.Second

// watchSessionLock emits session.locked / session.unlocked whenever the
// interactive session's lock state changes. The events are device state,
// not notifications: they go to the bus taps (the SmartThings push sink)
// and never to Telegram.
//
// The first successful sample only establishes the baseline; turning the
// option off and on again re-establishes it, so enabling the option never
// invents a transition. Failures (nobody logged in, WTS refusing) reset the
// baseline too, because what happened while the service could not look is
// unknown.
func watchSessionLock(stop <-chan struct{}) {
	t := time.NewTicker(sessionPollInterval)
	defer t.Stop()
	known := false
	var locked bool
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if !getConfig().SmartThings.ExposeSession {
			known = false
			continue
		}
		info, err := querySessionInfo()
		if err != nil {
			known = false
			continue
		}
		if known && info.Locked == locked {
			continue
		}
		if known {
			emitSessionLock(info)
		}
		known, locked = true, info.Locked
	}
}

// emitSessionLock reports one lock-state transition.
func emitSessionLock(info sessionInfo) {
	kind := "unlocked"
	if info.Locked {
		kind = "locked"
	}
	// No idle_seconds here: idle is heartbeat state, never an event (#77).
	fields := map[string]string{}
	// The user name follows the same opt-in as the status block (§4.2).
	if getConfig().SmartThings.ExposeSessionUser && info.User != "" {
		fields["user"] = info.User
	}
	logMsg("Session %s", kind)
	emitDevice("session", kind, fields)
}
