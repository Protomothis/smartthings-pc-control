package service

// Who is on the other end of a loopback connection (#131). A TCP
// connection between two sockets of this machine shows up twice in the
// IPv4 TCP table: once for the server socket (local = the WebUI port) and
// once for the client socket, whose row carries the client's PID. While a
// request is being served that connection is open, so its client port
// cannot belong to anybody else; the row found for it names the process
// that sent the request. From the PID the service (LocalSystem) can open
// the process and its token and read who runs it, in which session, from
// which exe.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tcpRow is one MIB_TCPROW_OWNER_PID, decoded.
type tcpRow struct {
	State  uint32
	Local  netip.AddrPort
	Remote netip.AddrPort
	PID    uint32
}

// mibTCPStateEstab is MIB_TCP_STATE_ESTAB.
const mibTCPStateEstab = 5

// tcpRowSize is sizeof(MIB_TCPROW_OWNER_PID): six DWORDs.
const tcpRowSize = 24

var (
	modIphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = modIphlpapi.NewProc("GetExtendedTcpTable")
)

// tcpTableOwnerPIDAll is TCP_TABLE_OWNER_PID_ALL.
const tcpTableOwnerPIDAll = 5

// readTCPTable returns the IPv4 TCP table with owning PIDs. The table can
// grow between the size query and the read, so a short buffer is retried.
func readTCPTable() ([]tcpRow, error) {
	size := uint32(16 << 10)
	for range 4 {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTCPTable.Call(
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
			0, windows.AF_INET, tcpTableOwnerPIDAll, 0)
		switch windows.Errno(r) {
		case 0:
			return parseTCPTable(buf[:min(int(size), len(buf))])
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4 << 10 // room for connections opened meanwhile
			continue
		default:
			return nil, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(r))
		}
	}
	return nil, errors.New("GetExtendedTcpTable: table kept growing")
}

// parseTCPTable decodes MIB_TCPTABLE_OWNER_PID: a DWORD count, then the
// rows. Addresses are in network order as stored; a port is the first two
// bytes of its DWORD, big-endian.
func parseTCPTable(buf []byte) ([]tcpRow, error) {
	if len(buf) < 4 {
		return nil, errors.New("TCP table: short buffer")
	}
	n := binary.LittleEndian.Uint32(buf)
	if uint64(len(buf)-4) < uint64(n)*tcpRowSize {
		return nil, fmt.Errorf("TCP table: %d rows do not fit %d bytes", n, len(buf))
	}
	rows := make([]tcpRow, 0, n)
	for i := range n {
		b := buf[4+i*tcpRowSize:]
		rows = append(rows, tcpRow{
			State:  binary.LittleEndian.Uint32(b[0:]),
			Local:  netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[4:8])), binary.BigEndian.Uint16(b[8:10])),
			Remote: netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[12:16])), binary.BigEndian.Uint16(b[16:18])),
			PID:    binary.LittleEndian.Uint32(b[20:]),
		})
	}
	return rows, nil
}

// errNoPeerRow means no open connection matches the request's endpoints.
var errNoPeerRow = errors.New("no TCP connection matches the request")

// ownerPIDFor finds the client socket of the connection client → server:
// the established row whose local end is the client and whose remote end
// is the server. Exactly one may match; two would mean the table is not
// what this code believes, and nobody is trusted then.
func ownerPIDFor(rows []tcpRow, client, server netip.AddrPort) (uint32, error) {
	var pid uint32
	found := 0
	for _, r := range rows {
		if r.State == mibTCPStateEstab && r.Local == client && r.Remote == server {
			pid = r.PID
			found++
		}
	}
	switch found {
	case 0:
		return 0, errNoPeerRow
	case 1:
		return pid, nil
	}
	return 0, fmt.Errorf("%d TCP connections match %s → %s", found, client, server)
}

// peerProcess is what the service learns about the process behind a PID.
type peerProcess struct {
	PID       uint32
	SessionID uint32
	// UserSID is the token's user.
	UserSID string
	// SessionUserSID is the user logged on to SessionID (WTSQueryUserToken),
	// "" when nobody is or it cannot be read.
	SessionUserSID string
	// Interactive: the token holds INTERACTIVE (S-1-5-4), i.e. it comes
	// from a console or remote desktop logon, not a service, batch or
	// network one.
	Interactive bool
	// Admin: the token lists BUILTIN\Administrators, enabled (elevated) or
	// deny-only (the UAC-filtered token of an administrator).
	Admin bool
	// Image is the full path of the process's exe.
	Image string
}

// inspectProcess opens pid and reads its token, session and image. It
// needs LocalSystem for other users' processes, which the service is.
func inspectProcess(pid uint32) (peerProcess, error) {
	p := peerProcess{PID: pid}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return p, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return p, fmt.Errorf("QueryFullProcessImageName(%d): %w", pid, err)
	}
	p.Image = windows.UTF16ToString(buf[:n])

	if err := windows.ProcessIdToSessionId(pid, &p.SessionID); err != nil {
		return p, fmt.Errorf("ProcessIdToSessionId(%d): %w", pid, err)
	}

	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return p, fmt.Errorf("OpenProcessToken(%d): %w", pid, err)
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		return p, fmt.Errorf("token user (%d): %w", pid, err)
	}
	p.UserSID = user.User.Sid.String()
	groups, err := tok.GetTokenGroups()
	if err != nil {
		return p, fmt.Errorf("token groups (%d): %w", pid, err)
	}
	for _, g := range groups.AllGroups() {
		switch {
		case g.Sid.IsWellKnown(windows.WinInteractiveSid) && g.Attributes&windows.SE_GROUP_ENABLED != 0:
			p.Interactive = true
		case g.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) &&
			g.Attributes&(windows.SE_GROUP_ENABLED|windows.SE_GROUP_USE_FOR_DENY_ONLY) != 0:
			p.Admin = true
		}
	}

	p.SessionUserSID = sessionUserSID(p.SessionID)
	return p, nil
}

// sessionUserSID is the SID of the user logged on to session, "" when
// nobody is (the logon screen) or the token cannot be had.
func sessionUserSID(session uint32) string {
	if session == 0 {
		return ""
	}
	t, err := wts.QueryUserToken(session)
	if err != nil {
		return ""
	}
	defer t.Close()
	u, err := t.GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}
