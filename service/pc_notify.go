package service

// PC notification (#106, docs/design/media-notify.md §3·§4·§6·§7): put a
// toast on the logged-in user's screen — and optionally read it aloud —
// from SmartThings (POST /st/v1/notify), Telegram (/say) or the app's test
// button (POST /api/notify/test). All three share sendPCNotify: the same
// text rules, the same per-source rate limit and the same user-action run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// NotifyPCConfig is the "notify_pc" object in config.json (§4).
type NotifyPCConfig struct {
	// Enabled allows SmartThings and Telegram to show a toast on this PC
	// (default on). Off, /st/v1/notify answers 403 notify_disabled.
	Enabled bool `json:"enabled"`
	// Speak reads the text aloud as well (default off). A request's
	// "speak" is honoured only while this is on.
	Speak bool `json:"speak"`
	// Voice is the SAPI voice to read with, matched against the installed
	// voices' names (a part is enough). Empty or unknown: the system default.
	Voice string `json:"voice"`
}

const (
	// pcNotifyDefaultTitle is the toast title when a request sends none.
	pcNotifyDefaultTitle = "SmartThings"
	// pcNotifyMaxText and pcNotifyMaxTitle are counted in characters
	// (runes) after control characters are removed (§3).
	pcNotifyMaxText  = useraction.MaxTextRunes
	pcNotifyMaxTitle = useraction.MaxTitleRunes
	// pcNotifyPerMinute is how many notifications one source may send in
	// any 60 seconds (§3, §7).
	pcNotifyPerMinute = 10
	pcNotifyWindow    = time.Minute
)

// validateNotifyPC checks the settings a client can get wrong.
func validateNotifyPC(c NotifyPCConfig) string {
	if utf8.RuneCountInString(c.Voice) > useraction.MaxVoiceRunes {
		return fmt.Sprintf("notify_pc.voice must be at most %d characters", useraction.MaxVoiceRunes)
	}
	if strings.IndexFunc(c.Voice, unicode.IsControl) >= 0 {
		return "notify_pc.voice must not contain control characters"
	}
	return ""
}

// cleanNotifyText removes what must not reach a toast: line breaks and
// tabs become spaces, every other control character and the bidirectional
// overrides (which could make a toast read differently from what was sent)
// are dropped, and runs of spaces collapse. Surrounding space is trimmed.
func cleanNotifyText(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // \n, \r, \t included
			space = true
			continue
		case unicode.IsControl(r), isBidiControl(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// isBidiControl reports the explicit directional formatting characters
// (LRE…RLO, LRI…PDI) and the marks ALM, LRM, RLM.
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x061C || r == 0x200E || r == 0x200F
}

// pcNotifyError is a request the notify rules refuse. Code is the wire
// error ("notify_disabled", "bad_text", "rate_limited").
type pcNotifyError struct {
	Code       string
	Message    string
	RetryAfter time.Duration // rate_limited only
}

func (e *pcNotifyError) Error() string { return e.Code + ": " + e.Message }

// prepareNotify validates and cleans one request's title and text.
func prepareNotify(title, text string) (string, string, error) {
	text = cleanNotifyText(text)
	if n := utf8.RuneCountInString(text); n == 0 || n > pcNotifyMaxText {
		return "", "", &pcNotifyError{Code: "bad_text",
			Message: fmt.Sprintf("text must be 1-%d characters (got %d)", pcNotifyMaxText, n)}
	}
	title = cleanNotifyText(title)
	if title == "" {
		title = pcNotifyDefaultTitle
	}
	if n := utf8.RuneCountInString(title); n > pcNotifyMaxTitle {
		return "", "", &pcNotifyError{Code: "bad_text",
			Message: fmt.Sprintf("title must be at most %d characters (got %d)", pcNotifyMaxTitle, n)}
	}
	return title, text, nil
}

// ---- rate limit ---------------------------------------------------------

// notifyLimiter allows pcNotifyPerMinute notifications per source in any
// sliding window of pcNotifyWindow. now is injectable for the tests.
type notifyLimiter struct {
	mu   sync.Mutex
	now  func() time.Time
	hits map[string][]time.Time
}

func newNotifyLimiter(now func() time.Time) *notifyLimiter {
	return &notifyLimiter{now: now, hits: map[string][]time.Time{}}
}

// allow records one notification from key, or reports how long until the
// oldest one in the window expires.
func (l *notifyLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-pcNotifyWindow)
	if len(l.hits) > 256 {
		// Sources that went quiet are worthless; keep the map small.
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= pcNotifyPerMinute {
		l.hits[key] = kept
		return false, kept[0].Sub(cutoff)
	}
	l.hits[key] = append(kept, now)
	return true, 0
}

// pcNotifyLimits is the live limiter, replaced by the tests.
var pcNotifyLimits = newNotifyLimiter(time.Now)

// ---- running -----------------------------------------------------------

// pcNotifyRun is runUserAction, replaced by the tests.
var pcNotifyRun = runUserAction

// pcNotifyResult is what the user-action child reported.
type pcNotifyResult struct {
	Toast      string `json:"toast"`                 // "shown" or "pending"
	Spoken     bool   `json:"spoken"`                // reading aloud started
	VoiceUsed  string `json:"voice_used,omitempty"`  // the voice it reads with
	VoiceFound *bool  `json:"voice_found,omitempty"` // false: the configured voice is missing, default used
	SpeakError string `json:"speak_error,omitempty"`
}

// notifyArgs is the user-action argument vector for one notification.
func notifyArgs(title, text string, speak bool, voice string) []string {
	args := []string{useraction.ActionNotify, "--title", title, "--text", text}
	if speak {
		args = append(args, "--speak")
		if strings.TrimSpace(voice) != "" {
			args = append(args, "--voice", voice)
		}
	}
	return args
}

// sendPCNotify shows one notification. source keys the rate limit ("ip
// 192.168.1.20", "telegram 12345", "app"). speak is the request's wish;
// it is honoured only when notify_pc.speak is on. checkEnabled is false
// for the app's test button, which must work before the feature is on.
func sendPCNotify(ctx context.Context, cfg NotifyPCConfig, checkEnabled bool, source, title, text string, speak bool) (pcNotifyResult, error) {
	if checkEnabled && !cfg.Enabled {
		return pcNotifyResult{}, &pcNotifyError{Code: "notify_disabled", Message: "PC notifications are turned off in the app settings"}
	}
	title, text, err := prepareNotify(title, text)
	if err != nil {
		return pcNotifyResult{}, err
	}
	if ok, wait := pcNotifyLimits.allow(source); !ok {
		return pcNotifyResult{}, &pcNotifyError{Code: "rate_limited", RetryAfter: wait,
			Message: fmt.Sprintf("at most %d notifications a minute", pcNotifyPerMinute)}
	}
	speak = speak && cfg.Speak
	res, err := pcNotifyRun(ctx, notifyArgs(title, text, speak, cfg.Voice)...)
	if err != nil {
		logMsg("PC notify (%s) failed: %v", source, err)
		return pcNotifyResult{}, err
	}
	var out pcNotifyResult
	for key, dst := range map[string]any{
		"toast": &out.Toast, "spoken": &out.Spoken, "voice_used": &out.VoiceUsed,
		"voice_found": &out.VoiceFound, "speak_error": &out.SpeakError,
	} {
		if raw, ok := res.Fields[key]; ok {
			json.Unmarshal(raw, dst)
		}
	}
	// The text itself is not logged: it is the user's message, not ours.
	voiceNote := ""
	if out.VoiceUsed != "" {
		voiceNote = ", voice " + out.VoiceUsed
	}
	logMsg("PC notify (%s): %d chars, toast %s, speak %v%s", source, utf8.RuneCountInString(text), out.Toast, out.Spoken, voiceNote)
	if out.SpeakError != "" {
		logMsg("PC notify (%s): not read aloud: %s", source, out.SpeakError)
	}
	// Without the Start menu shortcut the toast only reaches the
	// notification center, no banner (internal/appid).
	if raw, ok := res.Fields["shortcut"]; ok {
		var s string
		json.Unmarshal(raw, &s)
		logMsg("PC notify (%s): start menu shortcut %s", source, s)
	}
	return out, nil
}

// actionErrorStatus maps a sendPCNotify / runPreset error to the HTTP
// status and wire code of /st/v1 and /api.
func actionErrorStatus(err error) (int, string, string) {
	var ne *pcNotifyError
	var ue *userActionError
	switch {
	case errors.As(err, &ne):
		switch ne.Code {
		case "notify_disabled":
			return http.StatusForbidden, ne.Code, ne.Message
		case "rate_limited":
			return http.StatusTooManyRequests, ne.Code, ne.Message
		}
		return http.StatusBadRequest, ne.Code, ne.Message
	case errors.Is(err, errNoUserSession):
		return http.StatusConflict, "no_user_session", "nobody is logged in on this PC"
	case errors.Is(err, errUserActionTimeout):
		return http.StatusGatewayTimeout, "timeout", "the user session did not answer in time"
	case errors.As(err, &ue):
		switch ue.Code {
		case useraction.CodeBadArgs:
			return http.StatusBadRequest, ue.Code, ue.Message
		case useraction.CodeUnsupported:
			return http.StatusNotImplemented, ue.Code, ue.Message
		}
		return http.StatusBadGateway, useraction.CodeFailed, ue.Message
	}
	return http.StatusBadGateway, useraction.CodeFailed, err.Error()
}

// writeActionError answers with {"error": code, "message": …} and, for a
// rate limit, Retry-After in whole seconds (at least 1).
func writeActionError(w http.ResponseWriter, err error) {
	status, code, msg := actionErrorStatus(err)
	var ne *pcNotifyError
	if errors.As(err, &ne) && ne.Code == "rate_limited" {
		secs := int((ne.RetryAfter + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	}
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// stNotifyRequest is the POST /st/v1/notify body.
type stNotifyRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Speak bool   `json:"speak"`
}

// stNotifyResponse is the 200 answer: the pcNotifyResult plus ok.
type stNotifyResponse struct {
	OK bool `json:"ok"`
	pcNotifyResult
}

// handleSTNotify serves POST /st/v1/notify (§3).
func handleSTNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body stNotifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, stMaxBody)).Decode(&body); err != nil {
		stError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from := remoteHost(r.RemoteAddr)
	res, err := sendPCNotify(r.Context(), getConfig().NotifyPC, true, "ip "+from, body.Title, body.Text, body.Speak)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stNotifyResponse{OK: true, pcNotifyResult: res})
}

// handleNotifyTestAPI serves POST /api/notify/test for the app's
// [테스트 알림] button: {speak, voice} are the form's unsaved values, so the
// user can hear a voice before saving it. The enabled switch is ignored
// (testing is how the user decides), the rate limit is not.
func handleNotifyTestAPI(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "POST") {
		return
	}
	var body struct {
		Title string `json:"title"`
		Text  string `json:"text"`
		Speak bool   `json:"speak"`
		Voice string `json:"voice"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, stMaxBody)).Decode(&body); err != nil && err != io.EOF {
			writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
	}
	cfg := NotifyPCConfig{Enabled: true, Speak: body.Speak, Voice: body.Voice}
	if msg := validateNotifyPC(cfg); msg != "" {
		writeAPIError(w, http.StatusBadRequest, msg)
		return
	}
	text := body.Text
	if strings.TrimSpace(text) == "" {
		text = "PC Control 테스트 알림입니다 · This is a test notification"
	}
	res, err := sendPCNotify(r.Context(), cfg, false, "app", body.Title, text, body.Speak)
	if err != nil {
		status, code, msg := actionErrorStatus(err)
		writeJSON(w, status, map[string]string{"status": "error", "error": code, "message": msg})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
		pcNotifyResult
	}{"ok", res})
}
