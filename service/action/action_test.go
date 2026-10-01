package action

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

func TestClassifyActionError(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
		detail string
	}{
		{&NotifyError{Code: "notify_disabled", Message: "off"}, 403, "notify_disabled", "off"},
		{&NotifyError{Code: "rate_limited", Message: "slow down"}, 429, "rate_limited", "slow down"},
		{&NotifyError{Code: "bad_text", Message: "empty"}, 400, "bad_text", "empty"},
		{ErrMediaDisabled, 403, "media_disabled", ""},
		{fmt.Errorf("run: %w", session.ErrNoUserSession), 409, "no_user_session", ""},
		{&ValueError{Msg: "value must be between 0 and 100"}, 400, "value must be between 0 and 100", ""},
		{fmt.Errorf("%w after 3s", session.ErrTimeout), 504, "timeout", ""},
		{&session.ActionError{Code: "bad_args", Message: "no text"}, 400, "bad_args", "no text"},
		{&session.ActionError{Code: "unsupported", Message: "no device"}, 501, "unsupported", "no device"},
		{&session.ActionError{Code: "failed", Message: "COM error"}, 502, "failed", "COM error"},
	}
	for _, c := range cases {
		f := Classify(c.err)
		if f.Status != c.status || f.Code != c.code || f.Detail != c.detail || f.Message == "" {
			t.Errorf("%v: %+v", c.err, f)
		}
	}
}

// An error the user session never put into words — a start failure,
// unreadable output — reaches the log only, never the client.
func TestActionErrorsDoNotLeakInternals(t *testing.T) {
	err := errors.New(`start C:\Program Files\SmartThings PC Control\stpc.exe: access denied`)
	f := Classify(err)
	if f.Status != http.StatusBadGateway || f.Code != "failed" || f.Detail != "" || strings.Contains(f.Message, "Program Files") {
		t.Errorf("%+v", f)
	}
	w := httptest.NewRecorder()
	WriteError(w, err)
	if strings.Contains(w.Body.String(), "Program Files") || strings.Contains(w.Body.String(), "access denied") {
		t.Errorf("body leaks the error: %s", w.Body)
	}
}

func TestWriteActionErrorRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, &NotifyError{Code: "rate_limited", Message: "slow down", RetryAfter: 1500 * time.Millisecond})
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" {
		t.Errorf("%d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	w = httptest.NewRecorder()
	WriteError(w, &NotifyError{Code: "rate_limited"})
	if w.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After %q, want at least 1", w.Header().Get("Retry-After"))
	}
}
