package session

// The lock state and user name of one session, for the opt-in session
// block of GET /st/v1/status (edge-driver doc §3.2,
// "smartthings.expose_session"). A service runs in session 0, so they come
// from one WTSQuerySessionInformation call with WTSSessionInfoEx, which the
// terminal-services stack fills in on the service's behalf.
//
// The idle time is NOT read here (#77). WTSINFOEXW.LastInputTime stays pinned
// near logon on Windows 10/11 console sessions, so what it yields is the
// uptime, not the idle time (measured: 16780s reported against a real 136s).
// Only a process inside the interactive session can call GetLastInputInfo, so
// the tray app samples it and posts it to the service (service/st_idle.go).

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWtsapi32                     = windows.NewLazySystemDLL("wtsapi32.dll")
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

// Info is what WTS can tell the status endpoint about a session. The idle
// time is not part of it (#77): it arrives by heartbeat from the tray app.
type Info struct {
	Locked bool
	User   string
}

// QueryInfo reads one session's lock state and user name. Best effort:
// when the query fails (nobody logged in, an older Windows, a hardened
// policy) the caller reports null rather than guessing.
func QueryInfo(sessionID uint32) (Info, error) {
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
		return Info{}, fmt.Errorf("WTSQuerySessionInformation(session %d): %v", sessionID, err)
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))

	if size < wtsExMinBytes {
		return Info{}, fmt.Errorf("WTSINFOEX is %d bytes, want at least %d", size, wtsExMinBytes)
	}
	raw := unsafe.Slice(buf, size)
	if level := readU32(raw, wtsExLevelOffset); level != 1 {
		return Info{}, fmt.Errorf("WTSINFOEX level %d, want 1", level)
	}

	info := Info{User: readUTF16(raw, wtsExUserNameOffset, wtsExUserNameChars)}
	switch readU32(raw, wtsExFlagsOffset) {
	case wtsSessionStateLock:
		info.Locked = true
	case wtsSessionStateUnlock:
		info.Locked = false
	default:
		return Info{}, fmt.Errorf("unexpected WTSINFOEX session flags")
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
