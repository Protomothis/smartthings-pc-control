package action

import (
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func TestPresetFailureHidesThePath(t *testing.T) {
	script := config.Preset{Slot: 2, Name: "live", Type: "script", Path: `C:\Users\kim\secret-project\live.ps1`, Args: []string{"--key=abc"}}
	web := config.Preset{Slot: 3, Name: "dash", Type: "url", Path: "https://example.com/d?token=abc"}
	for _, tc := range []struct {
		name   string
		err    error
		p      config.Preset
		status int
		code   string
		reason string
		msg    string
	}{
		{"not found", &session.ActionError{Code: "failed", Reason: useraction.PresetNotFound, Message: "file not found: live.ps1", Detail: script.Path},
			script, 502, "failed", useraction.PresetNotFound, "file not found: live.ps1"},
		{"denied", &session.ActionError{Code: "failed", Reason: useraction.PresetAccessDenied, Message: "x"},
			script, 502, "failed", useraction.PresetAccessDenied, "access denied: live.ps1"},
		{"older child", &session.ActionError{Code: "failed", Message: "start live.ps1: open " + script.Path + ": denied"},
			script, 502, "failed", useraction.PresetStartFailed, "could not start: live.ps1"},
		{"bad args", &session.ActionError{Code: "bad_args", Message: "preset: path " + script.Path + " is odd"},
			script, 400, "bad_args", "", "the preset cannot be run as saved: live.ps1"},
		{"url", &session.ActionError{Code: "failed", Reason: useraction.PresetURLFailed, Message: "open " + web.Path},
			web, 502, "failed", useraction.PresetURLFailed, "could not open the URL"},
		{"no session", session.ErrNoUserSession, script, 409, "no_user_session", "", "nobody is logged in on this PC"},
	} {
		f, reason := PresetFailure(tc.err, tc.p)
		if f.Status != tc.status || f.Code != tc.code || reason != tc.reason || f.Message != tc.msg {
			t.Errorf("%s: %+v reason %q, want %d %s %q %q", tc.name, f, reason, tc.status, tc.code, tc.reason, tc.msg)
		}
		for _, leak := range []string{"secret-project", "kim", "token", "--key", "example.com"} {
			if strings.Contains(f.Message, leak) || strings.Contains(f.Detail, leak) {
				t.Errorf("%s: %q leaks %q", tc.name, f.Message, leak)
			}
		}
	}
}
