package webui

// POST /api/local-login (#131): a session for this app's own tray without
// the secret. Until #131 the tray read the secret from config.json, which
// every local account could read too. Now config.json is SYSTEM and
// Administrators only, and the service vouches for the tray itself: it
// looks the loopback connection up in the TCP table (peer.go),
// and issues a session only when the process that opened it
//
//   - runs in an interactive session (not session 0) under an INTERACTIVE
//     logon token — not a service, scheduled batch or network logon;
//   - is that session's own logged-on user (not a runas of somebody else
//     inside it);
//   - belongs to an administrator (BUILTIN\Administrators in the token,
//     elevated or UAC-filtered) — the same people who may read config.json
//     and its secret anyway, so this grants nobody more than the file ACL
//     does. A standard user's tray falls back to the login dialog;
//   - was started from this service's own exe (in the admin-only install
//     folder), which keeps browsers and other programs of that user out.
//
// Remote clients never reach this: a non-loopback peer is refused before
// anything is looked up, and the remote WebUI login still needs the
// secret. The local session has a token of its own, shared by every
// trusted caller (the tray and the toast handler), so they do not log each
// other — or a browser on the WebUI — out the way /api/login's single
// session does; it is valid from loopback only, until the service stops.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

// Local login rate limit: every attempt counts, trusted or not. The tray
// asks once at start and after each service restart; a burst means
// something else is knocking.
const (
	localLoginWindow = time.Minute
	localLoginMax    = 10
)

// localLogin is the local session and its bookkeeping. The lookups behind
// identifyLocalPeer are the Server's peer* fields.
type localLogin struct {
	mu    sync.Mutex
	token string
	// limiter is one global limit ("" key): localLoginMax attempts per
	// localLoginWindow.
	limiter *ratelimit.Limiter
	// lastRefusal is the last refusal logged, so a tray that keeps asking
	// costs one log line, not one per attempt.
	lastRefusal string
}

// localTrustError is why a peer is refused; code goes to the client and
// the log, detail only to the log.
type localTrustError struct {
	code   string
	detail string
}

func (e *localTrustError) Error() string {
	if e.detail == "" {
		return e.code
	}
	return e.code + ": " + e.detail
}

func refuse(code, format string, args ...any) error {
	return &localTrustError{code: code, detail: fmt.Sprintf(format, args...)}
}

// trustLocalPeer is the decision on what inspectProcess found; nil means
// the caller may have a session. The order puts the cheap, telling checks
// first so the log names the most basic reason.
func trustLocalPeer(p peerProcess, sameExe func(image string) bool) error {
	switch {
	case p.SessionID == 0:
		return refuse("session_0", "pid %d runs in session 0 (a service)", p.PID)
	case !p.Interactive:
		return refuse("not_interactive", "pid %d has no interactive logon", p.PID)
	case p.SessionUserSID == "" || p.SessionUserSID != p.UserSID:
		return refuse("not_session_user", "pid %d runs as %s, session %d belongs to %q", p.PID, p.UserSID, p.SessionID, p.SessionUserSID)
	case !p.Admin:
		return refuse("not_admin", "pid %d (%s) is not an administrator", p.PID, p.UserSID)
	case !sameExe(p.Image):
		return refuse("other_exe", "pid %d is %s", p.PID, p.Image)
	}
	return nil
}

// loopbackV4 parses addr ("127.0.0.1:52431", or the v4-mapped form) and
// reports whether it is an IPv4 loopback endpoint. The tray always dials
// 127.0.0.1, and only the IPv4 table is read.
func loopbackV4(addr string) (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return netip.AddrPort{}, false
	}
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	return ap, ap.Addr().Is4() && ap.Addr().IsLoopback()
}

// identifyLocalPeer finds and judges the process that sent r.
func (s *Server) identifyLocalPeer(r *http.Request) (peerProcess, error) {
	client, ok := loopbackV4(r.RemoteAddr)
	if !ok {
		return peerProcess{}, refuse("not_loopback", "request from %s", r.RemoteAddr)
	}
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if la == nil {
		return peerProcess{}, refuse("not_loopback", "no local address")
	}
	server, ok := loopbackV4(la.String())
	if !ok {
		return peerProcess{}, refuse("not_loopback", "request to %s", la)
	}
	rows, err := s.peerTable()
	if err != nil {
		return peerProcess{}, refuse("unknown_peer", "%v", err)
	}
	pid, err := ownerPIDFor(rows, client, server)
	if err != nil {
		return peerProcess{}, refuse("unknown_peer", "%v", err)
	}
	p, err := s.peerInspect(pid)
	if err != nil {
		return p, refuse("unknown_peer", "%v", err)
	}
	return p, trustLocalPeer(p, s.peerSameExe)
}

// isServiceExe reports whether image is the very file this service runs
// from — compared by file identity, so path spellings (8.3 names, case)
// do not matter.
func isServiceExe(image string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	a, err := os.Stat(exe)
	if err != nil {
		return false
	}
	b, err := os.Stat(image)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

// localSession returns the local session token, made on first use.
func (s *Server) localSession() string {
	s.local.mu.Lock()
	defer s.local.mu.Unlock()
	if s.local.token == "" {
		s.local.token = generateSessionToken()
	}
	return s.local.token
}

// localSessionValid reports whether value is the local session token and r
// came over loopback — the token is never honoured from the network.
func (s *Server) localSessionValid(r *http.Request, value string) bool {
	if _, ok := loopbackV4(r.RemoteAddr); !ok {
		return false
	}
	s.local.mu.Lock()
	tok := s.local.token
	s.local.mu.Unlock()
	return tok != "" && secret.Equal(value, tok)
}

// handleLocalLogin serves POST /api/local-login. Replies: 200
// {"status":"ok"} with the session cookie; 403 {"status":"error",
// "code":…} when the caller is not trusted; 429 when rate limited. With no
// secret configured nothing needs a session and the reply is a plain ok.
func (s *Server) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if s.d.Config().Secret == "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if ok, _ := s.local.limiter.Allow(""); !ok {
		httpx.WriteJSON(w, http.StatusTooManyRequests, map[string]string{"status": "error", "code": "rate_limited", "message": "Too many attempts. Try again later."})
		return
	}
	p, err := s.identifyLocalPeer(r)
	if err != nil {
		code := "unknown_peer"
		var te *localTrustError
		if errors.As(err, &te) {
			code = te.code
		}
		s.noteLocalLoginRefusal(err)
		httpx.WriteJSON(w, http.StatusForbidden, map[string]string{"status": "error", "code": code, "message": "Not trusted for a local login"})
		return
	}
	setSessionCookie(w, s.localSession())
	logx.Printf("Local login for the tray app (pid %d, session %d)", p.PID, p.SessionID)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// noteLocalLoginRefusal logs a refusal unless it repeats the last one.
func (s *Server) noteLocalLoginRefusal(err error) {
	msg := err.Error()
	s.local.mu.Lock()
	repeat := msg == s.local.lastRefusal
	s.local.lastRefusal = msg
	s.local.mu.Unlock()
	if !repeat {
		logx.Printf("Local login refused: %s", msg)
	}
}
