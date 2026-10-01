package tgcontrol

import (
	"context"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// This file closes the loop on the remote.grace_scheduled Telegram message
// (issue #62, design doc §8): the sink reports the message id through
// OnSent, the service remembers it for the active schedule, and when that
// schedule ends — cancelled from any path, executed by the timer or /now,
// or replaced by a newer schedule — the message is edited once: original
// HTML plus a result line, inline keyboard removed.

// graceEditTimeout bounds the best-effort editMessageText call.
const graceEditTimeout = 15 * time.Second

// GraceResult selects the result line appended to the message; the values
// are texts keys.
type GraceResult string

const (
	GraceCancelled GraceResult = "stamp_cancelled" // ✅ 취소됨 · HH:MM · <by>
	GraceExecuted  GraceResult = "stamp_ran"       // ▶️ 실행됨 · HH:MM · <by>
	GraceReplaced  GraceResult = "stamp_replaced"  // 🔁 대체됨 · HH:MM
)

// graceMessage is the Telegram message announcing one grace schedule.
type graceMessage struct {
	seq    uint64 // schedule it belongs to (power.Task.Seq)
	chatID string
	msgID  int
	html   string // rendered text as sent, so the edit can keep it
}

// graceStore holds at most one message: there is a single schedule slot.
type graceStore struct {
	mu  sync.Mutex
	cur graceMessage
}

// GraceMessage is the remembered grace message: its chat, its id (0 when
// none is remembered) and the HTML it was sent with (tests).
func (c *Control) GraceMessage() (chatID string, msgID int, html string) {
	c.grace.mu.Lock()
	defer c.grace.mu.Unlock()
	return c.grace.cur.chatID, c.grace.cur.msgID, c.grace.cur.html
}

// RememberGraceMessage is the live Telegram sink's OnSent hook. It keeps the message
// that announced the remote grace schedule which is active right now; any
// other event, or a grace message whose schedule already ended before it
// went out, is ignored. It runs on the notify.Bus worker goroutine.
func (c *Control) RememberGraceMessage(ev notify.Event, msgID int, html string) {
	if ev.Category != "remote" || ev.Kind != "grace_scheduled" || msgID == 0 {
		return
	}
	seq, ok := c.d.Commands.GraceSeq(ev.Fields["command"])
	if !ok {
		return
	}
	c.grace.mu.Lock()
	c.grace.cur = graceMessage{seq: seq, chatID: c.d.Config().Telegram.ChatID, msgID: msgID, html: html}
	c.grace.mu.Unlock()
}

// takeGraceMessage claims the stored message when it is the one a button
// was pressed on, so the Telegram callback path can edit it itself (with
// by=telegram) and FinishGraceMessage will not edit it a second time.
func (c *Control) takeGraceMessage(chatID string, msgID int) (graceMessage, bool) {
	c.grace.mu.Lock()
	defer c.grace.mu.Unlock()
	m := c.grace.cur
	if m.msgID == 0 || m.msgID != msgID || m.chatID != chatID {
		return graceMessage{}, false
	}
	c.grace.cur = graceMessage{}
	return m, true
}

// FinishGraceMessage is called by the scheduler's hooks (service/schedule.go, under
// its lock) whenever the schedule with seq ends. When a message is stored for that
// schedule it is edited in the background — best effort, never blocking
// the caller — and forgotten, so the edit happens at most once. by is the
// cancel/run-now origin (toast, tray, app, webui, api, telegram, timer);
// it is omitted from the line when empty (replaced).
func (c *Control) FinishGraceMessage(seq uint64, result GraceResult, by string) {
	c.grace.mu.Lock()
	m := c.grace.cur
	if m.msgID == 0 || m.seq != seq {
		c.grace.mu.Unlock()
		return
	}
	c.grace.cur = graceMessage{}
	c.grace.mu.Unlock()
	// The base URL is read here rather than in the goroutine: tests point
	// it at a fake Bot API and restore it as soon as the caller returns.
	go c.editGraceMessage(m, c.stampBy(string(result), by), c.d.BaseURL())
}

// ResetGraceMessage forgets any stored message (tests).
func (c *Control) ResetGraceMessage() {
	c.grace.mu.Lock()
	c.grace.cur = graceMessage{}
	c.grace.mu.Unlock()
}

// editGraceMessage appends line to the stored message and removes its
// keyboard. Failures are logged only: the schedule outcome is already
// final and a second notification (grace_cancelled / executed) went out.
func (c *Control) editGraceMessage(m graceMessage, line, baseURL string) {
	token, err := secret.Unprotect(c.d.Config().Telegram.BotToken)
	if err != nil || token == "" {
		logx.Printf("Telegram: grace message %d not edited: bot token unavailable (%v)", m.msgID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), graceEditTimeout)
	defer cancel()
	cli := telegram.NewClient(token, telegram.WithBaseURL(baseURL))
	cli.Log = logx.Printf
	if err := cli.EditMessageText(ctx, m.chatID, m.msgID, m.html+"\n"+line, nil); err != nil {
		logx.Printf("Telegram: grace message %d edit failed: %v", m.msgID, err)
		return
	}
	logx.Printf("Telegram: grace message %d updated (%s)", m.msgID, line)
}
