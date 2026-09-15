package service

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// telegramBaseURL is where every telegram.Client built by the service
// points. Tests swap it for an httptest.Server; production leaves the
// public endpoint.
var telegramBaseURL = telegram.DefaultBaseURL

// telegramAPITimeout bounds the one-shot calls made on behalf of the
// WebUI (/api/telegram/test, /me, /chats).
const telegramAPITimeout = 15 * time.Second

// newTelegramClient builds a Bot API client for a plaintext token. Callers
// must secret.Unprotect the stored config value first (see liveBotToken).
func newTelegramClient(token string) *telegram.Client {
	cli := telegram.NewClient(token, telegram.WithBaseURL(telegramBaseURL))
	cli.Log = logMsg
	return cli
}

// liveBotToken returns the decrypted bot token from a TelegramConfig.
// config.json stores it DPAPI-protected (issue #65); every consumer —
// this sink, the WebUI endpoints, the #61 poller — must go through here
// (or secret.Unprotect) rather than using BotToken directly.
func liveBotToken(tg TelegramConfig) (string, error) {
	return secret.Unprotect(tg.BotToken)
}

// telegramPCName is the footer name: telegram.pc_name or the hostname.
func telegramPCName(tg TelegramConfig) string {
	if tg.PCName != "" {
		return tg.PCName
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "PC"
}

// telegramConfigured reports whether the channel can deliver at all.
func telegramConfigured(tg TelegramConfig) bool {
	return tg.Enabled && tg.BotToken != "" && tg.ChatID != ""
}

// liveSinkKey is everything the inner telegram.Sink is built from; a change
// in any field rebuilds it on the next Send.
type liveSinkKey struct {
	token  string // plaintext
	chatID string
	lang   string
	detail string
	pcName string
}

// liveSink is the notify.Sink installed by startNotifier. It re-reads
// getConfig().Telegram on every Send so enabling Telegram, changing the
// token or the language takes effect without a service restart. Events
// arriving while Telegram is disabled or incomplete are dropped silently
// (one log line per state change).
type liveSink struct {
	mu       sync.Mutex
	key      liveSinkKey
	inner    *telegram.Sink
	onSent   func(ev notify.Event, msgID int, html string)
	disabled bool // last observed state, for the one-time log line
	started  bool
}

// newLiveSink returns the config-following Telegram sink. The concrete type
// is returned (not notify.Sink) so callers can attach SetOnSent before
// handing it to startNotifier.
func newLiveSink() *liveSink {
	return &liveSink{}
}

// SetOnSent forwards to telegram.Sink.OnSent (issue #62 records the message
// id and rendered HTML of remote.grace_scheduled with it, see
// rememberGraceMessage). The callback survives inner-sink rebuilds caused
// by config changes. It runs on the notify.Bus worker goroutine, so it must
// be quick and non-blocking.
func (s *liveSink) SetOnSent(fn func(ev notify.Event, msgID int, html string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSent = fn
	if s.inner != nil {
		s.inner.OnSent = fn
	}
}

// Send implements notify.Sink. Errors from the inner sink — including
// *telegram.RateLimitError — are returned as-is so the bus throttle can
// back off.
func (s *liveSink) Send(ctx context.Context, ev notify.Event) error {
	tg := getConfig().Telegram
	if !telegramConfigured(tg) {
		s.noteState(true)
		return nil
	}
	token, err := liveBotToken(tg)
	if err != nil {
		return fmt.Errorf("telegram sink: bot token: %w", err)
	}
	inner, err := s.sinkFor(liveSinkKey{
		token:  token,
		chatID: tg.ChatID,
		lang:   tg.Lang,
		detail: tg.Detail,
		pcName: telegramPCName(tg),
	})
	if err != nil {
		return err
	}
	s.noteState(false)
	return inner.Send(ctx, ev)
}

// sinkFor returns the cached inner sink for key, rebuilding it when the
// key differs from the one it was built with.
func (s *liveSink) sinkFor(key liveSinkKey) (*telegram.Sink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inner != nil && s.key == key {
		return s.inner, nil
	}
	r, err := telegram.NewRenderer(key.lang, key.detail)
	if err != nil {
		return nil, fmt.Errorf("telegram sink: %w", err)
	}
	inner := telegram.NewSink(newTelegramClient(key.token), key.chatID, r, key.pcName)
	inner.OnSent = s.onSent
	s.inner = inner
	s.key = key
	logMsg("Telegram sink ready (chat_id=%s, lang=%s, detail=%s)", key.chatID, key.lang, key.detail)
	return inner, nil
}

// noteState logs once whenever the configured/unconfigured state flips.
func (s *liveSink) noteState(disabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && s.disabled == disabled {
		return
	}
	s.started = true
	s.disabled = disabled
	if disabled {
		logMsg("Telegram notifications are off or incomplete (enabled/bot_token/chat_id); events are dropped")
	}
}
