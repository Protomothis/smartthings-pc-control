package gui

// Local login (#131). With a secret set the API wants a session. The app
// used to log in with the secret read from config.json next to the exe;
// that file is now SYSTEM and Administrators only, so instead the app asks
// POST /api/local-login and the service decides from the connection itself
// (service/webui/locallogin.go): this exe, in the caller's own interactive
// session, run by an administrator. A refusal — a standard user, an older
// service that has no such route — leaves the login dialog as before.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
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

// tryLocalLogin is the app's automatic login: after a 401 from initialLoad
// or the heartbeat. Safe from any goroutine.
func (u *ui) tryLocalLogin() bool {
	return u.localGate.try(localLoginClock(), u.client.LocalLogin)
}
