package service

import (
	"context"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// This file closes the loop on the remote.grace_scheduled Telegram message
// (issue #62, design doc §8): the sink reports the message id through
// OnSent, the service remembers it for the active schedule, and when that
// schedule ends — cancelled from any path, executed by the timer or /now,
// or replaced by a newer schedule — the message is edited once: original
// HTML plus a result line, inline keyboard removed.

// graceEditTimeout bounds the best-effort editMessageText call.
const graceEditTimeout = 15 * time.Second

// graceResult selects the result line appended to the message; the values
// are tgTexts keys.
type graceResult string

const (
	graceCancelled graceResult = "stamp_cancelled" // ✅ 취소됨 · HH:MM · <by>
	graceExecuted  graceResult = "stamp_ran"       // ▶️ 실행됨 · HH:MM · <by>
	graceReplaced  graceResult = "stamp_replaced"  // 🔁 대체됨 · HH:MM
)

// graceMessage is the Telegram message announcing one grace schedule.
type graceMessage struct {
	seq    uint64 // schedule it belongs to (ScheduledTask.seq)
	chatID string
	msgID  int
	html   string // rendered text as sent, so the edit can keep it
}

// graceStore holds at most one message: there is a single schedule slot.
type graceStore struct {
	mu  sync.Mutex
	cur graceMessage
}

var graceMsg graceStore

// startLiveNotifier installs the Telegram sink behind the notification bus
// with the grace-message hook attached. windows.go calls it at service
// start; tests wire the pieces themselves.
func startLiveNotifier() {
	s := newLiveSink()
	s.SetOnSent(rememberGraceMessage)
	startNotifier(s)
}

// rememberGraceMessage is the liveSink OnSent hook. It keeps the message
// that announced the remote grace schedule which is active right now; any
// other event, or a grace message whose schedule already ended before it
// went out, is ignored. It runs on the notify.Bus worker goroutine.
func rememberGraceMessage(ev notify.Event, msgID int, html string) {
	if ev.Category != "remote" || ev.Kind != "grace_scheduled" || msgID == 0 {
		return
	}
	seq, ok := activeScheduleSeq(ev.Fields["command"], originRemote)
	if !ok {
		return
	}
	graceMsg.mu.Lock()
	graceMsg.cur = graceMessage{seq: seq, chatID: getConfig().Telegram.ChatID, msgID: msgID, html: html}
	graceMsg.mu.Unlock()
}

// takeGraceMessage claims the stored message when it is the one a button
// was pressed on, so the Telegram callback path can edit it itself (with
// by=telegram) and finishGraceMessage will not edit it a second time.
func takeGraceMessage(chatID string, msgID int) (graceMessage, bool) {
	graceMsg.mu.Lock()
	defer graceMsg.mu.Unlock()
	m := graceMsg.cur
	if m.msgID == 0 || m.msgID != msgID || m.chatID != chatID {
		return graceMessage{}, false
	}
	graceMsg.cur = graceMessage{}
	return m, true
}

// finishGraceMessage is called by the schedule code (under scheduleMu)
// whenever the schedule with seq ends. When a message is stored for that
// schedule it is edited in the background — best effort, never blocking
// the caller — and forgotten, so the edit happens at most once. by is the
// cancel/run-now origin (toast, tray, app, webui, api, telegram, timer);
// it is omitted from the line when empty (replaced).
func finishGraceMessage(seq uint64, result graceResult, by string) {
	graceMsg.mu.Lock()
	m := graceMsg.cur
	if m.msgID == 0 || m.seq != seq {
		graceMsg.mu.Unlock()
		return
	}
	graceMsg.cur = graceMessage{}
	graceMsg.mu.Unlock()
	go editGraceMessage(m, tgStampBy(string(result), by))
}

// resetGraceMessage forgets any stored message (tests).
func resetGraceMessage() {
	graceMsg.mu.Lock()
	graceMsg.cur = graceMessage{}
	graceMsg.mu.Unlock()
}

// editGraceMessage appends line to the stored message and removes its
// keyboard. Failures are logged only: the schedule outcome is already
// final and a second notification (grace_cancelled / executed) went out.
func editGraceMessage(m graceMessage, line string) {
	token, err := liveBotToken(getConfig().Telegram)
	if err != nil || token == "" {
		logMsg("Telegram: grace message %d not edited: bot token unavailable (%v)", m.msgID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), graceEditTimeout)
	defer cancel()
	if err := newTelegramClient(token).EditMessageText(ctx, m.chatID, m.msgID, m.html+"\n"+line, nil); err != nil {
		logMsg("Telegram: grace message %d edit failed: %v", m.msgID, err)
		return
	}
	logMsg("Telegram: grace message %d updated (%s)", m.msgID, line)
}
