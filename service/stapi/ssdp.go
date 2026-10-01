package stapi

// SSDP discovery for the SmartThings Edge driver (docs/design/edge-driver.md
// §3.6, issue #69).
//
// The driver cannot ask the user for an IP address, so it multicasts an
// SSDP M-SEARCH on the LAN and this responder answers with a LOCATION
// pointing at GET /st/v1/description on the command port. That description
// is deliberately unauthenticated: it carries only what the driver needs to
// create the device (machine id, hostname, version, port) plus whether a
// secret is required, and never the secret itself.
//
// The responder has no switch (#95): discovery is the only way to add the
// device, so it runs for as long as the service does. Access control to
// /st/v1/* stays with the secret and smartthings.allowed_hubs (§3.1).
// Every M-SEARCH this PC matches is recorded (source IP and time) so the
// app can answer "did the hub's search ever reach me?" (§6.5).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/status"

	"golang.org/x/sys/windows"
)

const (
	// ssdpDeviceST is the search target the Edge driver sends and the one
	// this PC answers with (§3.6).
	ssdpDeviceST = "urn:smartthings-pc-control:device:pc:1"
	// ssdpSearchAll is the wildcard target every SSDP device answers.
	ssdpSearchAll = "ssdp:all"
	// ssdpGroup/SSDPPort are the SSDP multicast endpoint.
	ssdpGroup = "239.255.255.250"
	SSDPPort  = 1900
	// ssdpMaxAge is the CACHE-CONTROL lifetime of one response, in seconds.
	ssdpMaxAge = 1800
	// ssdpMaxMX caps the MX (maximum wait) a searcher can impose on us.
	ssdpMaxMX = 3 * time.Second
	// ssdpMaxPacket is generous for an M-SEARCH; anything longer is junk.
	ssdpMaxPacket = 2048
	// ssdpPerSourceInterval rate-limits answers to one searcher (§3.6 does
	// not ask for it, §3.1 does for every other inbound path). Hubs repeat
	// each M-SEARCH two or three times in a burst, so one answer per
	// second per source is plenty and keeps a flood cheap.
	ssdpPerSourceInterval = time.Second
)

// ssdpVerbose gates the per-response log line: answering every probe on the
// LAN would otherwise fill service.log. Set STPC_DEBUG in the service
// environment to see them.
var ssdpVerbose = os.Getenv("STPC_DEBUG") != ""

// ssdpState is the responder's lifecycle, its per-source limit and the
// last search it matched.
type ssdpState struct {
	limiter *ratelimit.Limiter

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	lastMu sync.RWMutex
	last   status.SSDPSearch
}

// ---- M-SEARCH parsing ------------------------------------------------------

// mSearch is the part of an M-SEARCH datagram this responder acts on.
type mSearch struct {
	// ST is the requested target, lower-cased: ssdpDeviceST or
	// ssdpSearchAll (nothing else reaches the caller).
	ST string
	// MX is the searcher's maximum wait in seconds, already clamped to
	// [0, ssdpMaxMX]. 0 means "answer at once".
	MX int
}

// parseMSearch reads one datagram. ok is false for anything that is not an
// M-SEARCH for a target this PC serves — a malformed packet, another
// device's search, or a NOTIFY from a neighbour — and such packets are
// silently dropped (§3.6).
func parseMSearch(pkt []byte) (mSearch, bool) {
	if len(pkt) == 0 || len(pkt) > ssdpMaxPacket {
		return mSearch{}, false
	}
	// SSDP is HTTPU: CRLF-delimited, but be tolerant of bare LF.
	lines := strings.Split(strings.ReplaceAll(string(pkt), "\r\n", "\n"), "\n")
	if !isMSearchRequestLine(lines[0]) {
		return mSearch{}, false
	}

	var out mSearch
	var st string
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue // blank line, body, or garbage: ignore
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "st":
			st = strings.ToLower(value)
		case "man":
			// The spec requires MAN: "ssdp:discover". A packet that
			// carries a different one is not a discovery request; a
			// packet that omits it is accepted (some stacks do).
			if strings.ToLower(strings.Trim(value, `"`)) != "ssdp:discover" {
				return mSearch{}, false
			}
		case "mx":
			// A non-numeric MX is ignored rather than fatal: the ST
			// still tells us an answer is wanted.
			if n, err := strconv.Atoi(value); err == nil {
				out.MX = clampMX(n)
			}
		}
	}
	if st != ssdpDeviceST && st != ssdpSearchAll {
		return mSearch{}, false
	}
	out.ST = st
	return out, true
}

// isMSearchRequestLine reports whether line is "M-SEARCH * HTTP/1.1"
// (case-insensitive, tolerant of extra spacing).
func isMSearchRequestLine(line string) bool {
	fields := strings.Fields(strings.TrimSpace(line))
	return len(fields) == 3 &&
		strings.EqualFold(fields[0], "M-SEARCH") &&
		fields[1] == "*" &&
		strings.HasPrefix(strings.ToUpper(fields[2]), "HTTP/1.")
}

// clampMX limits a searcher's MX to [0, ssdpMaxMX] seconds.
func clampMX(mx int) int {
	if mx < 0 {
		return 0
	}
	if max := int(ssdpMaxMX / time.Second); mx > max {
		return max
	}
	return mx
}

// ssdpDelay spreads the answer over the window the searcher allowed, so a
// LAN full of devices does not reply in the same millisecond (§3.6: honour
// MX, capped at ssdpMaxMX).
func (s *Server) ssdpDelay(mx int) time.Duration {
	if mx <= 0 {
		return 0
	}
	return time.Duration(s.randFloat() * float64(time.Duration(mx)*time.Second))
}

// ---- response --------------------------------------------------------------

// ssdpOSOnce caches the OS part of the SERVER header ("Windows/10.0.22631
// UPnP/1.0 smartthings-pc-control/v1.1.0"); the OS version never changes
// while the service runs, but the version is read on every answer.
var (
	ssdpOSOnce sync.Once
	ssdpOSName string
)

func ssdpOS() string {
	ssdpOSOnce.Do(func() {
		v := windows.RtlGetVersion()
		ssdpOSName = fmt.Sprintf("Windows/%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	})
	return ssdpOSName
}

// ssdpResponseText builds the unicast 200 OK for one M-SEARCH. localIP is
// the address of the interface the search arrived on, so the searcher gets
// a LOCATION it can actually reach; port is the live command port.
func (s *Server) ssdpResponseText(localIP net.IP, port int) string {
	location := "http://" + net.JoinHostPort(localIP.String(), strconv.Itoa(port)) + "/st/v1/description"
	headers := []string{
		"HTTP/1.1 200 OK",
		"CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge),
		"DATE: " + time.Now().UTC().Format(http.TimeFormat),
		"EXT:",
		"LOCATION: " + location,
		"SERVER: " + ssdpOS() + " UPnP/1.0 smartthings-pc-control/" + s.d.Version(),
		// Always the concrete target, never "ssdp:all": a responder
		// answers a wildcard search with what it actually is.
		"ST: " + ssdpDeviceST,
		"USN: uuid:" + s.d.Status.MachineID() + "::" + ssdpDeviceST,
	}
	// One trailing CRLF ends the last header, a second ends the message.
	return strings.Join(headers, "\r\n") + "\r\n\r\n"
}

// ---- last search (#95) -----------------------------------------------------

// NoteSSDPSearch records one M-SEARCH for a target this PC serves. It runs
// before the per-source rate limit on purpose: a hub repeats each search
// two or three times in a burst, and the diagnostic answers "when did a
// search last arrive", not "when did we last put a packet on the wire".
func (s *Server) NoteSSDPSearch(ip string) {
	s.ssdp.lastMu.Lock()
	s.ssdp.last = status.SSDPSearch{IP: ip, At: time.Now()}
	s.ssdp.lastMu.Unlock()
}

// LastSSDPSearch returns the last matched search; ok is false before the
// first one.
func (s *Server) LastSSDPSearch() (status.SSDPSearch, bool) {
	s.ssdp.lastMu.RLock()
	defer s.ssdp.lastMu.RUnlock()
	return s.ssdp.last, !s.ssdp.last.At.IsZero()
}

// ResetSSDPLastSearch forgets it (tests).
func (s *Server) ResetSSDPLastSearch() {
	s.ssdp.lastMu.Lock()
	s.ssdp.last = status.SSDPSearch{}
	s.ssdp.lastMu.Unlock()
}

// ---- per-source rate limit -------------------------------------------------

// ssdpAllow reports whether ip may be answered now: once per
// ssdpPerSourceInterval. Now is shared with the /st/v1 limiter so a test
// can drive both.
func (s *Server) ssdpAllow(ip string) bool {
	ok, _ := s.ssdp.limiter.Allow(ip)
	return ok
}

// ---- sockets ---------------------------------------------------------------

// ssdpSocket is one interface's pair of sockets: recv is joined to the SSDP
// group, send carries the unicast answers. They are the same socket only
// when a dedicated sender could not be opened (or in tests).
type ssdpSocket struct {
	name    string
	localIP net.IP
	recv    *net.UDPConn
	send    *net.UDPConn
}

func (s *ssdpSocket) close() {
	if s.send != nil && s.send != s.recv {
		s.send.Close()
	}
	s.recv.Close()
}

// openSSDPSockets joins 239.255.255.250:1900 on every usable IPv4
// interface. A LAN adapter that refuses the join (a VPN tap, a disabled
// virtual switch) is logged and skipped; only "not one interface worked"
// is an error.
func openSSDPSockets() ([]*ssdpSocket, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	group := &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: SSDPPort}
	var out []*ssdpSocket
	for i := range ifaces {
		ifi := ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		ip := firstIPv4(&ifi)
		if ip == nil {
			continue // IPv6-only or no address yet
		}
		recv, err := net.ListenMulticastUDP("udp4", &ifi, group)
		if err != nil {
			logx.Printf("SSDP: %s did not join %s: %v", ifi.Name, ssdpGroup, err)
			continue
		}
		// The receiving socket is bound to the group address, which is
		// not a valid source for a unicast reply, so answers go out on a
		// second socket bound to this interface's own address.
		send, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0})
		if err != nil {
			logx.Printf("SSDP: %s has no unicast sender (%v); replying from the group socket", ifi.Name, err)
			send = recv
		}
		out = append(out, &ssdpSocket{name: ifi.Name, localIP: ip, recv: recv, send: send})
	}
	if len(out) == 0 {
		return nil, errors.New("no multicast-capable IPv4 interface")
	}
	return out, nil
}

// firstIPv4 returns the interface's first usable IPv4 address.
func firstIPv4(ifi *net.Interface) net.IP {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() && !ip4.IsLoopback() {
			return ip4
		}
	}
	return nil
}

// ---- lifecycle -------------------------------------------------------------

// StartSSDP starts the responder. Called once at service start; starting
// twice is a no-op, so it is safe to repeat.
func (s *Server) StartSSDP() {
	s.ssdp.mu.Lock()
	defer s.ssdp.mu.Unlock()
	if s.ssdp.running {
		return
	}
	s.startSSDPLocked()
}

// StopSSDP stops the responder (if any).
func (s *Server) StopSSDP() {
	s.ssdp.mu.Lock()
	defer s.ssdp.mu.Unlock()
	s.stopSSDPLocked()
}

// SSDPRunning reports whether the responder is listening.
func (s *Server) SSDPRunning() bool {
	s.ssdp.mu.Lock()
	defer s.ssdp.mu.Unlock()
	return s.ssdp.running
}

// startSSDPLocked opens the sockets and serves them. ssdp.mu is held. A
// failure leaves running false, which the app reports as "검색 응답기
// 꺼짐" so the user is not left waiting for a search that can never land.
func (s *Server) startSSDPLocked() {
	socks, err := s.openSSDP()
	if err != nil {
		logx.Printf("SSDP: no socket could be opened, this PC will not answer searches: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	names := make([]string, 0, len(socks))
	for _, sock := range socks {
		names = append(names, fmt.Sprintf("%s (%s)", sock.name, sock.localIP))
		wg.Add(1)
		go s.serveSSDP(ctx, sock, &wg)
	}
	go func() {
		<-ctx.Done()
		// Closing unblocks the readers; the delayed answers watch ctx.
		for _, sock := range socks {
			sock.close()
		}
		wg.Wait()
		close(done)
	}()
	s.ssdp.running, s.ssdp.cancel, s.ssdp.done = true, cancel, done
	logx.Printf("SSDP: discovery responder started on %s", strings.Join(names, ", "))
}

// stopSSDPLocked cancels the responder and waits briefly for it. ssdp.mu
// is held.
func (s *Server) stopSSDPLocked() {
	if s.ssdp.cancel == nil {
		s.ssdp.running = false
		return
	}
	s.ssdp.cancel()
	select {
	case <-s.ssdp.done:
	case <-time.After(5 * time.Second):
		logx.Printf("SSDP: responder did not stop in time")
	}
	s.ssdp.running, s.ssdp.cancel, s.ssdp.done = false, nil, nil
	s.ssdp.limiter.Reset()
	logx.Printf("SSDP: discovery responder stopped")
}

// serveSSDP reads M-SEARCH datagrams from one socket until ctx is
// cancelled (which closes the socket and fails the read).
func (s *Server) serveSSDP(ctx context.Context, sock *ssdpSocket, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, ssdpMaxPacket)
	for {
		n, src, err := sock.recv.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				logx.Printf("SSDP: %s read failed: %v", sock.name, err)
			}
			return
		}
		req, ok := parseMSearch(buf[:n])
		if !ok {
			continue
		}
		if src == nil {
			continue
		}
		// The packet was for us; record it even if the burst limiter
		// below drops this particular repeat (#95).
		s.NoteSSDPSearch(src.IP.String())
		if !s.ssdpAllow(src.IP.String()) {
			continue
		}
		wg.Add(1)
		go s.answerSSDP(ctx, sock, *src, req, wg)
	}
}

// answerSSDP waits out the searcher's MX window and sends one unicast
// reply. A late cancel simply drops the answer.
func (s *Server) answerSSDP(ctx context.Context, sock *ssdpSocket, dst net.UDPAddr, req mSearch, wg *sync.WaitGroup) {
	defer wg.Done()
	if d := s.ssdpDelay(req.MX); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
	if ctx.Err() != nil {
		return
	}
	resp := s.ssdpResponseText(sock.localIP, s.d.Config().Port)
	if _, err := sock.send.WriteToUDP([]byte(resp), &dst); err != nil {
		if ctx.Err() == nil {
			logx.Printf("SSDP: reply to %s failed: %v", dst.IP, err)
		}
		return
	}
	if ssdpVerbose {
		logx.Printf("SSDP: answered %s (ST %s) from %s", dst.IP, req.ST, sock.localIP)
	}
}

// ---- GET /st/v1/description (§3.6) -----------------------------------------

// description is the unauthenticated discovery document. It holds only
// what the driver needs to create the device plus secret_set, which tells
// it whether to ask the user for the secret. The secret itself is never
// part of it.
type description struct {
	Protocol       int    `json:"protocol"`
	MachineID      string `json:"machine_id"`
	Hostname       string `json:"hostname"`
	ServiceVersion string `json:"service_version"`
	Port           int    `json:"port"`
	SecretSet      bool   `json:"secret_set"`
}

// handleDescription serves GET /st/v1/description. Unlike the rest of
// /st/v1 it is not wrapped in auth — an unconfigured driver has no secret
// yet — but it keeps the per-source rate limit (§3.1) and never touches the
// hub-seen state, so an anonymous probe cannot make the app claim a hub is
// connected.
func (s *Server) handleDescription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allow(httpx.RemoteHost(r.RemoteAddr)) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	cfg := s.d.Config() // live: a config save takes effect without a restart
	httpx.WriteJSON(w, http.StatusOK, description{
		Protocol:       Protocol,
		MachineID:      s.d.Status.MachineID(),
		Hostname:       s.d.Status.Hostname(),
		ServiceVersion: s.d.Version(),
		Port:           cfg.Port,
		SecretSet:      cfg.Secret != "",
	})
}
