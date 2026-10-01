package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassifyActionError(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
		detail string
	}{
		{&pcNotifyError{Code: "notify_disabled", Message: "off"}, 403, "notify_disabled", "off"},
		{&pcNotifyError{Code: "rate_limited", Message: "slow down"}, 429, "rate_limited", "slow down"},
		{&pcNotifyError{Code: "bad_text", Message: "empty"}, 400, "bad_text", "empty"},
		{errMediaDisabled, 403, "media_disabled", ""},
		{fmt.Errorf("run: %w", errNoUserSession), 409, "no_user_session", ""},
		{&errMediaValue{msg: "value must be between 0 and 100"}, 400, "value must be between 0 and 100", ""},
		{fmt.Errorf("%w after 3s", errUserActionTimeout), 504, "timeout", ""},
		{&userActionError{Code: "bad_args", Message: "no text"}, 400, "bad_args", "no text"},
		{&userActionError{Code: "unsupported", Message: "no device"}, 501, "unsupported", "no device"},
		{&userActionError{Code: "failed", Message: "COM error"}, 502, "failed", "COM error"},
	}
	for _, c := range cases {
		f := classifyActionError(c.err)
		if f.Status != c.status || f.Code != c.code || f.Detail != c.detail || f.Message == "" {
			t.Errorf("%v: %+v", c.err, f)
		}
	}
}

// An error the user session never put into words — a start failure,
// unreadable output — reaches the log only, never the client.
func TestActionErrorsDoNotLeakInternals(t *testing.T) {
	err := errors.New(`start C:\Program Files\SmartThings PC Control\stpc.exe: access denied`)
	f := classifyActionError(err)
	if f.Status != http.StatusBadGateway || f.Code != "failed" || f.Detail != "" || strings.Contains(f.Message, "Program Files") {
		t.Errorf("%+v", f)
	}
	w := httptest.NewRecorder()
	writeActionError(w, err)
	if strings.Contains(w.Body.String(), "Program Files") || strings.Contains(w.Body.String(), "access denied") {
		t.Errorf("body leaks the error: %s", w.Body)
	}
}

func TestWriteActionErrorRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	writeActionError(w, &pcNotifyError{Code: "rate_limited", Message: "slow down", RetryAfter: 1500 * time.Millisecond})
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" {
		t.Errorf("%d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	w = httptest.NewRecorder()
	writeActionError(w, &pcNotifyError{Code: "rate_limited"})
	if w.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After %q, want at least 1", w.Header().Get("Retry-After"))
	}
}
