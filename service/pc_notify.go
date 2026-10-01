package service

// PC notification (#106, docs/design/media-notify.md §3·§4·§6·§7): put a
// toast on the logged-in user's screen from SmartThings (POST /st/v1/notify), Telegram (/say) or the app's test
// button (POST /api/notify/test). All three share sendPCNotify: the same
// text rules, the same per-source rate limit and the same user-action run.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/stapi"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// pcNotifyPerMinute is how many notifications one source may send in
	// any 60 seconds (§3, §7).
	pcNotifyPerMinute = 10
	pcNotifyWindow    = time.Minute
)

// ---- rate limit ---------------------------------------------------------

// pcNotifyLimits allows pcNotifyPerMinute notifications per source in any
// sliding window of pcNotifyWindow. Replaced by the tests.
var pcNotifyLimits = ratelimit.New(pcNotifyPerMinute, pcNotifyWindow, time.Now)

// ---- running -----------------------------------------------------------

// pcNotifyRun is runUserAction, replaced by the tests.
var pcNotifyRun = runUserAction

// notifyArgs is the user-action argument vector for one notification.
func notifyArgs(title, text string) []string {
	return []string{useraction.ActionNotify, "--title", title, "--text", text}
}

// sendPCNotify shows one notification. source keys the rate limit ("ip
// 192.168.1.20", "telegram 12345", "app"). checkEnabled is false for the
// app's test button, which must work before the feature is on.
func sendPCNotify(ctx context.Context, cfg NotifyPCConfig, checkEnabled bool, source, title, text string) (action.NotifyResult, error) {
	if checkEnabled && !cfg.Enabled {
		return action.NotifyResult{}, &action.NotifyError{Code: "notify_disabled", Message: "PC notifications are turned off in the app settings"}
	}
	title, text, err := action.PrepareNotify(title, text)
	if err != nil {
		return action.NotifyResult{}, err
	}
	if ok, wait := pcNotifyLimits.Allow(source); !ok {
		return action.NotifyResult{}, &action.NotifyError{Code: "rate_limited", RetryAfter: wait,
			Message: fmt.Sprintf("at most %d notifications a minute", pcNotifyPerMinute)}
	}
	res, err := pcNotifyRun(ctx, notifyArgs(title, text)...)
	if err != nil {
		logMsg("PC notify (%s) failed: %v", source, err)
		return action.NotifyResult{}, err
	}
	var out action.NotifyResult
	if raw, ok := res.Fields["toast"]; ok {
		json.Unmarshal(raw, &out.Toast)
	}
	// The text itself is not logged: it is the user's message, not ours.
	logMsg("PC notify (%s): %d chars, toast %s", source, utf8.RuneCountInString(text), out.Toast)
	// Without the Start menu shortcut the toast only reaches the
	// notification center, no banner (internal/appid).
	if raw, ok := res.Fields["shortcut"]; ok {
		var s string
		json.Unmarshal(raw, &s)
		logMsg("PC notify (%s): start menu shortcut %s", source, s)
	}
	return out, nil
}

// handleNotifyTestAPI serves POST /api/notify/test for the app's
// [테스트 알림] button. The enabled switch is ignored (testing is how the
// user decides), the rate limit is not.
var handleNotifyTestAPI = apiAuth(serveNotifyTestAPI, "POST")

func serveNotifyTestAPI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, stapi.MaxBody)).Decode(&body); err != nil && err != io.EOF {
			writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
	}
	text := body.Text
	if strings.TrimSpace(text) == "" {
		text = "PC Control 테스트 알림입니다 · This is a test notification"
	}
	res, err := sendPCNotify(r.Context(), NotifyPCConfig{Enabled: true}, false, "app", body.Title, text)
	if err != nil {
		status, code, msg := action.Status(err)
		httpx.WriteJSON(w, status, map[string]string{"status": "error", "error": code, "message": msg})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
		action.NotifyResult
	}{"ok", res})
}
