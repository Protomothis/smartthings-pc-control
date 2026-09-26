package service

// Tests for the WoL adapter choice (#96): MAC normalisation, the four-step
// automatic rule with virtual adapters demoted, the manual override and
// the memory of the interface the hub's requests arrive on.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// wolAdapters is the multi-NIC PC the rules have to cope with: a wired
// card, Wi-Fi, and the pseudo-adapters a developer machine collects.
func wolAdapters() []WoLAdapter {
	return []WoLAdapter{
		{Name: "vEthernet (Default Switch)", MacAddress: "00-15-5D-01-02-03", IPs: []string{"172.20.0.1"}, Status: "Up", WoLEnabled: true, WoLCapable: true},
		{Name: "Ethernet", MacAddress: "B4-2E-99-45-B4-F5", IPs: []string{"192.168.1.30", "fe80::abcd"}, Status: "Up", WoLCapable: true},
		{Name: "Wi-Fi", MacAddress: "11-22-33-44-55-66", IPs: []string{"192.168.1.31"}, Status: "Up"},
	}
}

func TestNormalizeMAC(t *testing.T) {
	const want = "B4-2E-99-45-B4-F5"
	for _, in := range []string{
		"B4-2E-99-45-B4-F5",
		"b4-2e-99-45-b4-f5",
		"b4:2e:99:45:b4:f5",
		"B4:2E:99:45:B4:F5",
		"b4.2e.99.45.b4.f5",
		"b42e9945b4f5",
		"  b4:2E:99:45:b4:F5  ",
	} {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything that is not a 6-byte MAC is "no MAC" rather than a value
	// that could never match an adapter.
	for _, in := range []string{"", "   ", "B4-2E-99-45-B4", "B4-2E-99-45-B4-F5-00", "not a mac", "B4-2E-99-45-B4-FG", "192.168.1.30"} {
		if got := normalizeMAC(in); got != "" {
			t.Errorf("normalizeMAC(%q) = %q, want \"\"", in, got)
		}
	}
}

func TestIsVirtualAdapter(t *testing.T) {
	for _, name := range []string{
		"vEthernet (Default Switch)", "Hyper-V Virtual Ethernet Adapter",
		"VirtualBox Host-Only Network", "VMware Network Adapter VMnet8",
		"TAP-Windows Adapter V9", "Tailscale", "WireGuard Tunnel",
		"Software Loopback Interface 1", "Bluetooth Network Connection",
	} {
		if !isVirtualAdapter(name) {
			t.Errorf("%q should be treated as a virtual adapter", name)
		}
	}
	for _, name := range []string{"Ethernet", "Wi-Fi", "이더넷", "Realtek Gaming 2.5GbE Family Controller"} {
		if isVirtualAdapter(name) {
			t.Errorf("%q should not be treated as a virtual adapter", name)
		}
	}
}

// Rule ①: the adapter that owns the address the hub's requests land on
// wins, even though another adapter has WoL enabled already.
func TestSelectWoLAdapterPrefersTheHubsInterface(t *testing.T) {
	sel, ok := selectWoLAdapter(wolAdapters(), "", net.ParseIP("192.168.1.31"))
	if !ok {
		t.Fatal("no adapter was chosen")
	}
	if sel.Name != "Wi-Fi" || sel.Source != "auto" {
		t.Errorf("selected %+v, want the Wi-Fi adapter the hub reaches us on", sel)
	}
	if sel.MAC != "11-22-33-44-55-66" || sel.IP != "192.168.1.31" {
		t.Errorf("selected %+v", sel)
	}
}

// Rule ②: with no hub address on record, WoL already being on decides —
// but the vEthernet adapter that has it on is a pseudo-device and goes to
// the back of the queue, so the WoL-capable Ethernet card wins on rule ③.
func TestSelectWoLAdapterDemotesVirtualAdapters(t *testing.T) {
	sel, ok := selectWoLAdapter(wolAdapters(), "", nil)
	if !ok {
		t.Fatal("no adapter was chosen")
	}
	if sel.Name != "Ethernet" {
		t.Errorf("selected %+v, want Ethernet: the only WoL-enabled adapter is virtual", sel)
	}

	// A real adapter with WoL on beats a merely capable one (rule ②).
	adapters := wolAdapters()
	adapters[2].WoLEnabled, adapters[2].WoLCapable = true, true
	if sel, _ := selectWoLAdapter(adapters, "", nil); sel.Name != "Wi-Fi" {
		t.Errorf("selected %+v, want the Wi-Fi adapter that has WoL enabled", sel)
	}

	// The demotion is a last resort, not a ban: on a PC whose only
	// adapters are virtual there is nothing else to pick.
	only := []WoLAdapter{{Name: "vEthernet (WSL)", MacAddress: "00-15-5D-09-08-07"}}
	if sel, ok := selectWoLAdapter(only, "", nil); !ok || sel.Name != "vEthernet (WSL)" {
		t.Errorf("selected %+v (ok=%v), want the only adapter there is", sel, ok)
	}

	// And rule ① still beats the demotion: an address the hub really
	// reaches us on is evidence, whatever the adapter is called.
	if sel, _ := selectWoLAdapter(wolAdapters(), "", net.ParseIP("172.20.0.1")); sel.Name != "vEthernet (Default Switch)" {
		t.Errorf("selected %+v, want the adapter the hub's requests arrive on", sel)
	}
}

// Rule ④: nothing is enabled, nothing is capable — the first real adapter
// still has to be named, or the driver has no MAC to wake at all.
func TestSelectWoLAdapterFallsBackToTheFirstMAC(t *testing.T) {
	adapters := []WoLAdapter{
		{Name: "vEthernet (WSL)", MacAddress: "00-15-5D-09-08-07"},
		{Name: "Ethernet", MacAddress: "B4-2E-99-45-B4-F5"},
		{Name: "Wi-Fi", MacAddress: "11-22-33-44-55-66"},
	}
	sel, ok := selectWoLAdapter(adapters, "", nil)
	if !ok {
		t.Fatal("no adapter was chosen")
	}
	if sel.Name != "Ethernet" || sel.WoLCapable || sel.WoLEnabled {
		t.Errorf("selected %+v, want the first non-virtual adapter", sel)
	}

	// No MAC anywhere means no selection: §3.2 sends null rather than a
	// made-up adapter.
	if _, ok := selectWoLAdapter([]WoLAdapter{{Name: "Ethernet"}}, "", nil); ok {
		t.Error("an adapter without a MAC was chosen")
	}
	if _, ok := selectWoLAdapter(nil, "", nil); ok {
		t.Error("an empty adapter list produced a selection")
	}
}

// A manual wol_mac wins over every automatic rule, in whatever spelling,
// and even when it names a virtual adapter or one with WoL off.
func TestSelectWoLAdapterManualWins(t *testing.T) {
	for _, mac := range []string{"11:22:33:44:55:66", "11-22-33-44-55-66", "112233445566"} {
		sel, ok := selectWoLAdapter(wolAdapters(), mac, net.ParseIP("192.168.1.30"))
		if !ok {
			t.Fatalf("%q: no adapter was chosen", mac)
		}
		if sel.Name != "Wi-Fi" || sel.Source != "manual" {
			t.Errorf("%q selected %+v, want the pinned Wi-Fi adapter", mac, sel)
		}
		// The stored form is the one the adapter list reports.
		if sel.MAC != "11-22-33-44-55-66" {
			t.Errorf("%q selected MAC %q", mac, sel.MAC)
		}
	}
	if sel, _ := selectWoLAdapter(wolAdapters(), "00-15-5D-01-02-03", nil); sel.Name != "vEthernet (Default Switch)" {
		t.Errorf("a deliberately pinned virtual adapter was overridden: %+v", sel)
	}
}

// A MAC that matches nothing (the card was swapped, or config.json was
// edited by hand) falls back to the automatic choice instead of leaving
// WoL pointed at an adapter that does not exist.
func TestSelectWoLAdapterManualMissFallsBackToAuto(t *testing.T) {
	initLogger()
	resetMissingWoLMACLog()
	t.Cleanup(resetMissingWoLMACLog)

	sel, ok := selectWoLAdapter(wolAdapters(), "DE-AD-BE-EF-00-01", nil)
	if !ok {
		t.Fatal("no adapter was chosen")
	}
	if sel.Name != "Ethernet" || sel.Source != "auto" {
		t.Errorf("selected %+v, want the automatic choice", sel)
	}
	// Garbage in the config behaves the same way (withDefaults blanks it,
	// but the selector must not trust that).
	if sel, _ := selectWoLAdapter(wolAdapters(), "nonsense", nil); sel.Source != "auto" {
		t.Errorf("selected %+v, want the automatic choice", sel)
	}
}

// The unmatched-MAC warning is logged once per value — the driver polls
// the status every ten seconds — but a changed value is worth a line again.
func TestMissingWoLMACLoggedOncePerValue(t *testing.T) {
	resetMissingWoLMACLog()
	t.Cleanup(resetMissingWoLMACLog)

	if !firstMissingWoLMAC("DE-AD-BE-EF-00-01") {
		t.Error("the first unmatched wol_mac was not logged")
	}
	for i := 0; i < 3; i++ {
		if firstMissingWoLMAC("DE-AD-BE-EF-00-01") {
			t.Fatal("the same unmatched wol_mac was logged twice")
		}
	}
	if !firstMissingWoLMAC("DE-AD-BE-EF-00-02") {
		t.Error("a changed unmatched wol_mac was not logged")
	}
}

// A /st/v1 request teaches the service which of its own interfaces the hub
// reaches it on, and the automatic choice then uses it (rule ①).
func TestHubLocalIPRemembersTheRequestInterface(t *testing.T) {
	resetHubLocalIP()
	t.Cleanup(resetHubLocalIP)

	if got := lastHubLocalIP(); got != nil {
		t.Fatalf("lastHubLocalIP = %v before any request, want nil", got)
	}

	// The local address is what net/http puts on the context of every
	// connection it accepts.
	r := httptest.NewRequest("GET", "/st/v1/status", nil)
	local := &net.TCPAddr{IP: net.ParseIP("192.168.1.31"), Port: 5001}
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(local)))
	noteHubLocalIP(localRequestIP(r))

	if got := lastHubLocalIP(); got == nil || got.String() != "192.168.1.31" {
		t.Fatalf("lastHubLocalIP = %v, want 192.168.1.31", got)
	}
	if sel, _ := selectWoLAdapter(wolAdapters(), "", lastHubLocalIP()); sel.Name != "Wi-Fi" {
		t.Errorf("selected %+v, want the adapter that owns 192.168.1.31", sel)
	}

	// A local call (the WebUI, a test client) must not erase that memory:
	// it says nothing about how the hub gets here.
	noteHubLocalIP(net.ParseIP("127.0.0.1"))
	if got := lastHubLocalIP(); got.String() != "192.168.1.31" {
		t.Errorf("a loopback request overwrote the hub's interface: %v", got)
	}

	// A request with no local address on its context (a synthetic one)
	// leaves the memory alone rather than clearing it.
	noteHubLocalIP(localRequestIP(httptest.NewRequest("GET", "/st/v1/status", nil)))
	if got := lastHubLocalIP(); got.String() != "192.168.1.31" {
		t.Errorf("a request without a local address cleared the memory: %v", got)
	}
}

// withDefaults is where a MAC typed by a user becomes the stored form, and
// where nonsense becomes "" (= automatic) rather than a value that could
// never match.
func TestSmartThingsConfigNormalisesWoLMAC(t *testing.T) {
	for in, want := range map[string]string{
		"b4:2e:99:45:b4:f5": "B4-2E-99-45-B4-F5",
		"B4-2E-99-45-B4-F5": "B4-2E-99-45-B4-F5",
		"  b42e9945b4f5  ":  "B4-2E-99-45-B4-F5",
		"":                  "",
		"auto":              "",
		"B4-2E-99-45-B4":    "",
	} {
		if got := (SmartThingsConfig{WoLMAC: in}).withDefaults().WoLMAC; got != want {
			t.Errorf("withDefaults(%q).WoLMAC = %q, want %q", in, got, want)
		}
	}
	// A changed pin is a config change the security event reports.
	old := Config{SmartThings: SmartThingsConfig{}}
	new := Config{SmartThings: SmartThingsConfig{WoLMAC: "B4-2E-99-45-B4-F5"}}
	if !slices.Contains(configChangedKeys(old, new), "smartthings.wol_mac") {
		t.Errorf("configChangedKeys = %v, want smartthings.wol_mac", configChangedKeys(old, new))
	}
}
