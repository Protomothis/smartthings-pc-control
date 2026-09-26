package service

// Choosing the adapter a Wake-on-LAN magic packet must be addressed to
// (issue #96). A PC with an Ethernet port, a Wi-Fi card and a handful of
// Hyper-V / VPN pseudo-adapters offers several MACs, and the Edge driver
// used to take "the first one with WoL enabled" — which is a coin toss.
// The PC knows better than the driver, so it picks here and publishes the
// result as `wol.selected` (edge-driver doc §3.2); the driver just uses it
// (#97).
//
// `smartthings.wol_mac` overrides the choice. Empty (the default) means
// automatic, and a value that matches no adapter falls back to automatic
// rather than leaving WoL broken.

import (
	"net"
	"strings"
	"sync"
)

// ---- MAC normalisation -----------------------------------------------------

// normalizeMAC converts any spelling of a 6-byte MAC — "B4-2E-99-45-B4-F5",
// "b4:2e:99:45:b4:f5", "b4.2e.99.45.b4.f5", "b42e9945b4f5" — to the
// upper-case dash form Windows and the adapter list use. Anything that is
// not exactly 12 hex digits (with optional separators) returns "", which
// every caller reads as "no MAC".
func normalizeMAC(s string) string {
	digits := make([]byte, 0, 12)
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'F':
			digits = append(digits, byte(r))
		case r >= 'a' && r <= 'f':
			digits = append(digits, byte(r-'a'+'A'))
		case r == '-', r == ':', r == '.', r == ' ':
			// separator, ignored
		default:
			return ""
		}
		if len(digits) > 12 {
			return ""
		}
	}
	if len(digits) != 12 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.Write(digits[i : i+2])
	}
	return b.String()
}

// ---- virtual adapters ------------------------------------------------------

// virtualAdapterMarkers are the name fragments that mark an adapter as a
// pseudo-device rather than a network card that a magic packet can reach.
// One list, used by the automatic choice only: a user who picks such an
// adapter by hand gets what they asked for.
var virtualAdapterMarkers = []string{
	"vEthernet",
	"Hyper-V",
	"VirtualBox",
	"VMware",
	"TAP",
	"Tailscale",
	"WireGuard",
	"Loopback",
	"Bluetooth",
}

// isVirtualAdapter reports whether name looks like a pseudo-adapter.
// Matching is case-insensitive because the names come from Windows in
// whatever case the driver registered ("vEthernet (Default Switch)",
// "VMware Network Adapter VMnet8").
func isVirtualAdapter(name string) bool {
	lower := strings.ToLower(name)
	for _, m := range virtualAdapterMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// ---- the interface the hub talks to ----------------------------------------

// hubLocalIP is the local address of the connection that carried the most
// recent /st/v1 request, i.e. the IP of the NIC the hub actually reaches
// this PC on. That is the single best hint about which adapter has to stay
// awake, so it is rule ① of the automatic choice.
var (
	hubLocalIPValue net.IP
	hubLocalIPMu    sync.RWMutex
)

// noteHubLocalIP remembers ip as the interface the hub reached us on.
// Loopback and unusable addresses are ignored: a request from the WebUI or
// a test client on 127.0.0.1 must not erase the real hub's interface.
func noteHubLocalIP(ip net.IP) {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return
	}
	hubLocalIPMu.Lock()
	hubLocalIPValue = ip
	hubLocalIPMu.Unlock()
}

// lastHubLocalIP returns the remembered interface address, nil before the
// first request.
func lastHubLocalIP() net.IP {
	hubLocalIPMu.RLock()
	defer hubLocalIPMu.RUnlock()
	return hubLocalIPValue
}

// resetHubLocalIP forgets it (tests).
func resetHubLocalIP() {
	hubLocalIPMu.Lock()
	hubLocalIPValue = nil
	hubLocalIPMu.Unlock()
}

// ---- the choice ------------------------------------------------------------

// wolSelection is the adapter WoL should target, in the shape §3.2 puts on
// the wire.
type wolSelection struct {
	Name       string
	MAC        string
	IP         string
	WoLEnabled bool
	WoLCapable bool
	// Source is "manual" when wol_mac named this adapter, "auto" otherwise.
	Source string
}

// ipv4sOf returns the adapter's IPv4 addresses. The scan keeps IPv6 too
// (the network tab shows them), but the hub reaches this PC over IPv4 and
// that is what rule ① compares.
func ipv4sOf(a WoLAdapter) []string {
	out := []string{}
	for _, s := range a.IPs {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

// adapterIPv4 is the adapter's primary address for display, "" when it has
// none (a disconnected card, or one with IPv6 only).
func adapterIPv4(a WoLAdapter) string {
	if v4 := ipv4sOf(a); len(v4) > 0 {
		return v4[0]
	}
	return ""
}

// adapterOwnsIP reports whether ip is one of the adapter's IPv4 addresses.
func adapterOwnsIP(a WoLAdapter, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, s := range ipv4sOf(a) {
		if parsed := net.ParseIP(s); parsed != nil && parsed.Equal(ip) {
			return true
		}
	}
	return false
}

// selectionOf describes a for the status body.
func selectionOf(a WoLAdapter, source string) wolSelection {
	return wolSelection{
		Name:       a.Name,
		MAC:        normalizeMAC(a.MacAddress),
		IP:         adapterIPv4(a),
		WoLEnabled: a.WoLEnabled,
		WoLCapable: a.WoLCapable,
		Source:     source,
	}
}

// selectWoLAdapter picks the adapter to wake. manualMAC is
// `smartthings.wol_mac`; hubIP is the interface the hub last reached us on
// (nil when none has). ok is false only when there is no adapter with a
// MAC at all, in which case `wol.selected` is null.
//
// A manual MAC that matches an adapter always wins. Otherwise:
//
//	① the adapter that owns hubIP
//	② the first with WoL enabled
//	③ the first WoL-capable one
//	④ the first one, whatever it is
//
// Rule ① runs over every adapter: the interface the hub's packets really
// arrive on is evidence, not a guess, even when it is a Hyper-V switch
// carrying this PC's only LAN address. Rules ②–④ then run over the real
// adapters first and only fall to the pseudo ones if there are none —
// "some vEthernet has WakeOnMagicPacket set" is exactly the wrong answer
// that #96 exists to stop.
func selectWoLAdapter(adapters []WoLAdapter, manualMAC string, hubIP net.IP) (wolSelection, bool) {
	var cands []WoLAdapter
	for _, a := range adapters {
		if normalizeMAC(a.MacAddress) != "" {
			cands = append(cands, a)
		}
	}
	if len(cands) == 0 {
		return wolSelection{}, false
	}

	if want := normalizeMAC(manualMAC); want != "" {
		for _, a := range cands {
			if normalizeMAC(a.MacAddress) == want {
				return selectionOf(a, "manual"), true
			}
		}
		// The card was swapped, or the value was typed by hand: say so
		// once and fall through to the automatic choice, so WoL keeps
		// working instead of pointing at nothing.
		noteMissingWoLMAC(manualMAC)
	}

	for _, a := range cands { // ① the interface the hub uses
		if adapterOwnsIP(a, hubIP) {
			return selectionOf(a, "auto"), true
		}
	}

	real, virtual := []WoLAdapter{}, []WoLAdapter{}
	for _, a := range cands {
		if isVirtualAdapter(a.Name) {
			virtual = append(virtual, a)
		} else {
			real = append(real, a)
		}
	}
	for _, group := range [][]WoLAdapter{real, virtual} {
		if len(group) == 0 {
			continue
		}
		for _, a := range group { // ② WoL already on
			if a.WoLEnabled {
				return selectionOf(a, "auto"), true
			}
		}
		for _, a := range group { // ③ WoL possible
			if a.WoLCapable {
				return selectionOf(a, "auto"), true
			}
		}
		return selectionOf(group[0], "auto"), true // ④ anything with a MAC
	}
	// Unreachable: cands is non-empty, so one of the groups is too.
	return selectionOf(cands[0], "auto"), true
}

// noteMissingWoLMAC logs an unmatched wol_mac once per value, so a status
// poll every 10 seconds does not fill the log with the same line.
var (
	missingWoLMACLogged string
	missingWoLMACMu     sync.Mutex
)

func noteMissingWoLMAC(mac string) {
	if firstMissingWoLMAC(mac) {
		logMsg("WoL: smartthings.wol_mac %s matches no adapter; choosing automatically", normalizeMAC(mac))
	}
}

// firstMissingWoLMAC reports whether mac is a different unmatched value
// from the last one seen, and records it. A changed setting is worth a
// line again; the same one, ten seconds later, is not.
func firstMissingWoLMAC(mac string) bool {
	missingWoLMACMu.Lock()
	defer missingWoLMACMu.Unlock()
	if missingWoLMACLogged == mac {
		return false
	}
	missingWoLMACLogged = mac
	return true
}

// resetMissingWoLMACLog forgets the last logged value (tests).
func resetMissingWoLMACLog() {
	missingWoLMACMu.Lock()
	missingWoLMACLogged = ""
	missingWoLMACMu.Unlock()
}
