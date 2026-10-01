package service

// One mapping from the errors of the user-session actions — volume, mute
// and media keys, the PC notification, presets — to what a client is told
// (#127). It used to be two (mediaErrorStatus, actionErrorStatus) that
// disagreed on bad_args, and one of them handed internal error strings
// (paths, child output) to the client.

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// actionFailure is how one failed action is answered.
type actionFailure struct {
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

// classifyActionError maps an action error to its answer.
func classifyActionError(err error) actionFailure {
	var ne *pcNotifyError
	var ue *userActionError
	var ve *errMediaValue
	switch {
	case errors.As(err, &ne):
		f := actionFailure{Status: http.StatusBadRequest, Code: ne.Code, Message: ne.Message, Detail: ne.Message}
		switch ne.Code {
		case "notify_disabled":
			f.Status = http.StatusForbidden
		case "rate_limited":
			f.Status, f.RetryAfter = http.StatusTooManyRequests, ne.RetryAfter
		}
		return f
	case errors.Is(err, errMediaDisabled):
		return actionFailure{Status: http.StatusForbidden, Code: "media_disabled", Message: "media control is turned off in the app settings"}
	case errors.Is(err, errNoUserSession):
		return actionFailure{Status: http.StatusConflict, Code: "no_user_session", Message: "nobody is logged in on this PC"}
	case errors.As(err, &ve):
		// The range message is the code the driver shows (§3).
		return actionFailure{Status: http.StatusBadRequest, Code: ve.msg, Message: ve.msg}
	case errors.Is(err, errUserActionTimeout):
		return actionFailure{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "the user session did not answer in time"}
	case errors.As(err, &ue):
		switch ue.Code {
		case useraction.CodeBadArgs:
			return actionFailure{Status: http.StatusBadRequest, Code: ue.Code, Message: ue.Message, Detail: ue.Message}
		case useraction.CodeUnsupported:
			return actionFailure{Status: http.StatusNotImplemented, Code: ue.Code, Message: ue.Message, Detail: ue.Message}
		}
		return actionFailure{Status: http.StatusBadGateway, Code: useraction.CodeFailed, Message: ue.Message, Detail: ue.Message}
	}
	// A start failure or unreadable output: the details (paths, child
	// output) stay in service.log.
	return actionFailure{Status: http.StatusBadGateway, Code: useraction.CodeFailed, Message: "the action could not be run in the user session"}
}

// actionErrorStatus is classifyActionError as (status, code, message).
func actionErrorStatus(err error) (int, string, string) {
	f := classifyActionError(err)
	return f.Status, f.Code, f.Message
}

// writeActionError answers with {"error": code, "message": …} and, for a
// rate limit, Retry-After in whole seconds (at least 1).
func writeActionError(w http.ResponseWriter, err error) {
	f := classifyActionError(err)
	if f.Code == "rate_limited" {
		secs := int((f.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	}
	httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Message})
}
