package telegram

import (
	"context"
	"errors"
	"strings"
	"time"
)

// CommandHandler is what the service implements to act on inbound commands
// (design doc §7, §9). The Poller only parses, authorises and transports.
type CommandHandler interface {
	// HandleCommand answers a slash command ("status", "shutdown", ...) from
	// an allowed chat. cmd is lower-case without the leading slash or a
	// @botname suffix. The returned html is sent as the reply; kb, when
	// non-nil, is attached as an inline keyboard (confirmation prompts).
	HandleCommand(ctx context.Context, chatID string, cmd string, args []string) (html string, kb *InlineKeyboard, err error)
	// HandleCallback acts on an inline-button press. editHTML, when
	// non-empty, replaces the message the button belonged to (and removes
	// its keyboard unless the handler also implements EditKeyboarder);
	// toast is shown to the user through answerCallbackQuery.
	HandleCallback(ctx context.Context, chatID string, msgID int, data string) (editHTML string, toast string, err error)
	// Unauthorized is told about traffic from chats that are not allowed
	// so the service can raise security.unknown_chat.
	Unauthorized(chatID, username, text string)
}

// EditKeyboarder is an optional extension of CommandHandler. When the
// handler implements it, the edit that follows HandleCallback carries the
// returned keyboard instead of removing the buttons — this is how a
// confirm:<cmd> button turns a menu into a [confirm][cancel] prompt (#62).
type EditKeyboarder interface {
	EditKeyboard(data string) *InlineKeyboard
}

// PollerOptions configures NewPoller.
type PollerOptions struct {
	// AllowedChatIDs is consulted on every update so config edits apply
	// without a restart. nil (or an empty result) allows nobody.
	AllowedChatIDs func() []string
	Handler        CommandHandler
	Log            func(string, ...any)
}

const (
	defaultPollTimeout = 30 // seconds, getUpdates long poll
	minBackoff         = time.Second
	maxBackoff         = 60 * time.Second
)

// Poller long-polls getUpdates and routes messages and button presses to
// a CommandHandler.
type Poller struct {
	cli  *Client
	opts PollerOptions

	// PollTimeout is the getUpdates long-poll timeout in seconds.
	PollTimeout int
	// sleep waits out a back-off; tests replace it to avoid real delays.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewPoller returns a poller for cli. Run must be called to start it.
func NewPoller(cli *Client, opts PollerOptions) *Poller {
	return &Poller{cli: cli, opts: opts, PollTimeout: defaultPollTimeout, sleep: sleepCtx}
}

// Run polls until ctx is done and returns ctx.Err(). On start it asks for
// only the newest queued update (offset -1, which makes Telegram forget the
// rest) and discards it, so commands typed while the service was down are
// never executed. Errors back off 1s→2s→…→60s (or Telegram's retry_after
// when larger) and the delay resets after any successful call.
func (p *Poller) Run(ctx context.Context) error {
	if p.cli == nil || p.opts.Handler == nil {
		return errors.New("telegram poller: client and handler are required")
	}
	offset := 0
	drained := false
	backoff := minBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var (
			upds []Update
			err  error
		)
		if !drained {
			upds, err = p.cli.GetUpdates(ctx, -1, 0)
		} else {
			upds, err = p.cli.GetUpdates(ctx, offset, p.PollTimeout)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			wait := backoff
			if ra, ok := RetryAfterOf(err); ok && ra > wait {
				wait = ra
			}
			p.logf("poller: getUpdates failed: %v (retry in %s)", err, wait)
			if err := p.sleep(ctx, wait); err != nil {
				return err
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = minBackoff
		for _, u := range upds {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
		}
		if !drained {
			drained = true
			if len(upds) > 0 {
				p.logf("poller: discarded updates queued before start (next offset %d)", offset)
			}
			continue
		}
		for _, u := range upds {
			p.handle(ctx, u)
		}
	}
}

func (p *Poller) handle(ctx context.Context, u Update) {
	switch {
	case u.CallbackQuery != nil:
		p.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		p.handleMessage(ctx, u.Message)
	}
}

// handleMessage routes a text message: commands go to HandleCommand, any
// other text from an allowed chat is answered with /help. Messages without
// text (stickers, photos, joins) are ignored.
func (p *Poller) handleMessage(ctx context.Context, m *Message) {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	chatID := m.Chat.IDString()
	if !p.allowed(chatID) {
		p.opts.Handler.Unauthorized(chatID, userName(m.From), text)
		return
	}
	cmd, args := ParseCommand(text)
	if cmd == "" {
		cmd, args = "help", nil
	}
	html, kb, err := p.opts.Handler.HandleCommand(ctx, chatID, cmd, args)
	if err != nil {
		p.logf("poller: /%s from %s: %v", cmd, chatID, err)
	}
	if html == "" {
		return
	}
	if _, err := p.cli.SendMessage(ctx, chatID, html, kb); err != nil {
		p.logf("poller: reply to /%s failed: %v", cmd, err)
	}
}

// handleCallback answers a button press and, when the handler asks for it,
// edits the message the button was on.
func (p *Poller) handleCallback(ctx context.Context, cq *CallbackQuery) {
	chatID, msgID := "", 0
	if cq.Message != nil {
		chatID = cq.Message.Chat.IDString()
		msgID = cq.Message.MessageID
	}
	if !p.allowed(chatID) {
		p.opts.Handler.Unauthorized(chatID, userName(&cq.From), cq.Data)
		return
	}
	editHTML, toast, err := p.opts.Handler.HandleCallback(ctx, chatID, msgID, cq.Data)
	if err != nil {
		p.logf("poller: callback %q from %s: %v", cq.Data, chatID, err)
	}
	if err := p.cli.AnswerCallbackQuery(ctx, cq.ID, toast); err != nil {
		p.logf("poller: answerCallbackQuery failed: %v", err)
	}
	if editHTML == "" || msgID == 0 {
		return
	}
	var kb *InlineKeyboard
	if ek, ok := p.opts.Handler.(EditKeyboarder); ok {
		kb = ek.EditKeyboard(cq.Data)
	}
	if err := p.cli.EditMessageText(ctx, chatID, msgID, editHTML, kb); err != nil {
		p.logf("poller: editMessageText failed: %v", err)
	}
}

// allowed reports whether chatID is in the current allow-list.
func (p *Poller) allowed(chatID string) bool {
	if chatID == "" || p.opts.AllowedChatIDs == nil {
		return false
	}
	for _, id := range p.opts.AllowedChatIDs() {
		if strings.TrimSpace(id) == chatID {
			return true
		}
	}
	return false
}

func (p *Poller) logf(format string, args ...any) {
	if p.opts.Log != nil {
		p.opts.Log("telegram: "+format, args...)
	}
}

// ParseCommand splits "/shutdown@my_bot 30" into ("shutdown", ["30"]).
// The command is lower-cased and a @botname suffix is dropped. Text that
// is not a command yields ("", nil).
func ParseCommand(text string) (cmd string, args []string) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", nil
	}
	head := fields[0][1:]
	if i := strings.IndexByte(head, '@'); i >= 0 {
		head = head[:i]
	}
	if head == "" {
		return "", nil
	}
	return strings.ToLower(head), fields[1:]
}

// userName picks something printable for security.unknown_chat.
func userName(u *User) string {
	if u == nil {
		return ""
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
