package service

// Opt-in session information for GET /st/v1/status (edge-driver doc §4.2,
// "smartthings.expose_session"). A service runs in session 0 and cannot call
// GetLastInputInfo for the interactive desktop, so the lock state, the idle
// time and the user name all come from one WTSQuerySessionInformation call
// with WTSSessionInfoEx, which the terminal-services stack fills in on the
// service's behalf.
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
//	LARGE_INTEGER LastInputTime;       // 184
//	LARGE_INTEGER CurrentTime;         // 192
//	DWORD IncomingBytes; ...           // 200
const (
	wtsExLevelOffset     = 0
	wtsExDataOffset      = 8
	wtsExFlagsOffset     = wtsExDataOffset + 8
	wtsExUserNameOffset  = wtsExDataOffset + 78
	wtsExUserNameChars   = 21
	wtsExLastInputOffset = wtsExDataOffset + 184
	wtsExCurrentOffset   = wtsExDataOffset + 192
	// wtsExMinBytes is everything up to and including CurrentTime; a
	// shorter reply means this is not the struct we think it is.
	wtsExMinBytes = wtsExCurrentOffset + 8
)

// sessionInfo is what the status endpoint reports under "session".
type sessionInfo struct {
	Locked bool
	// IdleKnown is false when the session reports no last-input time
	// (a disconnected or freshly created session).
	IdleKnown   bool
	IdleSeconds int64
	User        string
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

	lastInput := readU64(raw, wtsExLastInputOffset)
	current := readU64(raw, wtsExCurrentOffset)
	if lastInput != 0 && current >= lastInput {
		// FILETIME ticks are 100ns.
		info.IdleKnown = true
		info.IdleSeconds = int64(time.Duration((current-lastInput)*100) / time.Second)
	}
	return info, nil
}

func readU32(b []byte, off int) uint32 {
	return *(*uint32)(unsafe.Pointer(&b[off]))
}

func readU64(b []byte, off int) uint64 {
	return *(*uint64)(unsafe.Pointer(&b[off]))
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
