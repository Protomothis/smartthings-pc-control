package service

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
// The responder follows config smartthings.discovery (§3.7) without a
// restart: saveConfig calls reconcileSSDP after every save.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// ssdpDeviceST is the search target the Edge driver sends and the one
	// this PC answers with (§3.6).
	ssdpDeviceST = "urn:smartthings-pc-control:device:pc:1"
	// ssdpSearchAll is the wildcard target every SSDP device answers.
	ssdpSearchAll = "ssdp:all"
	// ssdpGroup/ssdpPort are the SSDP multicast endpoint.
	ssdpGroup = "239.255.255.250"
	ssdpPort  = 1900
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

// ssdpRandFloat is rand.Float64, replaced by tests that want a fixed delay.
var ssdpRandFloat = rand.Float64

// ssdpDelay spreads the answer over the window the searcher allowed, so a
// LAN full of devices does not reply in the same millisecond (§3.6: honour
// MX, capped at ssdpMaxMX).
func ssdpDelay(mx int) time.Duration {
	if mx <= 0 {
		return 0
	}
	return time.Duration(ssdpRandFloat() * float64(time.Duration(mx)*time.Second))
}

// ---- response --------------------------------------------------------------

// ssdpServerOnce caches the SERVER header ("Windows/10.0.22631 UPnP/1.0
// smartthings-pc-control/v1.1.0"); the OS version never changes while the
// service runs, but Version is a package var tests replace, so only the
// OS part is cached.
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
func ssdpResponseText(localIP net.IP, port int) string {
	location := "http://" + net.JoinHostPort(localIP.String(), strconv.Itoa(port)) + "/st/v1/description"
	headers := []string{
		"HTTP/1.1 200 OK",
		"CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge),
		"DATE: " + time.Now().UTC().Format(http.TimeFormat),
		"EXT:",
		"LOCATION: " + location,
		"SERVER: " + ssdpOS() + " UPnP/1.0 smartthings-pc-control/" + Version,
		// Always the concrete target, never "ssdp:all": a responder
		// answers a wildcard search with what it actually is.
		"ST: " + ssdpDeviceST,
		"USN: uuid:" + machineID() + "::" + ssdpDeviceST,
	}
	// One trailing CRLF ends the last header, a second ends the message.
	return strings.Join(headers, "\r\n") + "\r\n\r\n"
}

// ---- per-source rate limit -------------------------------------------------

var (
	ssdpSeen   = map[string]time.Time{}
	ssdpSeenMu sync.Mutex
)

// ssdpAllow reports whether ip may be answered now: at most one response
// per ssdpPerSourceInterval. stNow is shared with the /st/v1 limiter so a
// test can drive both.
func ssdpAllow(ip string) bool {
	now := stNow()
	ssdpSeenMu.Lock()
	defer ssdpSeenMu.Unlock()
	if last, ok := ssdpSeen[ip]; ok && now.Sub(last) < ssdpPerSourceInterval {
		return false
	}
	// Probing sources come and go; drop the stale entries rather than
	// letting the map grow with every host that ever searched.
	if len(ssdpSeen) > 256 {
		for k, v := range ssdpSeen {
			if now.Sub(v) > time.Minute {
				delete(ssdpSeen, k)
			}
		}
	}
	ssdpSeen[ip] = now
	return true
}

// resetSSDPRateLimit drops every source (tests).
func resetSSDPRateLimit() {
	ssdpSeenMu.Lock()
	ssdpSeen = map[string]time.Time{}
	ssdpSeenMu.Unlock()
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

// ssdpOpen opens the responder's sockets. Tests replace it with a single
// loopback socket so they need no multicast-capable interface.
var ssdpOpen = openSSDPSockets

// openSSDPSockets joins 239.255.255.250:1900 on every usable IPv4
// interface. A LAN adapter that refuses the join (a VPN tap, a disabled
// virtual switch) is logged and skipped; only "not one interface worked"
// is an error.
func openSSDPSockets() ([]*ssdpSocket, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	group := &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: ssdpPort}
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
			logMsg("SSDP: %s did not join %s: %v", ifi.Name, ssdpGroup, err)
			continue
		}
		// The receiving socket is bound to the group address, which is
		// not a valid source for a unicast reply, so answers go out on a
		// second socket bound to this interface's own address.
		send, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0})
		if err != nil {
			logMsg("SSDP: %s has no unicast sender (%v); replying from the group socket", ifi.Name, err)
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

// ssdpRunner owns the responder goroutines. managed is set between
// startSSDP and stopSSDP so that saveConfig in the installer or in tests
// never opens a socket.
type ssdpRunner struct {
	mu      sync.Mutex
	managed bool
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

var ssdpR ssdpRunner

// startSSDP enables the lifecycle and starts the responder when the live
// config asks for it. Called once at service start.
func startSSDP() {
	ssdpR.mu.Lock()
	ssdpR.managed = true
	ssdpR.mu.Unlock()
	reconcileSSDP()
}

// stopSSDP stops the responder (if any) and disables the lifecycle.
func stopSSDP() {
	ssdpR.mu.Lock()
	defer ssdpR.mu.Unlock()
	ssdpR.managed = false
	ssdpR.stopLocked()
}

// reconcileSSDP starts or stops the responder so it matches
// smartthings.discovery (§3.7). saveConfig calls it after every save; it is
// a no-op until startSSDP has run, and safe to call repeatedly.
func reconcileSSDP() {
	ssdpR.mu.Lock()
	defer ssdpR.mu.Unlock()
	if !ssdpR.managed {
		return
	}
	want := getConfig().SmartThings.Discovery
	if want == ssdpR.running {
		return
	}
	if !want {
		ssdpR.stopLocked()
		return
	}
	ssdpR.startLocked()
}

// ssdpRunning reports whether the responder is listening.
func ssdpRunning() bool {
	ssdpR.mu.Lock()
	defer ssdpR.mu.Unlock()
	return ssdpR.running
}

// startLocked opens the sockets and serves them. ssdpR.mu is held. A
// failure leaves running false so the next reconcile retries.
func (r *ssdpRunner) startLocked() {
	socks, err := ssdpOpen()
	if err != nil {
		logMsg("SSDP: discovery is on but no socket could be opened: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	names := make([]string, 0, len(socks))
	for _, s := range socks {
		names = append(names, fmt.Sprintf("%s (%s)", s.name, s.localIP))
		wg.Add(1)
		go serveSSDP(ctx, s, &wg)
	}
	go func() {
		<-ctx.Done()
		// Closing unblocks the readers; the delayed answers watch ctx.
		for _, s := range socks {
			s.close()
		}
		wg.Wait()
		close(done)
	}()
	r.running, r.cancel, r.done = true, cancel, done
	logMsg("SSDP: discovery responder started on %s", strings.Join(names, ", "))
}

// stopLocked cancels the responder and waits briefly for it. ssdpR.mu is held.
func (r *ssdpRunner) stopLocked() {
	if r.cancel == nil {
		r.running = false
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		logMsg("SSDP: responder did not stop in time")
	}
	r.running, r.cancel, r.done = false, nil, nil
	resetSSDPRateLimit()
	logMsg("SSDP: discovery responder stopped")
}

// serveSSDP reads M-SEARCH datagrams from one socket until ctx is
// cancelled (which closes the socket and fails the read).
func serveSSDP(ctx context.Context, s *ssdpSocket, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, ssdpMaxPacket)
	for {
		n, src, err := s.recv.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				logMsg("SSDP: %s read failed: %v", s.name, err)
			}
			return
		}
		req, ok := parseMSearch(buf[:n])
		if !ok {
			continue
		}
		if src == nil || !ssdpAllow(src.IP.String()) {
			continue
		}
		wg.Add(1)
		go answerSSDP(ctx, s, *src, req, wg)
	}
}

// answerSSDP waits out the searcher's MX window and sends one unicast
// reply. A late cancel simply drops the answer.
func answerSSDP(ctx context.Context, s *ssdpSocket, dst net.UDPAddr, req mSearch, wg *sync.WaitGroup) {
	defer wg.Done()
	if d := ssdpDelay(req.MX); d > 0 {
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
	resp := ssdpResponseText(s.localIP, getConfig().Port)
	if _, err := s.send.WriteToUDP([]byte(resp), &dst); err != nil {
		if ctx.Err() == nil {
			logMsg("SSDP: reply to %s failed: %v", dst.IP, err)
		}
		return
	}
	if ssdpVerbose {
		logMsg("SSDP: answered %s (ST %s) from %s", dst.IP, req.ST, s.localIP)
	}
}

// ---- GET /st/v1/description (§3.6) -----------------------------------------

// stDescription is the unauthenticated discovery document. It holds only
// what the driver needs to create the device plus secret_set, which tells
// it whether to ask the user for the secret. The secret itself is never
// part of it.
type stDescription struct {
	Protocol       int    `json:"protocol"`
	MachineID      string `json:"machine_id"`
	Hostname       string `json:"hostname"`
	ServiceVersion string `json:"service_version"`
	Port           int    `json:"port"`
	SecretSet      bool   `json:"secret_set"`
}

// handleSTDescription serves GET /st/v1/description. Unlike the rest of
// /st/v1 it is not wrapped in stAuth — an unconfigured driver has no secret
// yet — but it keeps the per-source rate limit (§3.1) and never touches the
// hub-seen state, so an anonymous probe cannot make the GUI claim a hub is
// connected.
func handleSTDescription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !stAllow(remoteHost(r.RemoteAddr)) {
		w.Header().Set("Retry-After", "1")
		stError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	cfg := getConfig() // live: a config save takes effect without a restart
	writeJSON(w, http.StatusOK, stDescription{
		Protocol:       stProtocol,
		MachineID:      machineID(),
		Hostname:       hostname(),
		ServiceVersion: Version,
		Port:           cfg.Port,
		SecretSet:      cfg.Secret != "",
	})
}

// registerSTDescriptionRoute mounts the description route on the /st/v1
// mux. It is separate from stHandler so #69 adds a single line there.
func registerSTDescriptionRoute(mux *http.ServeMux) {
	mux.HandleFunc("/st/v1/description", handleSTDescription)
}
