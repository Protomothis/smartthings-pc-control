// Package action holds what every front end needs to know about the
// user-session actions — volume, mute and media keys, the PC notification,
// presets — without running them: the arguments a media command becomes,
// the text rules of a notification, and one mapping from their errors to
// what a client is told (#127). The runners themselves live in the root
// service, next to the stores their replies update.
package action

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// ErrMediaDisabled is returned while media.enabled is off; /st/v1 answers
// 403 media_disabled.
var ErrMediaDisabled = errors.New("media_disabled")

// ValueError is a value outside a media command's range (400). Msg is the
// code the driver shows (§3).
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }

// NotifyError is a request the notify rules refuse. Code is the wire
// error ("notify_disabled", "bad_text", "rate_limited").
type NotifyError struct {
	Code       string
	Message    string
	RetryAfter time.Duration // rate_limited only
}

func (e *NotifyError) Error() string { return e.Code + ": " + e.Message }

// Failure is how one failed action is answered. There used to be two
// mappings (mediaErrorStatus, actionErrorStatus) that disagreed on
// bad_args, and one of them handed internal error strings (paths, child
// output) to the client.
type Failure struct {
	Status int    // HTTP status
	Code   string // wire code: media_disabled, no_user_session, timeout, unsupported, failed, …
	// Message is a sentence for the client, never an internal error: the
	// user session's own words when it gave a reason, a fixed text
	// otherwise. The details stay in service.log.
	Message string
	// Detail is the user session's (or the notification check's) own
	// words, "" when the failure is described by the code alone. /st/v1's
	// media replies carry only this, so their bodies stay as the driver
	// knows them ({"error":"no_user_session"}).
	Detail string
	// RetryAfter is set for rate_limited.
	RetryAfter time.Duration
}

// Classify maps an action error to its answer.
func Classify(err error) Failure {
	var ne *NotifyError
	var ue *session.ActionError
	var ve *ValueError
	switch {
	case errors.As(err, &ne):
		f := Failure{Status: http.StatusBadRequest, Code: ne.Code, Message: ne.Message, Detail: ne.Message}
		switch ne.Code {
		case "notify_disabled":
			f.Status = http.StatusForbidden
		case "rate_limited":
			f.Status, f.RetryAfter = http.StatusTooManyRequests, ne.RetryAfter
		}
		return f
	case errors.Is(err, ErrMediaDisabled):
		return Failure{Status: http.StatusForbidden, Code: "media_disabled", Message: "media control is turned off in the app settings"}
	case errors.Is(err, session.ErrNoUserSession):
		return Failure{Status: http.StatusConflict, Code: "no_user_session", Message: "nobody is logged in on this PC"}
	case errors.As(err, &ve):
		// The range message is the code the driver shows (§3).
		return Failure{Status: http.StatusBadRequest, Code: ve.Msg, Message: ve.Msg}
	case errors.Is(err, session.ErrTimeout):
		return Failure{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "the user session did not answer in time"}
	case errors.As(err, &ue):
		switch ue.Code {
		case useraction.CodeBadArgs:
			return Failure{Status: http.StatusBadRequest, Code: ue.Code, Message: ue.Message, Detail: ue.Message}
		case useraction.CodeUnsupported:
			return Failure{Status: http.StatusNotImplemented, Code: ue.Code, Message: ue.Message, Detail: ue.Message}
		}
		return Failure{Status: http.StatusBadGateway, Code: useraction.CodeFailed, Message: ue.Message, Detail: ue.Message}
	}
	// A start failure or unreadable output: the details (paths, child
	// output) stay in service.log.
	return Failure{Status: http.StatusBadGateway, Code: useraction.CodeFailed, Message: "the action could not be run in the user session"}
}

// Status is Classify as (status, code, message).
func Status(err error) (int, string, string) {
	f := Classify(err)
	return f.Status, f.Code, f.Message
}

// WriteError answers with {"error": code, "message": …} and, for a rate
// limit, Retry-After in whole seconds (at least 1).
func WriteError(w http.ResponseWriter, err error) {
	f := Classify(err)
	if f.Code == "rate_limited" {
		secs := int((f.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	}
	httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Message})
}
