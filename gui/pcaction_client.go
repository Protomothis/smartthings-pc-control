package gui

import (
	"encoding/json"
	"net/http"
)

// API calls behind [테스트 알림] (#106) and the preset [실행]/[테스트]
// buttons (#109): user-session actions the service runs for the app.

// actionError is a refused or failed user-session action: Code is the
// service's wire code (no_user_session, notify_disabled, rate_limited,
// no_such_preset, timeout, unsupported, failed, …) and Message its text.
// The tab words the common codes itself (actionErrorKey).
type actionError struct {
	Code    string
	Message string
}

func (e *actionError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// errActionUnsupported is what an older service answers these routes with
// (the WebUI mux sends unknown paths to the settings page).
var errActionUnsupported = &actionError{Code: "service_too_old", Message: "needs service v1.2.0"}

// actionCall POSTs body to path and decodes an ok reply into out.
func (c *Client) actionCall(path string, body, out any) error {
	resp, err := c.do("POST", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if out == nil {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusUnauthorized:
		return errUnauthorized
	}
	var r struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || (r.Error == "" && r.Status == "") {
		// Not one of ours: the settings page or a 404 of an older service.
		return errActionUnsupported
	}
	if r.Error == "" {
		r.Error = "failed"
	}
	return &actionError{Code: r.Error, Message: r.Message}
}

// NotifyResult is the ok reply of /api/notify/test.
type NotifyResult struct {
	Toast      string `json:"toast"`
	Spoken     bool   `json:"spoken"`
	VoiceUsed  string `json:"voice_used"`
	VoiceFound *bool  `json:"voice_found"`
	SpeakError string `json:"speak_error"`
}

// TestNotify shows a test notification on this PC with the form's
// (possibly unsaved) speech settings.
func (c *Client) TestNotify(speak bool, voice string) (NotifyResult, error) {
	var r NotifyResult
	err := c.actionCall("/api/notify/test", map[string]any{"speak": speak, "voice": voice}, &r)
	return r, err
}

// RunPreset starts the saved preset in slot.
func (c *Client) RunPreset(slot int) error {
	return c.actionCall("/api/presets/run", map[string]int{"slot": slot}, nil)
}

// TestPreset starts p as typed in the editor, before it is saved.
func (c *Client) TestPreset(p Preset) error {
	return c.actionCall("/api/presets/test", p, nil)
}
