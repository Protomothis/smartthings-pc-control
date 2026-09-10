package telegram

import (
	"context"
	"fmt"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// Sink delivers notify.Events to one Telegram chat: it renders the event
// with a Renderer and maps Event.Actions to a single row of inline buttons
// whose callback_data is Action.Data verbatim.
type Sink struct {
	cli    *Client
	chatID string
	r      *Renderer
	pcName string

	// OnSent, when set, is invoked synchronously after every successful
	// sendMessage with the event and the id Telegram assigned to the
	// message. Issue #62 uses it to remember the grace-period message so
	// the inline keyboard can later be edited away (EditMessageText). It
	// runs on the notify.Bus worker goroutine, so keep it quick and
	// non-blocking; errors from Send are not reported through the hook.
	OnSent func(ev notify.Event, msgID int)
}

// NewSink wires a client, target chat, renderer and PC name together.
// pcName is what the footer shows; pass the hostname when config is empty.
func NewSink(cli *Client, chatID string, r *Renderer, pcName string) *Sink {
	return &Sink{cli: cli, chatID: chatID, r: r, pcName: pcName}
}

// ChatID returns the destination chat id.
func (s *Sink) ChatID() string { return s.chatID }

// Send implements notify.Sink. A *RateLimitError from the client is
// returned unwrapped so the notify throttle can inspect RetryAfter.
func (s *Sink) Send(ctx context.Context, ev notify.Event) error {
	if s.cli == nil || s.r == nil {
		return fmt.Errorf("telegram sink: not configured")
	}
	if s.chatID == "" {
		return fmt.Errorf("telegram sink: chat_id is empty")
	}
	html, err := s.r.Render(ev, s.pcName)
	if err != nil {
		return fmt.Errorf("telegram sink: render %s: %w", ev.Key(), err)
	}
	msgID, err := s.cli.SendMessage(ctx, s.chatID, html, Keyboard(ev.Actions))
	if err != nil {
		return err
	}
	if s.OnSent != nil {
		s.OnSent(ev, msgID)
	}
	return nil
}

// Keyboard turns actions into one row of callback buttons. Actions with an
// empty label are skipped; nil is returned when nothing remains so the
// request omits reply_markup entirely.
func Keyboard(actions []notify.Action) *InlineKeyboard {
	row := make([]InlineButton, 0, len(actions))
	for _, a := range actions {
		if a.Label == "" {
			continue
		}
		row = append(row, InlineButton{Text: a.Label, CallbackData: a.Data})
	}
	if len(row) == 0 {
		return nil
	}
	return &InlineKeyboard{InlineKeyboard: [][]InlineButton{row}}
}
