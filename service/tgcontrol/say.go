package tgcontrol

// Telegram /say 문구 (#106): a toast on this PC. It goes through
// the Notifier like /st/v1/notify: the same text rules, the per-source
// rate limit (keyed by chat) and the user-session requirement.

import (
	"context"
	"errors"
	"html"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// sayTexts are merged into texts at start-up; ko then en. They live
// here rather than in texts so each feature keeps its strings with it.
var sayTexts = map[string][2]string{
	"say_usage":        {"사용법: <code>/say 문구</code> (200자까지)", "Usage: <code>/say text</code> (up to 200 characters)"},
	"say_ok":           {"🔔 PC에 알림을 띄웠습니다", "🔔 Notification shown on the PC"},
	"say_disabled":     {"PC 알림이 꺼져 있습니다 (PC 앱의 설정 탭 → 미디어·알림에서 켤 수 있습니다)", "PC notifications are turned off (turn them on under Settings → Media &amp; notifications in the PC app)"},
	"say_bad_text":     {"문구는 1~200자여야 합니다", "The text must be 1–200 characters"},
	"say_rate_limited": {"알림이 너무 잦습니다. %d초 뒤에 다시 보내 주세요 (분당 10개)", "Too many notifications; send again in %d s (10 a minute)"},
	"no_user_session":  {"PC에 로그인한 사용자가 없어 할 수 없습니다", "Nobody is logged in on the PC"},
	"action_timeout":   {"PC가 제때 응답하지 않았습니다", "The PC did not answer in time"},
	"action_failed":    {"실패: %s", "Failed: %s"},
}

func init() {
	for k, v := range sayTexts {
		texts[k] = v
	}
}

// joinText is the argument as typed: the poller splits on whitespace,
// the notification (or a preset name) wants the words back together.
func joinText(args []string) string { return strings.Join(args, " ") }

// say handles /say 문구.
func (c *Control) say(chatID string, args []string) (string, *telegram.InlineKeyboard, error) {
	text := joinText(args)
	if strings.TrimSpace(text) == "" {
		return c.text("say_usage"), nil, nil
	}
	cfg := c.d.Config().NotifyPC
	ctx, cancel := context.WithTimeout(context.Background(), c.d.ActionTimeout()+time.Second)
	defer cancel()
	if _, err := c.d.Notify.Send(ctx, cfg, "telegram "+chatID, "", text); err != nil {
		return c.actionError(err), nil, err
	}
	return c.text("say_ok"), nil, nil
}

// actionError words a failed user-session action (/say, /run).
func (c *Control) actionError(err error) string {
	var ne *action.NotifyError
	if errors.As(err, &ne) {
		switch ne.Code {
		case "notify_disabled":
			return c.text("say_disabled")
		case "bad_text":
			return c.text("say_bad_text")
		case "rate_limited":
			secs := int((ne.RetryAfter + time.Second - 1) / time.Second)
			return c.text("say_rate_limited", max(secs, 1))
		}
	}
	switch _, code, msg := action.Status(err); code {
	case "no_user_session":
		return c.text("no_user_session")
	case "timeout":
		return c.text("action_timeout")
	default:
		return c.text("action_failed", html.EscapeString(httpx.Truncate(msg, 200)))
	}
}
