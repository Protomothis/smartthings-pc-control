package service

// Defences of the local WebUI API (#120, refactor-plan §1 0-3/0-4): the
// Host allow-list that stops DNS rebinding, and the constant-time secret
// comparison every password-like check goes through.

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// secretEqual compares a presented secret (or session token) with the
// expected one in constant time. Both sides are hashed first, so not even
// the length of the expected value leaks through timing.
func secretEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// webUILANHosts lists the names this PC answers to on the LAN — its own
// IPv4 addresses and its hostname — for the remote WebUI's Host check. A
// variable so tests can pin it; it is read per request because DHCP may
// change the addresses while the service runs.
var webUILANHosts = lanHostNames

// lanHostNames is the machine's IPv4 addresses (loopback excluded) plus
// its hostname.
func lanHostNames() []string {
	var out []string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				out = append(out, ip4.String())
			}
		}
	}
	if h := hostname(); h != "" {
		out = append(out, h)
	}
	return out
}

// webUIHostAllowed reports whether a request's Host header names this PC
// on the WebUI port. Loopback names (127.0.0.1, localhost, [::1]) are
// always accepted; with remote access on, so are this PC's LAN IPv4
// addresses and hostname. Anything else — notably an attacker's domain
// re-pointed at 127.0.0.1 — is refused.
func webUIHostAllowed(hostHeader string, port int, remote bool) bool {
	host, p, err := net.SplitHostPort(hostHeader)
	if err != nil || p != strconv.Itoa(port) {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if !remote {
		return false
	}
	for _, name := range webUILANHosts() {
		if strings.EqualFold(host, name) {
			return true
		}
	}
	return false
}

// webUIHostGuard rejects, before any handler runs, a request whose Host is
// not this PC (see webUIHostAllowed). Without it a page on any site could
// rebind its own domain to 127.0.0.1 and talk to the API as same-origin —
// with no secret set, that is everything the desktop app can do.
func webUIHostGuard(next http.Handler, port int, remote bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !webUIHostAllowed(r.Host, port, remote) {
			logMsg("WebUI: request from %s with Host %q rejected", remoteHost(r.RemoteAddr), truncate(r.Host, 64))
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
