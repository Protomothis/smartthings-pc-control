package gui

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		{"limited ko", errLoginLimited, LangKo, "로그인 시도가 너무 많아요. 60초 후 다시 시도하세요."},
		{"limited en", errLoginLimited, LangEn, "Too many login attempts. Try again in 60 seconds."},
		{"invalid ko", errLoginInvalid, LangKo, "시크릿이 올바르지 않아요."},
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
