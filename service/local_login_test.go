package service

// Tests for POST /api/local-login (#131): the TCP table lookup, the trust
// decision, and the endpoint end to end over a real loopback connection.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// tableBytes encodes rows as MIB_TCPTABLE_OWNER_PID.
func tableBytes(rows []tcpRow) []byte {
	buf := make([]byte, 4+len(rows)*tcpRowSize)
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, r := range rows {
		b := buf[4+i*tcpRowSize:]
		binary.LittleEndian.PutUint32(b[0:], r.State)
		l, rm := r.Local.Addr().As4(), r.Remote.Addr().As4()
		copy(b[4:8], l[:])
		binary.BigEndian.PutUint16(b[8:], r.Local.Port())
		copy(b[12:16], rm[:])
		binary.BigEndian.PutUint16(b[16:], r.Remote.Port())
		binary.LittleEndian.PutUint32(b[20:], r.PID)
	}
	return buf
}

var (
	webEnd    = netip.MustParseAddrPort("127.0.0.1:5002")
	trayEnd   = netip.MustParseAddrPort("127.0.0.1:52431")
	otherEnd  = netip.MustParseAddrPort("127.0.0.1:52432")
	lanClient = netip.MustParseAddrPort("192.168.1.20:52431")
)

func TestParseTCPTable(t *testing.T) {
	want := []tcpRow{
		{State: mibTCPStateEstab, Local: webEnd, Remote: trayEnd, PID: 4000},
		{State: mibTCPStateEstab, Local: trayEnd, Remote: webEnd, PID: 1234},
	}
	got, err := parseTCPTable(tableBytes(want))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parsed %+v, want %+v", got, want)
	}
	if _, err := parseTCPTable([]byte{9, 0, 0, 0}); err == nil {
		t.Error("a count larger than the buffer was accepted")
	}
}

func TestOwnerPIDFor(t *testing.T) {
	rows := []tcpRow{
		// The server's own row for the connection: same endpoints, swapped.
		{State: mibTCPStateEstab, Local: webEnd, Remote: trayEnd, PID: 4000},
		{State: mibTCPStateEstab, Local: trayEnd, Remote: webEnd, PID: 1234},
		// Another client, and a closed connection that used this port.
		{State: mibTCPStateEstab, Local: otherEnd, Remote: webEnd, PID: 666},
		{State: 11 /* TIME_WAIT */, Local: lanClient, Remote: webEnd, PID: 0},
	}
	if pid, err := ownerPIDFor(rows, trayEnd, webEnd); err != nil || pid != 1234 {
		t.Errorf("tray connection: pid %d, %v; want 1234", pid, err)
	}
	if _, err := ownerPIDFor(rows, lanClient, webEnd); !errors.Is(err, errNoPeerRow) {
		t.Errorf("only a TIME_WAIT row: %v, want errNoPeerRow", err)
	}
	dup := append(rows, tcpRow{State: mibTCPStateEstab, Local: trayEnd, Remote: webEnd, PID: 999})
	if _, err := ownerPIDFor(dup, trayEnd, webEnd); err == nil {
		t.Error("two matching rows were trusted")
	}
}

// TestReadTCPTableFindsOwnConnection runs the real lookup on a loopback
// connection this test opens: the client row must name this process.
func TestReadTCPTableFindsOwnConnection(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if s := <-accepted; s != nil {
		defer s.Close()
	}

	rows, err := readTCPTable()
	if err != nil {
		t.Fatal(err)
	}
	client := netip.MustParseAddrPort(c.LocalAddr().String())
	server := netip.MustParseAddrPort(c.RemoteAddr().String())
	pid, err := ownerPIDFor(rows, client, server)
	if err != nil || pid != uint32(os.Getpid()) {
		t.Errorf("owner of %s → %s: pid %d, %v; want %d", client, server, pid, err, os.Getpid())
	}
}

// TestInspectOwnProcess reads this test process the way the service reads
// the tray. WTSQueryUserToken needs LocalSystem, so SessionUserSID is not
// checked here.
func TestInspectOwnProcess(t *testing.T) {
	p, err := inspectProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pid %d session %d interactive=%v admin=%v image=%s", p.PID, p.SessionID, p.Interactive, p.Admin, p.Image)
	if !isServiceExe(p.Image) {
		t.Errorf("image %q is not this exe", p.Image)
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if p.UserSID != u.User.Sid.String() {
		t.Errorf("user %s, want %s", p.UserSID, u.User.Sid)
	}
	var session uint32
	windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session)
	if p.SessionID != session {
		t.Errorf("session %d, want %d", p.SessionID, session)
	}
	if isServiceExe(`C:\Windows\System32\notepad.exe`) {
		t.Error("notepad counted as the service exe")
	}
}

// trustedTray is a peer every check accepts.
func trustedTray() peerProcess {
	return peerProcess{
		PID: 1234, SessionID: 1, UserSID: "S-1-5-21-1-2-3-1001", SessionUserSID: "S-1-5-21-1-2-3-1001",
		Interactive: true, Admin: true, Image: `C:\Program Files\SmartThings PC Control\smartthings-pc-control.exe`,
	}
}

func isTray(image string) bool { return image == trustedTray().Image }

func TestTrustLocalPeer(t *testing.T) {
	cases := []struct {
		name   string
		change func(*peerProcess)
		want   string // "" = trusted
	}{
		{"the tray", func(*peerProcess) {}, ""},
		{"a service in session 0", func(p *peerProcess) { p.SessionID = 0 }, "session_0"},
		{"a batch or network logon", func(p *peerProcess) { p.Interactive = false }, "not_interactive"},
		{"runas of another account in the session", func(p *peerProcess) { p.UserSID = "S-1-5-21-1-2-3-1002" }, "not_session_user"},
		{"a session nobody is logged on to", func(p *peerProcess) { p.SessionUserSID = "" }, "not_session_user"},
		{"a standard user", func(p *peerProcess) { p.Admin = false }, "not_admin"},
		{"another program (a browser)", func(p *peerProcess) { p.Image = `C:\Program Files\Browser\browser.exe` }, "other_exe"},
		{"a copy of the exe elsewhere", func(p *peerProcess) { p.Image = `C:\Users\bob\Downloads\smartthings-pc-control.exe` }, "other_exe"},
	}
	for _, c := range cases {
		p := trustedTray()
		c.change(&p)
		err := trustLocalPeer(p, isTray)
		var te *localTrustError
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (!errors.As(err, &te) || te.code != c.want):
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
}

// localLoginSetup configures a secret, swaps the process lookup for peer
// (the real TCP table still maps the connection to this test process) and
// returns a loopback server for the WebUI mux.
func localLoginSetup(t *testing.T, peer peerProcess) (srv *httptest.Server, inspected *[]uint32) {
	t.Helper()
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	resetLocalSession()
	var pids []uint32
	savedInspect, savedSame, savedNow := localPeerInspect, localPeerSameExe, localLoginNow
	localPeerInspect = func(pid uint32) (peerProcess, error) {
		pids = append(pids, pid)
		p := peer
		p.PID = pid
		return p, nil
	}
	localPeerSameExe = isTray
	t.Cleanup(func() {
		localPeerInspect, localPeerSameExe, localLoginNow = savedInspect, savedSame, savedNow
		resetLocalSession()
		sessionMu.Lock()
		sessionToken = ""
		sessionMu.Unlock()
	})
	srv = httptest.NewServer(newWebUIMux(false))
	t.Cleanup(srv.Close)
	return srv, &pids
}

func localLoginPost(t *testing.T, c *http.Client, base string) (*http.Response, map[string]string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/local-login", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "session" {
			return c
		}
	}
	return nil
}

func scheduleStatus(t *testing.T, c *http.Client, base string, cookie *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/schedule", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestLocalLoginIssuesSession(t *testing.T) {
	srv, pids := localLoginSetup(t, trustedTray())
	c := &http.Client{Timeout: 5 * time.Second}

	if code := scheduleStatus(t, c, srv.URL, nil); code != http.StatusUnauthorized {
		t.Fatalf("before the login: %d, want 401", code)
	}
	resp, body := localLoginPost(t, c, srv.URL)
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("local login: %d %v", resp.StatusCode, body)
	}
	if len(*pids) != 1 || (*pids)[0] != uint32(os.Getpid()) {
		t.Errorf("inspected pids %v, want this process %d", *pids, os.Getpid())
	}
	cookie := sessionCookie(resp)
	if cookie == nil || !cookie.HttpOnly {
		t.Fatalf("no HttpOnly session cookie: %+v", resp.Cookies())
	}
	if code := scheduleStatus(t, c, srv.URL, cookie); code != http.StatusOK {
		t.Errorf("with the local session: %d, want 200", code)
	}

	// A secret login (the browser) and a second local caller (the toast
	// handler) leave the tray's session alone.
	browser := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"secret":"s3cr3t"}`))
	browser.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	newWebUIMux(false).ServeHTTP(w, browser)
	if w.Code != http.StatusOK {
		t.Fatalf("secret login: %d", w.Code)
	}
	resp2, _ := localLoginPost(t, c, srv.URL)
	if again := sessionCookie(resp2); again == nil || again.Value != cookie.Value {
		t.Errorf("second local login changed the token: %+v", again)
	}
	if code := scheduleStatus(t, c, srv.URL, cookie); code != http.StatusOK {
		t.Errorf("after other logins: %d, want 200", code)
	}

	// The local token is worthless from the network.
	lan := httptest.NewRequest("GET", "/api/schedule", nil)
	lan.RemoteAddr = lanClient.String()
	lan.AddCookie(cookie)
	if checkAuth(lan, "s3cr3t") {
		t.Error("the local session was accepted from a LAN address")
	}
}

func TestLocalLoginRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*peerProcess)
		code   string
	}{
		{"session 0", func(p *peerProcess) { p.SessionID = 0 }, "session_0"},
		{"standard user", func(p *peerProcess) { p.Admin = false }, "not_admin"},
		{"other exe", func(p *peerProcess) { p.Image = `C:\Windows\explorer.exe` }, "other_exe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := trustedTray()
			tc.change(&p)
			srv, _ := localLoginSetup(t, p)
			c := &http.Client{Timeout: 5 * time.Second}
			resp, body := localLoginPost(t, c, srv.URL)
			if resp.StatusCode != http.StatusForbidden || body["code"] != tc.code {
				t.Errorf("%d %v, want 403 %s", resp.StatusCode, body, tc.code)
			}
			if sessionCookie(resp) != nil {
				t.Error("a refused caller got a cookie")
			}
		})
	}
}

func TestLocalLoginRequestChecks(t *testing.T) {
	_, pids := localLoginSetup(t, trustedTray())
	do := func(method, remote string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/local-login", nil)
		r.RemoteAddr = remote
		if csrf {
			r.Header.Set("X-Requested-With", "XMLHttpRequest")
		}
		w := httptest.NewRecorder()
		handleLocalLoginAPI(w, r)
		return w
	}
	if w := do("POST", lanClient.String(), true); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not_loopback") {
		t.Errorf("from the LAN: %d %s, want 403 not_loopback", w.Code, w.Body.String())
	}
	if w := do("POST", "[::1]:52431", true); w.Code != http.StatusForbidden {
		t.Errorf("from IPv6 loopback: %d, want 403", w.Code)
	}
	if len(*pids) != 0 {
		t.Errorf("non-loopback callers were looked up: %v", *pids)
	}
	if w := do("POST", trayEnd.String(), false); w.Code != http.StatusForbidden {
		t.Errorf("without X-Requested-With: %d, want 403", w.Code)
	}
	if w := do("GET", trayEnd.String(), true); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", w.Code)
	}

	// No secret: nothing needs a session, so no lookup and no cookie.
	setConfig(Config{Port: 5001})
	w := do("POST", lanClient.String(), true)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 0 {
		t.Errorf("without a secret: %d %v, want a plain 200", w.Code, w.Result().Cookies())
	}
}

func TestLocalLoginRateLimit(t *testing.T) {
	localLoginSetup(t, trustedTray())
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	localLoginNow = func() time.Time { return now }
	do := func() int {
		r := httptest.NewRequest("POST", "/api/local-login", nil)
		r.RemoteAddr = lanClient.String() // refused, but it still counts
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		w := httptest.NewRecorder()
		handleLocalLoginAPI(w, r)
		return w.Code
	}
	for i := 0; i < localLoginMax; i++ {
		if code := do(); code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d, want 403", i+1, code)
		}
	}
	if code := do(); code != http.StatusTooManyRequests {
		t.Errorf("attempt %d: %d, want 429", localLoginMax+1, code)
	}
	now = now.Add(localLoginWindow)
	if code := do(); code != http.StatusForbidden {
		t.Errorf("after the window: %d, want 403 again", code)
	}
}
