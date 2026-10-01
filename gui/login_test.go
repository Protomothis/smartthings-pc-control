package gui

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestShouldAutoLoad: connTick must not fire initialLoad while the login
// dialog is up or an attempt is in flight — that is what stacked dialogs
// every 5 s and swallowed the user's typing (#98).
func TestShouldAutoLoad(t *testing.T) {
	cases := []struct {
		connected bool
		st        loginState
		want      bool
	}{
		{false, loginIdle, true},
		{false, loginPrompting, false},
		// Deferred still polls: initialLoad does not prompt then, and a
		// GetConfig is not a login attempt.
		{false, loginDeferred, true},
		{true, loginIdle, false},
		{true, loginPrompting, false},
		{true, loginDeferred, false},
	}
	for _, tc := range cases {
		if got := shouldAutoLoad(tc.connected, tc.st); got != tc.want {
			t.Errorf("shouldAutoLoad(%v, %d) = %v, want %v", tc.connected, tc.st, got, tc.want)
		}
	}
}

func TestShouldPromptLogin(t *testing.T) {
	cases := []struct {
		st   loginState
		want bool
	}{
		{loginIdle, true},
		{loginPrompting, false}, // one dialog at a time
		{loginDeferred, false},  // cancelled: wait for the Login button
	}
	for _, tc := range cases {
		if got := shouldPromptLogin(tc.st); got != tc.want {
			t.Errorf("shouldPromptLogin(%d) = %v, want %v", tc.st, got, tc.want)
		}
	}
}

func TestRetryPromptAfter(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"wrong secret", errLoginInvalid, true},
		{"wrapped wrong secret", fmt.Errorf("login: %w", errLoginInvalid), true},
		{"rate limited", errLoginLimited, false},
		{"service gone", errors.New("dial tcp 127.0.0.1:8080: connection refused"), false},
	}
	for _, tc := range cases {
		if got := retryPromptAfter(tc.err); got != tc.want {
			t.Errorf("%s: retryPromptAfter = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldReloginAfterSave(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     bool
	}{
		{"first secret", "", "s3cret", true},
		{"changed secret", "old", "new", true},
		{"unchanged", "same", "same", false},
		{"cleared", "old", "", false}, // no secret, no auth
		{"never set", "", "", false},
	}
	for _, tc := range cases {
		if got := shouldReloginAfterSave(tc.old, tc.new); got != tc.want {
			t.Errorf("%s: shouldReloginAfterSave(%q, %q) = %v, want %v", tc.name, tc.old, tc.new, got, tc.want)
		}
	}
}

func TestLoginErrorMessage(t *testing.T) {
	other := errors.New("login failed (HTTP 500)")
	cases := []struct {
		name string
		err  error
		lang Lang
		want string
	}{
		{"limited ko", errLoginLimited, LangKo, "로그인 시도가 너무 많습니다. 60초 후 다시 시도하세요."},
		{"limited en", errLoginLimited, LangEn, "Too many login attempts. Try again in 60 seconds."},
		{"invalid ko", errLoginInvalid, LangKo, "시크릿이 올바르지 않습니다."},
		{"invalid en", errLoginInvalid, LangEn, "Invalid secret."},
		{"other passes through", other, LangKo, "login failed (HTTP 500)"},
	}
	for _, tc := range cases {
		if got := loginErrorMessage(tc.err, tc.lang); got != tc.want {
			t.Errorf("%s: loginErrorMessage = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestClientLoginStatus maps the service's login answers (service/webui)
// onto the sentinels the dialog words itself.
func TestClientLoginStatus(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error // nil: success; errOther: any other error
	}{
		{http.StatusOK, `{"status":"ok"}`, nil},
		{http.StatusUnauthorized, `{"status":"error","message":"Invalid secret"}`, errLoginInvalid},
		{http.StatusTooManyRequests, `{"status":"error","message":"Too many attempts. Try again later."}`, errLoginLimited},
		{http.StatusForbidden, `Forbidden`, errOther},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		}))
		c := &Client{base: srv.URL, http: srv.Client()}
		err := c.Login("x")
		srv.Close()
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("HTTP %d: err = %v, want nil", tc.status, err)
		case errors.Is(tc.want, errOther) && (err == nil || errors.Is(err, errLoginInvalid) || errors.Is(err, errLoginLimited)):
			t.Errorf("HTTP %d: err = %v, want a generic error", tc.status, err)
		case tc.want != nil && !errors.Is(tc.want, errOther) && !errors.Is(err, tc.want):
			t.Errorf("HTTP %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

var errOther = errors.New("other")
