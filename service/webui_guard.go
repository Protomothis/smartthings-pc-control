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

// webUIHostAllowed reports whether a request's Host header may reach the
// WebUI. Without remote access the WebUI listens on loopback only and needs
// no login when no secret is set, so a page whose domain was re-pointed at
// 127.0.0.1 (DNS rebinding) could otherwise use the whole API as same-origin:
// only loopback names on the WebUI port are accepted. With remote access on,
// a secret is required and every API call needs the session cookie, which a
// rebound page cannot have, so any Host is accepted — a Tailscale name, a
// reverse proxy or a port forward must keep working.
func webUIHostAllowed(hostHeader string, port int, remote bool) bool {
	if remote {
		return true
	}
	host, p, err := net.SplitHostPort(hostHeader)
	if err != nil || p != strconv.Itoa(port) {
		return false
	}
	switch strings.TrimSuffix(strings.ToLower(host), ".") {
	case "127.0.0.1", "localhost", "::1":
		return true
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
