package service

// Tests for SSDP discovery and GET /st/v1/description (edge-driver doc
// §3.6, #69). Nothing here joins a multicast group: the responder's socket
// factory is replaced with a loopback pair, so the tests run on a build
// agent with no LAN.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- M-SEARCH parsing ------------------------------------------------------

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
	orig := ssdpRandFloat
	t.Cleanup(func() { ssdpRandFloat = orig })

	if got := ssdpDelay(0); got != 0 {
		t.Errorf("MX 0 -> %v, want no delay", got)
	}
	ssdpRandFloat = func() float64 { return 0.5 }
	if got := ssdpDelay(2); got != time.Second {
		t.Errorf("MX 2 -> %v, want 1s", got)
	}
	ssdpRandFloat = func() float64 { return 0.999 }
	if got := ssdpDelay(clampMX(120)); got > ssdpMaxMX {
		t.Errorf("MX 120 -> %v, want at most %v", got, ssdpMaxMX)
	}
}

// ---- response formatting ---------------------------------------------------

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
	origVersion := Version
	Version = "v1.1.0-test"
	t.Cleanup(func() { Version = origVersion })

	start, h := ssdpHeaders(t, ssdpResponseText(net.IPv4(192, 168, 1, 77), 5001))

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
	if want := "uuid:" + machineID() + "::" + ssdpDeviceST; h["USN"] != want {
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
	_, h := ssdpHeaders(t, ssdpResponseText(net.IPv4(10, 0, 0, 5), 41234))
	if want := "http://10.0.0.5:41234/st/v1/description"; h["LOCATION"] != want {
		t.Errorf("LOCATION = %q, want %q", h["LOCATION"], want)
	}
}

// ---- per-source rate limit -------------------------------------------------

func TestSSDPAllowOncePerSecond(t *testing.T) {
	origNow := stNow
	now := time.Now()
	stNow = func() time.Time { return now }
	resetSSDPRateLimit()
	t.Cleanup(func() {
		stNow = origNow
		resetSSDPRateLimit()
	})

	if !ssdpAllow("192.168.1.20") {
		t.Fatal("the first search from a source must be answered")
	}
	if ssdpAllow("192.168.1.20") {
		t.Error("a repeat within a second must be dropped")
	}
	if !ssdpAllow("192.168.1.21") {
		t.Error("another source must not share the first one's budget")
	}
	now = now.Add(ssdpPerSourceInterval + time.Millisecond)
	if !ssdpAllow("192.168.1.20") {
		t.Error("the source must be answered again after the interval")
	}
}

// ---- GET /st/v1/description ------------------------------------------------

func TestSTDescriptionNeedsNoSecret(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "topsecret"})
	// Earlier tests authenticated as a hub; start from a clean slate so
	// the "an anonymous probe is not a hub" check below means something.
	hubLastSeenMu.Lock()
	prevHub := hubLastSeen
	hubLastSeen = hubSeen{}
	hubLastSeenMu.Unlock()
	t.Cleanup(func() {
		hubLastSeenMu.Lock()
		hubLastSeen = prevHub
		hubLastSeenMu.Unlock()
	})

	// No X-PC-Secret at all: an unconfigured driver has none yet (§3.6).
	r := httptest.NewRequest(http.MethodGet, "/st/v1/description", nil)
	r.RemoteAddr = "192.168.1.20:51234"
	w := httptest.NewRecorder()
	stHandler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := stJSON(t, w)
	if got["protocol"] != float64(stProtocol) {
		t.Errorf("protocol = %v", got["protocol"])
	}
	if got["machine_id"] != machineID() || got["hostname"] != hostname() {
		t.Errorf("identity = %v / %v", got["machine_id"], got["hostname"])
	}
	if got["service_version"] != Version {
		t.Errorf("service_version = %v, want %q", got["service_version"], Version)
	}
	if got["port"] != float64(5001) {
		t.Errorf("port = %v", got["port"])
	}
	if got["secret_set"] != true {
		t.Errorf("secret_set = %v, want true", got["secret_set"])
	}
	// The secret itself must never appear in a public document.
	if strings.Contains(w.Body.String(), "topsecret") {
		t.Fatal("the description leaks the secret")
	}
	// An anonymous probe is not a hub: it must not make the GUI claim one
	// is connected.
	if _, ok := hubLastSeenInfo(); ok {
		t.Error("the description recorded a hub contact")
	}
}

func TestSTDescriptionWithoutSecretConfigured(t *testing.T) {
	stSetup(t, Config{Port: 5002})
	w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.20", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	got := stJSON(t, w)
	if got["secret_set"] != false {
		t.Errorf("secret_set = %v, want false", got["secret_set"])
	}
	if got["port"] != float64(5002) {
		t.Errorf("port = %v, want the live port", got["port"])
	}
}

func TestSTDescriptionIgnoresAllowedHubs(t *testing.T) {
	// A hub allow-list guards the control routes; discovery has to work
	// before the user knows which IP to list.
	stSetup(t, Config{Port: 5001, Secret: "s", SmartThings: SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}}})
	if w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.99", "", ""); w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
}

func TestSTDescriptionMethodAndRateLimit(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	if w := stDo(t, http.MethodPost, "/st/v1/description", "192.168.1.20", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d", w.Code)
	}

	resetSTRateLimit()
	for i := 0; i < stRatePerSecond; i++ {
		if w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.30", "", ""); w.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, w.Code)
		}
	}
	if w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.30", "", ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("burst past the limit: status %d, want 429", w.Code)
	}
}

// ---- reconcile lifecycle ---------------------------------------------------

// loopbackSSDP replaces the socket factory with a single loopback socket so
// the responder can be started without multicast. It returns the address
// the test sends its M-SEARCH to.
func loopbackSSDP(t *testing.T) *net.UDPAddr {
	t.Helper()
	var (
		mu   sync.Mutex
		addr *net.UDPAddr
		open = ssdpOpen
	)
	ssdpOpen = func() ([]*ssdpSocket, error) {
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
		stopSSDP()
		ssdpOpen = open
	})

	startSSDP()
	mu.Lock()
	defer mu.Unlock()
	if addr == nil {
		t.Fatal("the responder did not open a socket")
	}
	return addr
}

func TestReconcileSSDPFollowsConfig(t *testing.T) {
	stSetup(t, Config{Port: 5001, SmartThings: SmartThingsConfig{Discovery: true}})
	loopbackSSDP(t)

	if !ssdpRunning() {
		t.Fatal("discovery: true must start the responder")
	}
	// Idempotent: reconciling an unchanged config is a no-op, and a second
	// start must not leak a socket or a goroutine.
	reconcileSSDP()
	startSSDP()
	if !ssdpRunning() {
		t.Fatal("starting twice stopped the responder")
	}

	setConfig(Config{Port: 5001, SmartThings: SmartThingsConfig{Discovery: false}})
	reconcileSSDP()
	if ssdpRunning() {
		t.Fatal("discovery: false must stop the responder")
	}
	reconcileSSDP() // stopping twice is safe
	stopSSDP()
	if ssdpRunning() {
		t.Fatal("still running after stopSSDP")
	}

	// And it comes back when the user turns it on again.
	setConfig(Config{Port: 5001, SmartThings: SmartThingsConfig{Discovery: true}})
	startSSDP()
	if !ssdpRunning() {
		t.Fatal("the responder did not restart")
	}
}

func TestReconcileSSDPIsNoOpUntilStarted(t *testing.T) {
	stSetup(t, Config{Port: 5001, SmartThings: SmartThingsConfig{Discovery: true}})
	// saveConfig runs in the installer and in tests, where no lifecycle is
	// managed: reconcile must not open a socket there.
	orig := ssdpOpen
	opened := false
	ssdpOpen = func() ([]*ssdpSocket, error) {
		opened = true
		return nil, fmt.Errorf("must not be called")
	}
	t.Cleanup(func() { ssdpOpen = orig })

	reconcileSSDP()
	if opened || ssdpRunning() {
		t.Error("reconcileSSDP opened a socket before startSSDP")
	}
}

func TestSSDPResponderAnswersOnLoopback(t *testing.T) {
	stSetup(t, Config{Port: 5001, SmartThings: SmartThingsConfig{Discovery: true}})
	resetSSDPRateLimit()
	// No MX jitter, so the reply lands well inside the one-per-second
	// window the repeat below has to fall foul of.
	origRand := ssdpRandFloat
	ssdpRandFloat = func() float64 { return 0 }
	t.Cleanup(func() {
		ssdpRandFloat = origRand
		resetSSDPRateLimit()
	})
	dst := loopbackSSDP(t)

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// A packet the responder must ignore, then one it must answer. Both go
	// out before the read, so a reply to the first would arrive first.
	if _, err := client.WriteToUDP(mSearchPacket(`MAN: "ssdp:discover"`, "ST: upnp:rootdevice"), dst); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteToUDP(mSearchPacket(`MAN: "ssdp:discover"`, "MX: 1", "ST: "+ssdpDeviceST), dst); err != nil {
		t.Fatal(err)
	}

	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, ssdpMaxPacket)
	n, _, err := client.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	start, h := ssdpHeaders(t, string(buf[:n]))
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
	if _, err := client.WriteToUDP(mSearchPacket(`MAN: "ssdp:discover"`, "ST: "+ssdpDeviceST), dst); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := client.ReadFromUDP(buf); err == nil {
		t.Errorf("a repeat within a second was answered: %q", buf[:n])
	}
}

func TestServeSSDPStopsWhenTheSocketCloses(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	s := &ssdpSocket{name: "loopback", localIP: net.IPv4(127, 0, 0, 1), recv: c, send: c}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go serveSSDP(ctx, s, &wg)

	cancel()
	s.close()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveSSDP did not return after its socket closed")
	}
}

// ---- config hot reload (§3.7) ----------------------------------------------

func TestConfigAPIRoundTripsSmartThings(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001, SmartThings: SmartThingsConfig{
		Discovery: true, AllowedHubs: []string{"192.168.1.20"}, ExposeSession: true,
	}})

	// GET hands the GUI every §3.7 key.
	w := httptest.NewRecorder()
	handleConfigAPI(w, httptest.NewRequest("GET", "/api/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	st, ok := decodeBody(t, w)["smartthings"].(map[string]any)
	if !ok {
		t.Fatalf("no smartthings object: %s", w.Body.String())
	}
	if st["discovery"] != true || st["expose_session"] != true || st["expose_session_user"] != false {
		t.Errorf("smartthings = %v", st)
	}
	hubs, _ := st["allowed_hubs"].([]any)
	if len(hubs) != 1 || hubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v", st["allowed_hubs"])
	}

	// POST writes them back and the live config follows without a restart.
	w = httptest.NewRecorder()
	handleConfigAPI(w, postJSON("/api/config", `{"port":5001,"smartthings":{"discovery":false,"allowed_hubs":["10.0.0.7"],"expose_session":false,"expose_session_user":true}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", w.Code, w.Body.String())
	}
	got := getConfig().SmartThings
	if got.Discovery || got.ExposeSession || !got.ExposeSessionUser {
		t.Errorf("live config = %+v", got)
	}
	if len(got.AllowedHubs) != 1 || got.AllowedHubs[0] != "10.0.0.7" {
		t.Errorf("allowed_hubs = %v", got.AllowedHubs)
	}

	// A request made right afterwards sees the saved values (no restart).
	resetSTRateLimit()
	d := stDo(t, http.MethodGet, "/st/v1/description", "10.0.0.7", "", "")
	if d.Code != http.StatusOK {
		t.Fatalf("description after save: status %d", d.Code)
	}
	if stJSON(t, d)["port"] != float64(5001) {
		t.Errorf("description port = %v", stJSON(t, d)["port"])
	}
}

// TestConfigChangedKeysCoversDiscovery guards the security event: flipping
// discovery is a network-visible change and must be listed.
func TestConfigChangedKeysCoversDiscovery(t *testing.T) {
	old := defaultConfig.withDefaults()
	updated := old
	updated.SmartThings.Discovery = !old.SmartThings.Discovery
	keys := configChangedKeys(old, updated)
	if len(keys) != 1 || keys[0] != "smartthings.discovery" {
		t.Errorf("keys = %v", keys)
	}
}
