package webui

// Defences of the local WebUI API (#120, refactor-plan §1 0-3/0-4): the
// Host allow-list that stops DNS rebinding. The constant-time secret
// comparison every password-like check goes through is secret.Equal.

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
)

// hostAllowed reports whether a request's Host header may reach the
// WebUI. Without remote access the WebUI listens on loopback only and needs
// no login when no secret is set, so a page whose domain was re-pointed at
// 127.0.0.1 (DNS rebinding) could otherwise use the whole API as same-origin:
// only loopback names on the WebUI port are accepted. With remote access on,
// a secret is required and every API call needs the session cookie, which a
// rebound page cannot have, so any Host is accepted — a Tailscale name, a
// reverse proxy or a port forward must keep working.
func hostAllowed(hostHeader string, port int, remote bool) bool {
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

// hostGuard rejects, before any handler runs, a request whose Host is
// not this PC (see hostAllowed). Without it a page on any site could
// rebind its own domain to 127.0.0.1 and talk to the API as same-origin —
// with no secret set, that is everything the desktop app can do.
func hostGuard(next http.Handler, port int, remote bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, port, remote) {
			logx.Printf("WebUI: request from %s with Host %q rejected", httpx.RemoteHost(r.RemoteAddr), httpx.Truncate(r.Host, 64))
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
