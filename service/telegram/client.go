// Package telegram talks to the Telegram Bot API (outbound sendMessage and
// friends, getUpdates long polling) and adapts it to notify.Sink. See
// docs/design/v1.0-notifications.md sections 7 and 8; this package must
// never import the service package.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the public Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

const (
	// callTimeout bounds ordinary calls when the caller's ctx has no deadline.
	callTimeout = 30 * time.Second
	// pollSlack is added to the long-poll timeout for the HTTP round trip.
	pollSlack = 15 * time.Second
	// maxBody caps how much of a response we read (Bot API responses are small).
	maxBody = 4 << 20
)

// Client is a minimal Bot API client. Zero-value is not usable; use NewClient.
type Client struct {
	token string
	base  string
	http  *http.Client

	// Log, when set, receives one debug line per request ("sendMessage ok"
	// / "getUpdates: 429 ..."). The token never appears in these lines.
	Log func(format string, args ...any)
}

// ClientOption customises NewClient.
type ClientOption func(*Client)

// WithHTTPClient replaces the underlying *http.Client (tests, proxies).
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithBaseURL points the client at another server, e.g. an httptest.Server.
// A trailing slash is tolerated.
func WithBaseURL(base string) ClientOption {
	return func(c *Client) {
		if base != "" {
			c.base = strings.TrimRight(base, "/")
		}
	}
}

// NewClient returns a client for the given bot token.
func NewClient(token string, opts ...ClientOption) *Client {
	c := &Client{
		token: token,
		base:  DefaultBaseURL,
		http:  &http.Client{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ---- wire types (only the fields we need) --------------------------------

// InlineKeyboard is a reply_markup with rows of buttons.
type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

// InlineButton is one button; exactly one of CallbackData or URL is set.
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Update is one getUpdates entry.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a chat message (incoming, or the one returned by sendMessage).
type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text,omitempty"`
}

// Chat identifies a private chat, group or channel.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// IDString returns the chat id in the string form used by config and the
// rest of this package.
func (c Chat) IDString() string { return fmt.Sprint(c.ID) }

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// CallbackQuery is an inline button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// BotCommand is one entry for setMyCommands.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// ---- errors ---------------------------------------------------------------

// APIError is a non-ok Bot API response other than 429.
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.Code, e.Description)
}

// RateLimitError is returned for HTTP 429 / "Too Many Requests". RetryAfter
// comes from parameters.retry_after (1s when Telegram sends none) so the
// notify throttle can back off precisely.
type RateLimitError struct {
	RetryAfter  time.Duration
	Description string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("telegram rate limited, retry after %s: %s", e.RetryAfter, e.Description)
}

// RetryAfterOf reports the back-off Telegram asked for if err is (or wraps)
// a *RateLimitError.
func RetryAfterOf(err error) (time.Duration, bool) {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return rl.RetryAfter, true
	}
	return 0, false
}

// ---- API methods ------------------------------------------------------------

// GetMe returns the bot's own user record; useful as a token check.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// SendMessage sends HTML text (with web page previews disabled) and an
// optional inline keyboard. It returns the new message id.
func (c *Client) SendMessage(ctx context.Context, chatID string, html string, kb *InlineKeyboard) (int, error) {
	req := struct {
		ChatID                string          `json:"chat_id"`
		Text                  string          `json:"text"`
		ParseMode             string          `json:"parse_mode"`
		DisableWebPagePreview bool            `json:"disable_web_page_preview"`
		ReplyMarkup           *InlineKeyboard `json:"reply_markup,omitempty"`
	}{chatID, html, "HTML", true, kb}
	var msg Message
	if err := c.call(ctx, "sendMessage", req, &msg); err != nil {
		return 0, err
	}
	return msg.MessageID, nil
}

// EditMessageText replaces the text of a sent message. Passing kb == nil
// removes the inline keyboard, which is what callers want after a button
// has been handled.
func (c *Client) EditMessageText(ctx context.Context, chatID string, msgID int, html string, kb *InlineKeyboard) error {
	req := struct {
		ChatID                string          `json:"chat_id"`
		MessageID             int             `json:"message_id"`
		Text                  string          `json:"text"`
		ParseMode             string          `json:"parse_mode"`
		DisableWebPagePreview bool            `json:"disable_web_page_preview"`
		ReplyMarkup           *InlineKeyboard `json:"reply_markup,omitempty"`
	}{chatID, msgID, html, "HTML", true, kb}
	// editMessageText returns the Message (or true for inline messages);
	// we don't need either.
	return c.call(ctx, "editMessageText", req, nil)
}

// AnswerCallbackQuery dismisses the button spinner, optionally with a toast.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id string, text string) error {
	req := struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}{id, text}
	return c.call(ctx, "answerCallbackQuery", req, nil)
}

// GetUpdates long-polls for up to timeoutSec seconds starting at offset.
// Only message and callback_query updates are requested.
func (c *Client) GetUpdates(ctx context.Context, offset int, timeoutSec int) ([]Update, error) {
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	req := struct {
		Offset         int      `json:"offset"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, timeoutSec, []string{"message", "callback_query"}}
	var out []Update
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second+pollSlack)
		defer cancel()
	}
	err := c.callTimeout(ctx, "getUpdates", req, &out, false)
	return out, err
}

// SetMyCommands publishes the slash-command menu.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	if cmds == nil {
		cmds = []BotCommand{}
	}
	req := struct {
		Commands []BotCommand `json:"commands"`
	}{cmds}
	return c.call(ctx, "setMyCommands", req, nil)
}

// ---- transport -----------------------------------------------------------------

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts req as JSON to /bot<token>/<method> and decodes result into
// out (may be nil). A default deadline is applied when ctx has none.
func (c *Client) call(ctx context.Context, method string, req any, out any) error {
	return c.callTimeout(ctx, method, req, out, true)
}

func (c *Client) callTimeout(ctx context.Context, method string, req any, out any, applyDefault bool) error {
	if applyDefault {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, callTimeout)
			defer cancel()
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, c.maskErr(err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		err = c.maskErr(err)
		c.logf("%s: %v", method, err)
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		err = c.maskErr(err)
		c.logf("%s: read: %v", method, err)
		return fmt.Errorf("telegram %s: read: %w", method, err)
	}

	var ar apiResponse
	if jerr := json.Unmarshal(raw, &ar); jerr != nil {
		// Not a Bot API envelope (proxy page, HTML error, ...).
		if resp.StatusCode == http.StatusTooManyRequests {
			rl := &RateLimitError{RetryAfter: retryAfterHeader(resp), Description: http.StatusText(resp.StatusCode)}
			c.logf("%s: %v", method, rl)
			return rl
		}
		e := &APIError{Code: resp.StatusCode, Description: "non-JSON response: " + snippet(raw)}
		c.logf("%s: %v", method, e)
		return e
	}
	if !ar.OK {
		code := ar.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		if code == http.StatusTooManyRequests {
			ra := time.Second
			if ar.Parameters != nil && ar.Parameters.RetryAfter > 0 {
				ra = time.Duration(ar.Parameters.RetryAfter) * time.Second
			} else if h := retryAfterHeader(resp); h > 0 {
				ra = h
			}
			rl := &RateLimitError{RetryAfter: ra, Description: ar.Description}
			c.logf("%s: %v", method, rl)
			return rl
		}
		e := &APIError{Code: code, Description: ar.Description}
		c.logf("%s: %v", method, e)
		return e
	}
	if out != nil && len(ar.Result) > 0 {
		if err := json.Unmarshal(ar.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	c.logf("%s ok", method)
	return nil
}

func (c *Client) methodURL(method string) string {
	return c.base + "/bot" + c.token + "/" + method
}

// maskErr removes the bot token from transport errors. *url.Error embeds the
// full request URL (which carries the token in its path), so the URL field is
// rewritten in place while the wrapped cause is preserved for errors.Is.
func (c *Client) maskErr(err error) error {
	if err == nil || c.token == "" {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &url.Error{Op: ue.Op, URL: c.mask(ue.URL), Err: ue.Err}
	}
	if s := err.Error(); strings.Contains(s, c.token) {
		return errors.New(c.mask(s))
	}
	return err
}

func (c *Client) mask(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "***")
}

func (c *Client) logf(format string, args ...any) {
	if c.Log == nil {
		return
	}
	c.Log("telegram: %s", c.mask(fmt.Sprintf(format, args...)))
}

func retryAfterHeader(resp *http.Response) time.Duration {
	h := resp.Header.Get("Retry-After")
	if h == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(h, "%d", &secs); err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return s
}
