package gui

// Local login (#131). With a secret set the API wants a session. The app
// used to log in with the secret read from config.json next to the exe;
// that file is now SYSTEM and Administrators only, so instead the app asks
// POST /api/local-login and the service decides from the connection itself
// (service/webui/locallogin.go): this exe, in the caller's own interactive
// session, run by an administrator. A refusal — a standard user, an older
// service that has no such route — leaves the login dialog as before.
//
// The same session is the "local trusted session" of the review contract
// (C5): the app asks for it at every (re)connect even with no secret set,
// because only it may edit the presets and the watch list. Refused, those
// two editors are locked with a note instead of failing on save.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"fyne.io/fyne/v2"
)

// errLocalLoginRefused is any answer but 200 from /api/local-login: the
// service does not vouch for this process (403), limits it (429), or is
// too old to know the route (404). Network errors are returned as such.
var errLocalLoginRefused = errors.New("local login refused")

// LocalLogin asks the service for a session without the secret and keeps
// the cookie it sets.
func (c *Client) LocalLogin() error {
	resp, err := c.do("POST", "/api/local-login", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) // drain for keep-alive
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("%w (HTTP %d)", errLocalLoginRefused, resp.StatusCode)
}

// localLoginRetryAfter is how long a refusal keeps the automatic paths
// (the heartbeat every 3–30 s, connTick every 5 s) from asking again: the
// answer will not change soon, and each refusal is a log line on the
// service.
const localLoginRetryAfter = 2 * time.Minute

// localLoginGate holds back a refused local login. The zero value allows
// the first attempt.
type localLoginGate struct {
	mu    sync.Mutex
	until time.Time
}

// try runs login unless one was refused within localLoginRetryAfter, and
// reports whether there is a session now.
func (g *localLoginGate) try(now time.Time, login func() error) bool {
	g.mu.Lock()
	wait := now.Before(g.until)
	g.mu.Unlock()
	if wait {
		return false
	}
	err := login()
	if errors.Is(err, errLocalLoginRefused) {
		g.mu.Lock()
		g.until = now.Add(localLoginRetryAfter)
		g.mu.Unlock()
	}
	return err == nil
}

// localLoginClock is the gate's clock; the tests move it.
var localLoginClock = time.Now

// trustState is what the app knows of its local trusted session (#131,
// review C5): the session /api/local-login hands out. Only that session
// may change the presets and the watch list, test a preset or list the
// running programs; without it the service masks the preset paths in
// GET /api/config.
type trustState int32

const (
	// trustUnknown: not asked yet, or the service went away since (a
	// restart drops every session).
	trustUnknown trustState = iota
	// trustLocal: the service vouched for this process.
	trustLocal
	// trustRefused: the service would not (a standard user) — the presets
	// and watch editors are locked.
	trustRefused
)

// trust is the current trustState. Safe from any goroutine.
func (u *ui) trust() trustState { return trustState(u.trustAt.Load()) }

// setTrust records s and, when that locks or unlocks the editors,
// re-applies the lock on the UI thread. Safe from any goroutine.
func (u *ui) setTrust(s trustState) {
	was := trustState(u.trustAt.Swap(int32(s)))
	if (was == trustRefused) != (s == trustRefused) {
		fyne.Do(u.applyTrust)
	}
}

// editorsLocked reports whether the presets and watch editors are locked:
// the service refused the local session, so it would refuse their saves.
func (u *ui) editorsLocked() bool { return u.trust() == trustRefused }

// applyTrust locks or unlocks the editors that need the local session.
// UI thread only.
func (u *ui) applyTrust() {
	u.applyPresetsLock()
	u.renderActivityRows()
	u.refreshDirty()
}

// tryLocalLogin asks for the local trusted session unless a refusal is
// still fresh (localLoginGate) and records the answer: a refusal — also
// one the gate remembers — locks the editors; a network error leaves the
// state alone. It runs at every (re)connect and after a 401 or a 403
// local_only. Safe from any goroutine.
func (u *ui) tryLocalLogin() bool {
	err := errLocalLoginRefused // what a held-back attempt means
	ok := u.localGate.try(localLoginClock(), func() error {
		err = u.client.LocalLogin()
		return err
	})
	switch {
	case ok:
		u.setTrust(trustLocal)
	case errors.Is(err, errLocalLoginRefused):
		u.setTrust(trustRefused)
	}
	return ok
}

// ensureLocalSession obtains the local trusted session when the app does
// not hold one: at start and after every reconnect, whether or not a
// secret is set (C5), so the config read that follows is not masked.
func (u *ui) ensureLocalSession() {
	if u.trust() != trustLocal {
		u.tryLocalLogin()
	}
}

// forgetLocalSession is for the service going away: its restart drops the
// session, so the next connect asks again (ensureLocalSession). A refusal
// is kept; the editors stay locked until an answer says otherwise.
func (u *ui) forgetLocalSession() {
	u.trustAt.CompareAndSwap(int32(trustLocal), int32(trustUnknown))
}

// errLocalOnly is the service's 403 {"error":"local_only"}: the call needs
// the local trusted session (C5).
var errLocalOnly = errors.New("local_only")

// needsLocalSession reports whether err asks for a (new) local session: a
// 401, or a 403 local_only.
func needsLocalSession(err error) bool {
	return errors.Is(err, errUnauthorized) || errors.Is(err, errLocalOnly)
}

// withLocalSession runs call and, when the service wants the local session
// again (it restarted, the secret changed), obtains it once more and
// retries before handing the error on. Off the UI thread.
func withLocalSession[T any](u *ui, call func() (T, error)) (T, error) {
	v, err := call()
	if needsLocalSession(err) && u.tryLocalLogin() {
		v, err = call()
	}
	return v, err
}

// localOnlyReply reports whether a 403 body is {"error":"local_only"}.
func localOnlyReply(body io.Reader) bool {
	var r struct {
		Error string `json:"error"`
	}
	return json.NewDecoder(io.LimitReader(body, 4<<10)).Decode(&r) == nil && r.Error == "local_only"
}
