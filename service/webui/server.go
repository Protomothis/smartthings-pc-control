// Package webui is the WebUI port (command port + 1): the browser pages
// and the JSON API under /api that the desktop app, the tray and the
// toast handler use.
//
// Every /api route but the two logins goes through apiAuth (#127): the
// session (the WebUI login, or the tray's local session from loopback,
// #131), the method and the CSRF header. A few go further and need the
// local trusted session itself (localOnly). The whole server sits behind the
// Host check that stops DNS rebinding (#120). The JSON shapes are the
// desktop app's contract; testdata/st-v1/api-config.get.json pins GET
// /api/config.
//
// The package reads the service through the interfaces in deps.go and
// never imports the root service (#127): the root builds one Server with
// New and serves Handler on the WebUI port.
package webui

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/secret"

	"golang.org/x/sys/windows"
)

// maxBody caps a small JSON request body (a command, a slot, a test
// notification).
const maxBody = 8 << 10

// Server is the WebUI and /api surface. The func fields are its clocks and
// system lookups, replaced by the tests; New fills them with the real ones.
type Server struct {
	d Deps

	// Uptime is how long the PC has been up (the page's status card).
	Uptime func() time.Duration

	// The lookups behind the local login (#131).
	peerTable   func() ([]tcpRow, error)
	peerInspect func(pid uint32) (peerProcess, error)
	peerSameExe func(image string) bool
	loginNow    func() time.Time

	// presetUserSID is the SID of the user presets run as, which may write
	// to its own scripts without a writable_by_others warning
	// (presetsafety.go).
	presetUserSID func() string

	// sessionToken is the browser login's session (POST /api/login): one
	// at a time, a new login replaces it.
	sessionMu    sync.RWMutex
	sessionToken string

	local localLogin
}

// New builds the surface on d.
func New(d Deps) *Server {
	s := &Server{
		d:        d,
		Uptime:   windows.DurationSinceBoot,
		loginNow: time.Now,
	}
	s.peerTable = readTCPTable
	s.peerInspect = s.inspectProcess
	s.peerSameExe = isServiceExe
	s.presetUserSID = s.targetUserSID
	s.local.limiter = ratelimit.New(localLoginMax, localLoginWindow, func() time.Time { return s.loginNow() })
	return s
}

// Handler is the whole WebUI server behind its Host check (#120). port is
// the WebUI port; remote is whether browser access from the LAN is on.
func (s *Server) Handler(port int, remote bool) http.Handler {
	return hostGuard(s.Routes(remote), port, remote)
}

// SetSessionToken replaces the browser login's session token and returns
// the one before (tests).
func (s *Server) SetSessionToken(token string) (prev string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	prev, s.sessionToken = s.sessionToken, token
	return prev
}

func generateSessionToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// checkAuth validates the session cookie against the configured secret:
// true when no secret is set, or the cookie carries the browser login's
// session or the tray's local one.
func (s *Server) checkAuth(r *http.Request, configured string) bool {
	if configured == "" {
		return true // No auth required
	}
	cookie, err := r.Cookie("session")
	if err != nil {
		return false
	}
	s.sessionMu.RLock()
	defer s.sessionMu.RUnlock()
	if s.sessionToken != "" && secret.Equal(cookie.Value, s.sessionToken) {
		return true
	}
	// The tray's session from POST /api/local-login (#131).
	return s.localSessionValid(r, cookie.Value)
}

// setSessionCookie hands out a session token, from /api/login or
// /api/local-login (#131) alike.
func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// checkCSRF validates CSRF protection for POST requests.
func checkCSRF(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// apiAuth wraps an /api handler in the checks every one of them shares,
// in this order: the session (checkAuth — the WebUI login, or the tray's
// local session from loopback, #131; 401), the method when methods are
// given (405), and the CSRF header on a POST (checkCSRF; 403). The replies
// are the plain-text http.Error ones the app has always seen.
func (s *Server) apiAuth(next http.HandlerFunc, methods ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r, s.d.Config().Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if len(methods) > 0 && !slices.Contains(methods, r.Method) {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !checkCSRF(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// localTrusted reports whether r carries the local trusted session: the
// token POST /api/local-login hands the desktop app after checking the
// peer process (loopback, this exe, the session's own administrator), and
// only over loopback. A secret login (/api/login) is not one.
func (s *Server) localTrusted(r *http.Request) bool {
	cookie, err := r.Cookie("session")
	if err != nil {
		return false
	}
	return s.localSessionValid(r, cookie.Value)
}

// localOnly wraps a handler that only the desktop app on this PC may use:
// without a local trusted session it answers 403 {"error":"local_only"},
// secret or not. It goes inside apiAuth, so a missing session with a
// secret set is still the usual 401 first.
//
// What sits behind it: running an arbitrary program as the user (preset
// test), the process list, and changing what presets run or what the
// watch list looks for (POST /api/config, checked there). A WebUI open to
// the LAN with a guessed or shared secret must not reach any of them.
func (s *Server) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.localTrusted(r) {
			writeLocalOnly(w)
			return
		}
		next(w, r)
	}
}

// writeLocalOnly is localOnly's refusal.
func writeLocalOnly(w http.ResponseWriter) {
	httpx.WriteJSON(w, http.StatusForbidden, map[string]string{"status": "error", "error": "local_only",
		"message": "Only the desktop app on this PC can do this."})
}

// SetLocalSessionToken replaces the local trusted session's token and
// returns the one before (tests of the root service, which cannot run the
// peer-process check).
func (s *Server) SetLocalSessionToken(token string) (prev string) {
	s.local.mu.Lock()
	defer s.local.mu.Unlock()
	prev, s.local.token = s.local.token, token
	return prev
}

// writeAPIError is the {status:"error", message} shape the app expects.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	httpx.WriteJSON(w, status, map[string]string{"status": "error", "message": msg})
}
