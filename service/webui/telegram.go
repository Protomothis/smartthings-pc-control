package webui

// Telegram helper endpoints for the app's notify tab (#63, design doc §11).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// telegramAPITimeout bounds the one-shot Bot API calls made on behalf of
// the app (/api/telegram/test, /me, /chats).
const telegramAPITimeout = 15 * time.Second

// telegramOverride is the optional POST body that lets the GUI try values
// it has not saved yet. bot_token may be plaintext here (it travels in the
// body over localhost / the authenticated session, never in a URL).
type telegramOverride struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// decodeTelegramOverride reads an optional JSON body; an empty body or a
// non-POST request yields the zero value.
func decodeTelegramOverride(r *http.Request) (telegramOverride, error) {
	var o telegramOverride
	if r.Method != http.MethodPost || r.Body == nil {
		return o, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return o, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return o, nil
	}
	err = json.Unmarshal(raw, &o)
	return o, err
}

// telegramTarget resolves the plaintext token and chat id to use for one
// helper call: the override when it carries a value, else the live config
// (decrypted). A masked placeholder in the override means "use the live
// token" so the GUI can send its form as-is.
func telegramTarget(tg config.TelegramConfig, o telegramOverride) (token, chatID string, err error) {
	token = o.BotToken
	if token == "" || strings.HasPrefix(token, config.MaskedTokenPrefix) || token == config.ClearTokenSentinel {
		token, err = secret.Unprotect(tg.BotToken)
		if err != nil {
			return "", "", err
		}
	}
	chatID = o.ChatID
	if chatID == "" {
		chatID = tg.ChatID
	}
	return token, chatID, nil
}

// telegramCallStatus maps a Bot API failure to an HTTP status for the GUI:
// 429 when Telegram rate-limited us, 502 for anything else it (or the
// network) reported.
func telegramCallStatus(err error) int {
	if _, ok := telegram.RetryAfterOf(err); ok {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

// telegramErrorMessage extracts the Bot API description when there is one
// so the GUI shows "Unauthorized" / "chat not found" rather than the whole
// wrapped chain.
func telegramErrorMessage(err error) string {
	var api *telegram.APIError
	if errors.As(err, &api) {
		return fmt.Sprintf("Telegram API error %d: %s", api.Code, api.Description)
	}
	var rl *telegram.RateLimitError
	if errors.As(err, &rl) {
		return fmt.Sprintf("Telegram rate limited, retry after %s", rl.RetryAfter)
	}
	return err.Error()
}

// serveTelegramTest serves POST /api/telegram/test: one system.test event
// straight through a telegram.Sink (bypassing the bus so quiet hours or a
// mute cannot swallow it). Body {bot_token, chat_id} is optional and lets
// the GUI test unsaved values; otherwise the live config is used. The
// telegram.enabled flag is deliberately ignored here — testing is how the
// user decides whether to enable it.
func (s *Server) serveTelegramTest(w http.ResponseWriter, r *http.Request) {
	o, err := decodeTelegramOverride(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	tg := s.d.Config().Telegram
	token, chatID, err := telegramTarget(tg, o)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bot token cannot be decrypted on this machine; enter it again.")
		return
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: bot token is missing.")
		return
	}
	if chatID == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: chat id is missing.")
		return
	}
	rd, err := telegram.NewRenderer(tg.Lang, tg.Detail)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sink := telegram.NewSink(s.d.Telegram.Client(token), chatID, rd, s.d.Telegram.PCName(tg))
	ctx, cancel := context.WithTimeout(r.Context(), telegramAPITimeout)
	defer cancel()
	ev := notify.Event{Category: "system", Kind: "test", At: time.Now(), Fields: map[string]string{}}
	if err := sink.Send(ctx, ev); err != nil {
		logx.Printf("Telegram test message failed: %v", err)
		writeAPIError(w, telegramCallStatus(err), telegramErrorMessage(err))
		return
	}
	logx.Printf("Telegram test message sent to chat %s", chatID)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "sent"})
}

// serveTelegramMe serves GET /api/telegram/me with the live token:
// {status:"ok", username, name}. The token never comes from the URL.
func (s *Server) serveTelegramMe(w http.ResponseWriter, r *http.Request) {
	token, err := secret.Unprotect(s.d.Config().Telegram.BotToken)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bot token cannot be decrypted on this machine; enter it again.")
		return
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: bot token is missing.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramAPITimeout)
	defer cancel()
	u, err := s.d.Telegram.Client(token).GetMe(ctx)
	if err != nil {
		writeAPIError(w, telegramCallStatus(err), telegramErrorMessage(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"username": u.Username,
		"name":     strings.TrimSpace(u.FirstName + " " + u.LastName),
	})
}

// serveTelegramState serves GET /api/telegram/state: the local state of
// inbound Telegram control, with no Bot API call of its own.
//
//	{status:"ok", polling:bool, conflict:bool, since:"RFC3339"}
//
// conflict is true while getUpdates keeps answering 409 because another PC
// shares this bot token (#75); since is when that started and is omitted
// otherwise. The GUI notify tab shows a warning for it.
func (s *Server) serveTelegramState(w http.ResponseWriter, r *http.Request) {
	conflict, since := s.d.Telegram.Conflict()
	out := map[string]any{
		"status":   "ok",
		"polling":  s.d.Telegram.Polling(),
		"conflict": conflict,
	}
	if conflict && !since.IsZero() {
		out["since"] = since.Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// TelegramChat is one entry of /api/telegram/chats.
type TelegramChat struct {
	ChatID   string `json:"chat_id"`
	Title    string `json:"title"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// serveTelegramChats serves GET (live token) and POST (optional body
// {bot_token}) /api/telegram/chats: getUpdates(offset 0, timeout 0) reduced
// to the distinct chats that wrote to the bot, in first-seen order:
// {status:"ok", chats:[{chat_id, title, username, type}]}.
func (s *Server) serveTelegramChats(w http.ResponseWriter, r *http.Request) {
	o, err := decodeTelegramOverride(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	token, _, err := telegramTarget(s.d.Config().Telegram, o)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bot token cannot be decrypted on this machine; enter it again.")
		return
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: bot token is missing.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramAPITimeout)
	defer cancel()
	updates, err := s.d.Telegram.Client(token).GetUpdates(ctx, 0, 0)
	if err != nil {
		writeAPIError(w, telegramCallStatus(err), telegramErrorMessage(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "chats": distinctChats(updates)})
}

// distinctChats collects each chat once (message and callback_query
// updates alike), keeping first-seen order. Never nil, so the JSON is [].
func distinctChats(updates []telegram.Update) []TelegramChat {
	out := []TelegramChat{}
	seen := map[int64]bool{}
	add := func(c telegram.Chat) {
		if c.ID == 0 || seen[c.ID] {
			return
		}
		seen[c.ID] = true
		title := c.Title
		if title == "" {
			title = strings.TrimSpace(c.FirstName + " " + c.LastName)
		}
		out = append(out, TelegramChat{ChatID: c.IDString(), Title: title, Username: c.Username, Type: c.Type})
	}
	for _, u := range updates {
		if u.Message != nil {
			add(u.Message.Chat)
		}
		if u.CallbackQuery != nil && u.CallbackQuery.Message != nil {
			add(u.CallbackQuery.Message.Chat)
		}
	}
	return out
}
