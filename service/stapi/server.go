// Package stapi is the SmartThings Edge driver protocol, /st/v1
// (docs/design/edge-driver.md §3, issue #67), and the SSDP responder that
// lets the driver find this PC (§3.6). The routes live on the main command
// port next to the legacy PCControl path, so the driver needs no second
// port:
//
//	GET    /st/v1/status          §3.2
//	POST   /st/v1/command         §3.3
//	DELETE /st/v1/schedule        §3.4
//	POST   /st/v1/notify          media-notify.md §3 (#106)
//	POST   /st/v1/subscribe       §3.5 (push.go)
//	DELETE /st/v1/subscribe/{id}  §3.5
//	GET    /st/v1/description     §3.6, unauthenticated (ssdp.go)
//
// Authentication is the X-PC-Secret header (§3.1) — never the URL — plus an
// optional hub allow-list and a per-source-IP rate limit (§3.1).
//
// The package reads the service through the interfaces in deps.go and
// never imports the root service (#127): the root builds one Server with
// New and mounts Handler on its command port.
package stapi

import (
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

const (
	// Protocol is the wire version the driver compares against (§3.2).
	Protocol = 1
	// RatePerSecond is the token-bucket refill rate per source IP (§3.1).
	RatePerSecond = 10
	// DriverAgent is the User-Agent prefix the Edge driver sends,
	// "smartthings-pc-control-edge/<driver version>".
	DriverAgent = "smartthings-pc-control-edge"
	// MaxBody caps a command body; the JSON is a handful of fields.
	MaxBody = 8 << 10
	// MaxMinutes matches /api/schedule, the Telegram bot and the app's
	// schedule tab: three days (#89, power.MaxScheduleMinutes). The
	// driver's `schedule(minutes)` definition carries the same maximum, and
	// the cloud rejects anything outside it before the hub ever sees it.
	MaxMinutes = power.MaxScheduleMinutes
)

// Server is the /st/v1 surface and the SSDP responder. The func fields are
// its clocks and sockets, replaced by the tests; New fills them with the
// real ones.
type Server struct {
	d Deps

	// Now is the clock of the per-source rate limits (/st/v1 and the SSDP
	// answers), so a test can drive both.
	Now func() time.Time
	// PushNow is the clock of the subscription expiry.
	PushNow func() time.Time
	// openSSDP opens the responder's sockets; the tests use one loopback
	// socket so they need no multicast-capable interface.
	openSSDP func() ([]*ssdpSocket, error)
	// randFloat spreads the SSDP answers over the MX window
	// (rand.Float64); the tests want a fixed delay.
	randFloat func() float64

	limiter *ratelimit.Limiter

	hubMu      sync.RWMutex
	hub        status.HubSeen
	hubLocalIP net.IP

	wol wolCache

	subs *subStore
	push pusher

	ssdp ssdpState
}

// New builds the surface on d. Nothing runs until Handler is mounted,
// StartSSDP is called or PushTap is attached to the notification bus.
func New(d Deps) *Server {
	s := &Server{
		d:         d,
		Now:       time.Now,
		PushNow:   time.Now,
		openSSDP:  openSSDPSockets,
		randFloat: rand.Float64,
	}
	s.limiter = ratelimit.New(RatePerSecond, time.Second, func() time.Time { return s.Now() })
	s.ssdp.limiter = ratelimit.New(1, ssdpPerSourceInterval, func() time.Time { return s.Now() })
	s.subs = newSubStore(func() time.Time { return s.PushNow() })
	s.push.init()
	return s
}

// Handler is the authenticated /st/v1 tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/st/v1/status", s.auth(s.handleStatus))
	mux.HandleFunc("/st/v1/command", s.auth(s.handleCommand))
	mux.HandleFunc("/st/v1/schedule", s.auth(s.handleSchedule))
	// PC notification (#106)
	mux.HandleFunc("/st/v1/notify", s.auth(s.handleNotify))
	// #69, unauthenticated (see ssdp.go)
	mux.HandleFunc("/st/v1/description", s.handleDescription)
	// /st/v1/subscribe (§3.5, #68)
	mux.HandleFunc("/st/v1/subscribe", s.auth(s.handleSubscribe))
	mux.HandleFunc("/st/v1/subscribe/", s.auth(s.handleUnsubscribe))
	// Anything else under /st/v1 is a 404 rather than falling through to
	// the legacy /{secret}/{command} handler.
	mux.HandleFunc("/st/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return mux
}

// ---- rate limiting (§3.1) --------------------------------------------------

// allow reports whether ip may make one more request now: RatePerSecond
// per source IP in any second.
func (s *Server) allow(ip string) bool {
	ok, _ := s.limiter.Allow(ip)
	return ok
}

// ResetRateLimit forgets every source (tests).
func (s *Server) ResetRateLimit() { s.limiter.Reset() }

// ---- hub last seen ---------------------------------------------------------

// noteHubSeen records one authenticated request from ip.
func (s *Server) noteHubSeen(ip, userAgent string) {
	s.hubMu.Lock()
	s.hub = status.HubSeen{IP: ip, DriverVersion: driverVersionOf(userAgent), At: time.Now()}
	s.hubMu.Unlock()
}

// HubLastSeen returns the last authenticated hub contact; ok is false
// before the first one.
func (s *Server) HubLastSeen() (status.HubSeen, bool) {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	return s.hub, !s.hub.At.IsZero()
}

// driverVersionOf extracts "1.0.0" from
// "smartthings-pc-control-edge/1.0.0"; any other User-Agent is kept
// verbatim (truncated) so an unexpected client is still recognisable.
func driverVersionOf(userAgent string) string {
	ua := strings.TrimSpace(userAgent)
	if name, version, ok := strings.Cut(ua, "/"); ok && name == DriverAgent {
		return httpx.Truncate(version, 32)
	}
	return httpx.Truncate(ua, 64)
}

// ---- auth (§3.1) -----------------------------------------------------------

// auth wraps a /st/v1 handler with the rate limit, the hub allow-list and
// the header secret check, and records the hub on success. The secret is
// never logged (§8).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := s.d.Config()
		from := httpx.RemoteHost(r.RemoteAddr)

		if !s.allow(from) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		if hubs := cfg.SmartThings.AllowedHubs; len(hubs) > 0 && !hubAllowed(hubs, from) {
			logx.Printf("ST API: %s %s from %s rejected (not in allowed_hubs)", r.Method, r.URL.Path, from)
			s.d.Emit("security", "unauthorized", map[string]string{
				"from": from,
				"path": httpx.Truncate(r.URL.Path, 64),
			})
			writeError(w, http.StatusForbidden, "hub not allowed")
			return
		}
		if cfg.Secret != "" && !secret.Equal(r.Header.Get("X-PC-Secret"), cfg.Secret) {
			// The attempted value is deliberately not logged or notified.
			logx.Printf("ST API: %s %s from %s (UNAUTHORIZED)", r.Method, r.URL.Path, from)
			s.d.Emit("security", "unauthorized", map[string]string{
				"from": from,
				"path": httpx.Truncate(r.URL.Path, 64),
			})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		s.noteHubSeen(from, r.Header.Get("User-Agent"))
		s.NoteHubLocalIP(localRequestIP(r))
		next(w, r)
	}
}

// localRequestIP is the address on *this* PC that the connection arrived
// on — http.Server stores it on the request context — which names the NIC
// the hub can reach us through. Rule ① of the WoL adapter choice (#96)
// compares it against each adapter's IPv4 list. nil when the server did
// not record one (a synthetic request in a test, say).
func localRequestIP(r *http.Request) net.IP {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return nil
	}
	return net.ParseIP(httpx.RemoteHost(addr.String()))
}

// hubAllowed reports whether from matches one of the configured hub
// addresses. Entries are compared as IPs when both parse (so "192.168.1.20"
// matches "::ffff:192.168.1.20"), otherwise as plain strings.
func hubAllowed(hubs []string, from string) bool {
	fromIP := net.ParseIP(from)
	for _, h := range hubs {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h == from {
			return true
		}
		if hIP := net.ParseIP(h); hIP != nil && fromIP != nil && hIP.Equal(fromIP) {
			return true
		}
	}
	return false
}

// writeError writes the {"error": ...} body the driver shows in pcInfo.
func writeError(w http.ResponseWriter, status int, msg string) {
	httpx.WriteJSON(w, status, map[string]string{"error": msg})
}
