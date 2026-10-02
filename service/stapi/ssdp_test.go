package stapi

// Tests for SSDP discovery (edge-driver doc §3.6, #69). Nothing here
// joins a multicast group: the responder's socket factory is replaced with
// a loopback pair, so the tests run on a build agent with no LAN.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

// mSearchPacket builds a CRLF datagram with the given headers.
func mSearchPacket(lines ...string) []byte {
	return []byte(strings.Join(append([]string{"M-SEARCH * HTTP/1.1"}, lines...), "\r\n") + "\r\n\r\n")
}

func TestParseMSearchAcceptsOurTargets(t *testing.T) {
	cases := []struct {
		name string
		st   string
		want string
	}{
		{"device type", ssdpDeviceST, ssdpDeviceST},
		{"uppercase device type", strings.ToUpper(ssdpDeviceST), ssdpDeviceST},
		{"wildcard", "ssdp:all", ssdpSearchAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseMSearch(mSearchPacket(
				"HOST: 239.255.255.250:1900",
				`MAN: "ssdp:discover"`,
				"MX: 2",
				"ST: "+tc.st,
			))
			if !ok {
				t.Fatalf("ST %q was not accepted", tc.st)
			}
			if got.ST != tc.want {
				t.Errorf("ST = %q, want %q", got.ST, tc.want)
			}
			if got.MX != 2 {
				t.Errorf("MX = %d, want 2", got.MX)
			}
		})
	}
}

func TestParseMSearchIgnoresOtherPackets(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
	}{
		{"another device's search", mSearchPacket(`MAN: "ssdp:discover"`, "ST: urn:schemas-upnp-org:device:MediaRenderer:1")},
		{"root device search", mSearchPacket(`MAN: "ssdp:discover"`, "ST: upnp:rootdevice")},
		{"no ST at all", mSearchPacket(`MAN: "ssdp:discover"`, "MX: 1")},
		{"wrong MAN", mSearchPacket(`MAN: "ssdp:wrong"`, "ST: "+ssdpDeviceST)},
		{"NOTIFY, not a search", []byte("NOTIFY * HTTP/1.1\r\nNT: " + ssdpDeviceST + "\r\n\r\n")},
		{"empty", []byte{}},
		{"binary junk", []byte{0x00, 0x01, 0x02, 0xff}},
		{"oversized", make([]byte, ssdpMaxPacket+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseMSearch(tc.pkt); ok {
				t.Error("packet should have been ignored")
			}
		})
	}
}

func TestParseMSearchMX(t *testing.T) {
	mx := func(header string) int {
		t.Helper()
		got, ok := parseMSearch(mSearchPacket(header, "ST: "+ssdpDeviceST))
		if !ok {
			t.Fatalf("packet with %q was rejected", header)
		}
		return got.MX
	}
	if got := mx("MX: 1"); got != 1 {
		t.Errorf("MX: 1 -> %d", got)
	}
	if got := mx("MX:   5  "); got != int(ssdpMaxMX/time.Second) {
		t.Errorf("MX: 5 -> %d, want it clamped to %d", got, int(ssdpMaxMX/time.Second))
	}
	if got := mx("MX: -1"); got != 0 {
		t.Errorf("MX: -1 -> %d, want 0", got)
	}
	if got := mx("MX: soon"); got != 0 {
		t.Errorf("non-numeric MX -> %d, want 0 (and the search still answered)", got)
	}
	if got := mx("HOST: 239.255.255.250:1900"); got != 0 {
		t.Errorf("missing MX -> %d, want 0", got)
	}
}

func TestParseMSearchToleratesBareLF(t *testing.T) {
	pkt := []byte("m-search * HTTP/1.1\nMAN: ssdp:discover\nST: " + ssdpDeviceST + "\n\n")
	if _, ok := parseMSearch(pkt); !ok {
		t.Error("an LF-delimited M-SEARCH was rejected")
	}
}

func TestSSDPDelayHonoursMX(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})

	if got := s.ssdpDelay(0); got != 0 {
		t.Errorf("MX 0 -> %v, want no delay", got)
	}
	s.randFloat = func() float64 { return 0.5 }
	if got := s.ssdpDelay(2); got != time.Second {
		t.Errorf("MX 2 -> %v, want 1s", got)
	}
	s.randFloat = func() float64 { return 0.999 }
	if got := s.ssdpDelay(clampMX(120)); got > ssdpMaxMX {
		t.Errorf("MX 120 -> %v, want at most %v", got, ssdpMaxMX)
	}
}

// ssdpHeaders splits a response into its start line and a header map.
func ssdpHeaders(t *testing.T, resp string) (string, map[string]string) {
	t.Helper()
	if !strings.HasSuffix(resp, "\r\n\r\n") {
		t.Fatalf("response is not CRLF-terminated: %q", resp)
	}
	lines := strings.Split(strings.TrimSuffix(resp, "\r\n\r\n"), "\r\n")
	out := map[string]string{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("header line without a colon: %q", line)
		}
		out[strings.ToUpper(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return lines[0], out
}

func TestSSDPResponseText(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001}) // version v1.1.0-test

	start, h := ssdpHeaders(t, s.ssdpResponseText(net.IPv4(192, 168, 1, 77), 5001))

	if start != "HTTP/1.1 200 OK" {
		t.Errorf("start line = %q", start)
	}
	if h["CACHE-CONTROL"] != "max-age=1800" {
		t.Errorf("CACHE-CONTROL = %q", h["CACHE-CONTROL"])
	}
	if _, ok := h["EXT"]; !ok {
		t.Error("EXT header missing")
	}
	if h["ST"] != ssdpDeviceST {
		t.Errorf("ST = %q, want the device type even for a wildcard search", h["ST"])
	}
	if want := "uuid:" + testMachineID + "::" + ssdpDeviceST; h["USN"] != want {
		t.Errorf("USN = %q, want %q", h["USN"], want)
	}
	// LOCATION must carry the interface the search arrived on, so the hub
	// can reach it, and the live command port.
	if want := "http://192.168.1.77:5001/st/v1/description"; h["LOCATION"] != want {
		t.Errorf("LOCATION = %q, want %q", h["LOCATION"], want)
	}
	if !strings.HasPrefix(h["SERVER"], "Windows/") || !strings.Contains(h["SERVER"], "smartthings-pc-control/v1.1.0-test") {
		t.Errorf("SERVER = %q", h["SERVER"])
	}
	if _, err := time.Parse(http.TimeFormat, h["DATE"]); err != nil {
		t.Errorf("DATE = %q: %v", h["DATE"], err)
	}
}

func TestSSDPResponseUsesTheGivenPort(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})
	_, h := ssdpHeaders(t, s.ssdpResponseText(net.IPv4(10, 0, 0, 5), 41234))
	if want := "http://10.0.0.5:41234/st/v1/description"; h["LOCATION"] != want {
		t.Errorf("LOCATION = %q, want %q", h["LOCATION"], want)
	}
}

func TestSSDPAllowOncePerSecond(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})
	now := time.Now()
	s.Now = func() time.Time { return now }

	if !s.ssdpAllow("192.168.1.20") {
		t.Fatal("the first search from a source must be answered")
	}
	if s.ssdpAllow("192.168.1.20") {
		t.Error("a repeat within a second must be dropped")
	}
	if !s.ssdpAllow("192.168.1.21") {
		t.Error("another source must not share the first one's budget")
	}
	now = now.Add(ssdpPerSourceInterval + time.Millisecond)
	if !s.ssdpAllow("192.168.1.20") {
		t.Error("the source must be answered again after the interval")
	}
}

// loopbackSSDP replaces the socket factory with a single loopback socket so
// the responder can be started without multicast. It returns the address
// the test sends its M-SEARCH to.
func loopbackSSDP(t *testing.T, s *Server) *net.UDPAddr {
	t.Helper()
	var (
		mu   sync.Mutex
		addr *net.UDPAddr
		open = s.openSSDP
	)
	s.openSSDP = func() ([]*ssdpSocket, error) {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			return nil, err
		}
		mu.Lock()
		addr = c.LocalAddr().(*net.UDPAddr)
		mu.Unlock()
		return []*ssdpSocket{{name: "loopback", localIP: net.IPv4(127, 0, 0, 1), recv: c, send: c}}, nil
	}
	t.Cleanup(func() {
		s.StopSSDP()
		s.openSSDP = open
	})

	s.StartSSDP()
	mu.Lock()
	defer mu.Unlock()
	if addr == nil {
		t.Fatal("the responder did not open a socket")
	}
	return addr
}

// TestSSDPLifecycleHasNoSwitch guards #95: the responder runs for as long
// as the service does, and start/stop are idempotent.
func TestSSDPLifecycleHasNoSwitch(t *testing.T) {
	// No smartthings settings at all: nothing in the config can suppress
	// the responder any more.
	s := newTestServer(t, config.Config{Port: 5001})
	loopbackSSDP(t, s)

	if !s.SSDPRunning() {
		t.Fatal("the responder must start with the service")
	}
	// A second start must not leak a socket or a goroutine.
	s.StartSSDP()
	if !s.SSDPRunning() {
		t.Fatal("starting twice stopped the responder")
	}

	s.StopSSDP()
	if s.SSDPRunning() {
		t.Fatal("still running after stopSSDP")
	}
	s.StopSSDP() // stopping twice is safe

	s.StartSSDP()
	if !s.SSDPRunning() {
		t.Fatal("the responder did not restart")
	}
}

// TestSSDPStartOpensOnlyOnce keeps a repeated start from opening a second
// socket set (the old reconcile path had the same guarantee).
func TestSSDPStartOpensOnlyOnce(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})
	loopbackSSDP(t, s)

	opened := false
	s.openSSDP = func() ([]*ssdpSocket, error) {
		opened = true
		return nil, fmt.Errorf("must not be called")
	}

	s.StartSSDP()
	if opened {
		t.Error("startSSDP opened a second socket set while already running")
	}
}

// Loopback UDP is not lossless: on Windows a datagram now and then never
// reaches the reader (seen about once in a few thousand runs, either way
// round). The tests below therefore search again, as a hub would, instead
// of waiting on one packet; ssdpTries bounds that, ssdpTryWait is how long
// each try waits.
const (
	ssdpTries   = 20
	ssdpTryWait = 250 * time.Millisecond
)

// frozenSSDPClock stops the rate limiter's clock so a test decides which
// searches fall into the same one-per-second window; advance moves it on.
// Call it before the responder starts.
func frozenSSDPClock(s *Server) (advance func(time.Duration)) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
}

// searchUntilSeen clears the last search and calls send until the
// responder records one, logging each try that was lost.
func searchUntilSeen(t *testing.T, s *Server, what string, send func()) {
	t.Helper()
	s.ResetSSDPLastSearch()
	for try := 0; try < ssdpTries; try++ {
		send()
		if waitSSDPSearch(s, ssdpTryWait) {
			return
		}
		t.Logf("%s: try %d was lost, searching again", what, try+1)
	}
	t.Fatalf("the responder never saw the %s in %d tries", what, ssdpTries)
}

// waitSSDPSearch reports whether the responder records a search within d.
func waitSSDPSearch(s *Server, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if _, ok := s.LastSSDPSearch(); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSSDPResponderAnswersOnLoopback(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})
	// No MX jitter: the answer goes out at once.
	s.randFloat = func() float64 { return 0 }
	advance := frozenSSDPClock(s)
	dst := loopbackSSDP(t, s)

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	send := func(lines ...string) {
		t.Helper()
		if _, err := client.WriteToUDP(mSearchPacket(append([]string{`MAN: "ssdp:discover"`}, lines...)...), dst); err != nil {
			t.Fatal(err)
		}
	}

	// A packet the responder must ignore, then one it must answer. Both go
	// out before the read, so a reply to the first would arrive first. Each
	// try is a new rate-limit window, so a lost try does not block the next.
	buf := make([]byte, ssdpMaxPacket)
	reply := ""
	for try := 0; try < ssdpTries && reply == ""; try++ {
		advance(2 * ssdpPerSourceInterval)
		send("ST: upnp:rootdevice")
		send("MX: 1", "ST: "+ssdpDeviceST)
		client.SetReadDeadline(time.Now().Add(ssdpTryWait))
		n, _, err := client.ReadFromUDP(buf)
		if err != nil {
			t.Logf("try %d got no reply (%v), searching again", try+1, err)
			continue
		}
		reply = string(buf[:n])
	}
	if reply == "" {
		t.Fatalf("no reply to %d searches", ssdpTries)
	}
	start, h := ssdpHeaders(t, reply)
	if start != "HTTP/1.1 200 OK" {
		t.Errorf("start line = %q", start)
	}
	if h["ST"] != ssdpDeviceST {
		t.Errorf("ST = %q", h["ST"])
	}
	if want := "http://127.0.0.1:5001/st/v1/description"; h["LOCATION"] != want {
		t.Errorf("LOCATION = %q, want %q", h["LOCATION"], want)
	}

	// The repeat a hub sends straight after is dropped by the rate limit.
	// A fresh window first takes one search (the first one the responder
	// sees in it is answered) ...
	advance(2 * ssdpPerSourceInterval)
	searchUntilSeen(t, s, "first search", func() { send("ST: " + ssdpDeviceST) })
	// ... then the repeat comes from a second socket on the same IP (the
	// limiter's key), so no late answer to the client above can land on
	// it. It is resent until the responder has seen it, so the silence
	// below is the limiter and not a lost packet.
	repeat, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer repeat.Close()
	searchUntilSeen(t, s, "repeat", func() {
		if _, err := repeat.WriteToUDP(mSearchPacket(`MAN: "ssdp:discover"`, "ST: "+ssdpDeviceST), dst); err != nil {
			t.Fatal(err)
		}
	})
	repeat.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := repeat.ReadFromUDP(buf); err == nil {
		t.Errorf("a repeat within a second was answered: %q", buf[:n])
	}
}

func TestServeSSDPStopsWhenTheSocketCloses(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	sock := &ssdpSocket{name: "loopback", localIP: net.IPv4(127, 0, 0, 1), recv: c, send: c}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go newTestServer(t, config.Config{Port: 5001}).serveSSDP(ctx, sock, &wg)

	cancel()
	sock.close()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveSSDP did not return after its socket closed")
	}
}

func TestNoteSSDPSearch(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})

	if _, ok := s.LastSSDPSearch(); ok {
		t.Fatal("a search is reported before any arrived")
	}
	s.NoteSSDPSearch("192.168.1.105")
	got, ok := s.LastSSDPSearch()
	if !ok || got.IP != "192.168.1.105" {
		t.Fatalf("lastSSDPSearch = %+v, ok=%v", got, ok)
	}
	if time.Since(got.At) > time.Minute {
		t.Errorf("last search time = %v", got.At)
	}
	// The newest search wins.
	s.NoteSSDPSearch("10.0.0.2")
	if got, _ := s.LastSSDPSearch(); got.IP != "10.0.0.2" {
		t.Errorf("lastSSDPSearch = %+v, want the newer source", got)
	}
}

// TestServeSSDPRecordsTheSearch checks the bookkeeping from the serve loop:
// a probe for another device leaves it alone, one for this PC records the
// source, and a burst repeat the rate limit drops still refreshes it.
func TestServeSSDPRecordsTheSearch(t *testing.T) {
	s := newTestServer(t, config.Config{Port: 5001})
	s.randFloat = func() float64 { return 0 }
	frozenSSDPClock(s)
	dst := loopbackSSDP(t, s)

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	send := func(st string) {
		t.Helper()
		pkt := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 0\r\nST: " + st + "\r\n\r\n"
		if _, err := client.WriteToUDP([]byte(pkt), dst); err != nil {
			t.Fatal(err)
		}
	}

	send("urn:schemas-upnp-org:device:InternetGatewayDevice:1")
	// A packet for someone else must never show up as "a search arrived".
	time.Sleep(100 * time.Millisecond)
	if got, ok := s.LastSSDPSearch(); ok {
		t.Fatalf("another device's search was recorded: %+v", got)
	}

	// Searches are resent until one lands (see ssdpTries). With the clock
	// frozen, every one after the first the responder sees is a burst
	// repeat the per-source limiter drops.
	for _, what := range []string{"M-SEARCH", "burst repeat"} {
		searchUntilSeen(t, s, what, func() { send(ssdpDeviceST) })
		if got, _ := s.LastSSDPSearch(); got.IP != "127.0.0.1" {
			t.Errorf("%s: last search = %+v, want 127.0.0.1", what, got)
		}
	}
}
