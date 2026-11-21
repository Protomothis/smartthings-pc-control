package webui

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
	// Admin: the user is a member of BUILTIN\Administrators, however it
	// shows (peeradmin.go): enabled in the token (elevated), deny-only (the
	// UAC-filtered token), enabled in the linked token, or the user's SID —
	// or a group of the token — in the local group (#168).
	Admin bool
	// AdminVia is the check that found it, AdminDetail what the checks saw
	// along the way; both for the log.
	AdminVia    string
	AdminDetail string
	// Image is the full path of the process's exe.
	Image string
}

// inspectProcess opens pid and reads its token, session and image. It
// needs LocalSystem for other users' processes, which the service is.
func (s *Server) inspectProcess(pid uint32) (peerProcess, error) {
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
		if g.Sid.IsWellKnown(windows.WinInteractiveSid) && g.Attributes&windows.SE_GROUP_ENABLED != 0 {
			p.Interactive = true
		}
	}
	v := checkAdmin(winToken{tok}, p.UserSID, s.peerAdmins)
	p.Admin, p.AdminVia, p.AdminDetail = v.Admin, v.Via, v.Detail

	p.SessionUserSID = s.sessionUserSID(p.SessionID)
	return p, nil
}

// sessionUserSID is the SID of the user logged on to session, "" when
// nobody is (the logon screen) or the token cannot be had.
func (s *Server) sessionUserSID(session uint32) string {
	if session == 0 {
		return ""
	}
	t, err := s.d.UserToken(session)
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

// winToken is the tokenReader of a real process token.
type winToken struct{ t windows.Token }

func (w winToken) groups() ([]tokenGroup, error) {
	g, err := w.t.GetTokenGroups()
	if err != nil {
		return nil, err
	}
	return copyGroups(g), nil
}

func (w winToken) elevationType() (uint32, error) {
	var t, n uint32
	err := windows.GetTokenInformation(w.t, windows.TokenElevationType, (*byte)(unsafe.Pointer(&t)), uint32(unsafe.Sizeof(t)), &n)
	return t, err
}

func (w winToken) linkedGroups() ([]tokenGroup, error) {
	lt, err := w.t.GetLinkedToken()
	if err != nil {
		return nil, err
	}
	defer lt.Close()
	g, err := lt.GetTokenGroups()
	if err != nil {
		return nil, err
	}
	return copyGroups(g), nil
}

// copyGroups turns TokenGroups into strings while its buffer is alive.
func copyGroups(g *windows.Tokengroups) []tokenGroup {
	all := g.AllGroups()
	out := make([]tokenGroup, 0, len(all))
	for _, sa := range all {
		out = append(out, tokenGroup{SID: sa.Sid.String(), Attr: sa.Attributes})
	}
	return out
}

var (
	modNetapi32                 = windows.NewLazySystemDLL("netapi32.dll")
	procNetLocalGroupGetMembers = modNetapi32.NewProc("NetLocalGroupGetMembers")
)

// maxPreferredLength is MAX_PREFERRED_LENGTH: NetLocalGroupGetMembers
// sizes the buffer itself.
const maxPreferredLength = 0xFFFFFFFF

// localAdministrators lists the SIDs that are direct members of the local
// BUILTIN\Administrators group. The group is named by looking its SID up,
// so a localized name ("Administratoren") works too.
func localAdministrators() ([]string, error) {
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	name, _, _, err := sid.LookupAccount("")
	if err != nil {
		return nil, fmt.Errorf("group name: %w", err)
	}
	group, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var (
		buf         *byte
		read, total uint32
		resume      uintptr
	)
	r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(group)), 0,
		uintptr(unsafe.Pointer(&buf)), maxPreferredLength,
		uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
	if buf != nil {
		defer func() { _ = windows.NetApiBufferFree(buf) }()
	}
	if r != 0 {
		return nil, fmt.Errorf("NetLocalGroupGetMembers: %w", windows.Errno(r))
	}
	if read == 0 {
		return nil, nil
	}
	// LOCALGROUP_MEMBERS_INFO_0 is one PSID per member.
	sids := unsafe.Slice((**windows.SID)(unsafe.Pointer(buf)), read)
	out := make([]string, 0, read)
	for _, s := range sids {
		if s != nil {
			out = append(out, s.String())
		}
	}
	return out, nil
}
