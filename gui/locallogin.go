package gui

// Local login (#131). With a secret set the API wants a session. The app
// used to log in with the secret read from config.json next to the exe;
// that file is now SYSTEM and Administrators only, so instead the app asks
// POST /api/local-login and the service decides from the connection itself
// (service/webui/locallogin.go): this exe, in the caller's own interactive
// session, run by an administrator. A refusal — a standard user, an older
// service that has no such route — leaves the secret to the home screen's
// login card.
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) // drain for keep-alive
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return &localLoginRefusal{status: resp.StatusCode, code: refusalCode(resp.StatusCode, body)}
}

// localLoginRefusal is errLocalLoginRefused with the service's reason:
// its "code" (not_admin, other_exe, session_0, rate_limited, …; see
// service/webui/locallogin.go), or "http_<status>" when the reply had none
// (an older service without the route answers 404).
type localLoginRefusal struct {
	status int
	code   string
}

func (e *localLoginRefusal) Error() string {
	return fmt.Sprintf("%v (HTTP %d, %s)", errLocalLoginRefused, e.status, e.code)
}

func (e *localLoginRefusal) Is(target error) bool { return target == errLocalLoginRefused }

// refusalCode reads {"code":…} from a refusal's body.
func refusalCode(status int, body []byte) string {
	var r struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &r) == nil && r.Code != "" {
		return r.Code
	}
	return fmt.Sprintf("http_%d", status)
}

// localLoginRefusalCode is the reason a LocalLogin error carries, "" for
// any other error.
func localLoginRefusalCode(err error) string {
	var r *localLoginRefusal
	if errors.As(err, &r) {
		return r.code
	}
	return ""
}

// localLoginRetryAfter is how long a refusal keeps the automatic paths
// (the heartbeats, trayexec, the window's reconnect; ui/shell.go) from
// asking again: the
// answer will not change soon, and each refusal is a log line on the
// service.
const localLoginRetryAfter = 2 * time.Minute

// errLocalOnly is the service's 403 {"error":"local_only"}: the call needs
// the local trusted session (C5).
var errLocalOnly = errors.New("local_only")

// needsLocalSession reports whether err asks for a (new) local session: a
// 401, or a 403 local_only.
func needsLocalSession(err error) bool {
	return errors.Is(err, errUnauthorized) || errors.Is(err, errLocalOnly)
}

// localOnlyReply reports whether a 403 body is {"error":"local_only"}.
func localOnlyReply(body io.Reader) bool {
	var r struct {
		Error string `json:"error"`
	}
	return json.NewDecoder(io.LimitReader(body, 4<<10)).Decode(&r) == nil && r.Error == "local_only"
}
