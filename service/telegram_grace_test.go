package service

import (
	"context"
	"fmt"
	"html"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// Tests for the grace-message lifecycle (#62): the Telegram message that
// announced a remote grace schedule is edited exactly once when the
// schedule ends, whichever path ended it.

// graceCfg is a Telegram-enabled config with the grace period on.
func graceCfg() Config {
	cfg := telegramCfg(true, testBotToken)
	cfg.ShutdownGrace = true
	cfg.GraceSeconds = 300
	return cfg
}

// startGraceSink installs the production wiring (live sink + grace hook)
// for the test and clears the remembered message afterwards.
func startGraceSink(t *testing.T) {
	t.Helper()
	resetGraceMessage()
	startLiveNotifier()
	t.Cleanup(func() {
		cancelSchedule()
		stopNotifier()
		resetGraceMessage()
	})
}

// methodCalls returns the recorded calls of one Bot API method.
func (f *fakeTelegram) methodCalls(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// waitCalls polls until at least n calls of method were recorded.
func (f *fakeTelegram) waitCalls(t *testing.T, method string, n int) []fakeCall {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if calls := f.methodCalls(method); len(calls) >= n {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was called %d time(s), want %d", method, len(f.methodCalls(method)), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// storedGrace returns a copy of the remembered grace message.
func storedGrace() graceMessage {
	graceMsg.mu.Lock()
	defer graceMsg.mu.Unlock()
	return graceMsg.cur
}

// waitStored polls until the message with msgID is remembered: the fake
// server records sendMessage before the client has processed the reply
// and run OnSent.
func waitStored(t *testing.T, msgID int) graceMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := storedGrace(); got.msgID == msgID {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored grace message = %+v, want id %d", storedGrace(), msgID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sendGrace fires a remote /shutdown through the HTTP handler and waits
// for the grace message to reach the fake Bot API; it returns the sent HTML.
func sendGrace(t *testing.T, f *fakeTelegram) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/shutdown", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	newCommandHandler().ServeHTTP(httptest.NewRecorder(), req)
	sent := f.waitCalls(t, "sendMessage", 1)[0]
	text, _ := sent.body["text"].(string)
	if _, has := sent.body["reply_markup"]; !has || !strings.Contains(text, "shutdown") {
		t.Fatalf("grace message = %v", sent.body)
	}
	if got := waitStored(t, 7); got.chatID != "42" || got.html != text {
		t.Fatalf("stored grace message = %+v, want chat 42 / the sent text", got)
	}
	return text
}

var stampTime = regexp.MustCompile(`^\d\d:\d\d$`)

// expectEdit checks the single editMessageText call: same message, original
// text plus one result line "<stamp> · HH:MM[ · by]", keyboard removed.
func expectEdit(t *testing.T, f *fakeTelegram, original, stamp, by string) {
	t.Helper()
	edits := f.waitCalls(t, "editMessageText", 1)
	e := edits[0]
	if e.body["message_id"] != float64(7) || e.body["chat_id"] != "42" || e.token != testBotToken {
		t.Errorf("edit call = %+v", e)
	}
	if _, has := e.body["reply_markup"]; has {
		t.Error("edit must remove the keyboard")
	}
	text, _ := e.body["text"].(string)
	if !strings.HasPrefix(text, original+"\n") {
		t.Fatalf("edit does not keep the original text:\n%s", text)
	}
	line := strings.TrimPrefix(text, original+"\n")
	parts := strings.Split(line, " · ")
	if parts[0] != stamp || len(parts) < 2 || !stampTime.MatchString(parts[1]) {
		t.Errorf("result line = %q, want %q · HH:MM", line, stamp)
	}
	switch {
	case by == "" && len(parts) != 2:
		t.Errorf("result line = %q, want no origin", line)
	case by != "" && (len(parts) != 3 || parts[2] != by):
		t.Errorf("result line = %q, want origin %q", line, by)
	}
}

func TestGraceMessageEditedOnceWhenCancelledFromTray(t *testing.T) {
	initLogger()
	f := useFakeTelegram(t)
	withLiveConfig(t, graceCfg())
	stubTrayLauncher(t, nil)
	shutdown := stubCommand(t, "shutdown")
	startGraceSink(t)

	original := sendGrace(t, f)
	if !cancelScheduleBy("tray") {
		t.Fatal("nothing to cancel")
	}
	expectEdit(t, f, original, "✅ 취소됨", "트레이")
	if storedGrace().msgID != 0 {
		t.Error("message must be forgotten after the edit")
	}
	// remote.grace_cancelled goes out as a new message; no second edit.
	f.waitCalls(t, "sendMessage", 2)
	if n := len(f.methodCalls("editMessageText")); n != 1 {
		t.Errorf("editMessageText called %d times, want 1", n)
	}
	expectNotExecuted(t, shutdown, "shutdown")
	// A later cancel of nothing must not touch Telegram either.
	cancelScheduleBy("api")
	time.Sleep(50 * time.Millisecond)
	if n := len(f.methodCalls("editMessageText")); n != 1 {
		t.Errorf("editMessageText called %d times after a no-op cancel", n)
	}
}

func TestGraceMessageEditedWhenTimerFires(t *testing.T) {
	initLogger()
	f := useFakeTelegram(t)
	withLiveConfig(t, graceCfg())
	stubTrayLauncher(t, nil)
	shutdown := stubCommand(t, "shutdown")
	startGraceSink(t)

	// The command handler's grace is seconds long; arm the timer directly.
	if err := setSchedule("shutdown", 800*time.Millisecond, originRemote); err != nil {
		t.Fatal(err)
	}
	emit("remote", "grace_scheduled", map[string]string{
		"command": "shutdown", "from": "10.0.0.5", "delay": "1 sec", "execute_at": "00:00:00",
	}, graceActions()...)
	sent := f.waitCalls(t, "sendMessage", 1)[0]
	original, _ := sent.body["text"].(string)
	waitStored(t, 7)
	expectExecuted(t, shutdown, "shutdown")
	expectEdit(t, f, original, "▶️ 실행됨", "타이머")
	if getSchedule()["active"] == true {
		t.Error("schedule still active after the timer fired")
	}
}

func TestGraceMessageEditedWhenReplacedAndInEnglish(t *testing.T) {
	initLogger()
	f := useFakeTelegram(t)
	cfg := graceCfg()
	cfg.Telegram.Lang = "en"
	withLiveConfig(t, cfg)
	stubTrayLauncher(t, nil)
	stubCommand(t, "shutdown")
	stubCommand(t, "restart")
	startGraceSink(t)

	original := sendGrace(t, f)
	if err := setSchedule("restart", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	expectEdit(t, f, original, "🔁 Replaced", "")
	// The new (UI) schedule has no grace message; ending it edits nothing.
	f.waitCalls(t, "sendMessage", 2) // schedule.replaced or schedule.created
	cancelScheduleBy("webui")
	time.Sleep(50 * time.Millisecond)
	if n := len(f.methodCalls("editMessageText")); n != 1 {
		t.Errorf("editMessageText called %d times, want 1", n)
	}
}

func TestGraceMessageNotRememberedWhenScheduleAlreadyGone(t *testing.T) {
	initLogger()
	useFakeTelegram(t)
	withLiveConfig(t, graceCfg())
	resetGraceMessage()
	t.Cleanup(resetGraceMessage)

	ev := testGraceEvent()
	rememberGraceMessage(ev, 7, "<b>x</b>")
	if storedGrace().msgID != 0 {
		t.Error("remembered a message for a schedule that does not exist")
	}
	// Other events never register, even with a schedule active.
	stubTrayLauncher(t, nil)
	defer cancelSchedule()
	if err := setSchedule("shutdown", time.Hour, originRemote); err != nil {
		t.Fatal(err)
	}
	other := ev
	other.Kind = "grace_cancelled"
	rememberGraceMessage(other, 8, "x")
	rememberGraceMessage(ev, 0, "x")
	if storedGrace().msgID != 0 {
		t.Errorf("stored = %+v, want nothing", storedGrace())
	}
	rememberGraceMessage(ev, 9, "<b>x</b>")
	if got := storedGrace(); got.msgID != 9 || got.chatID != "42" || got.html != "<b>x</b>" {
		t.Errorf("stored = %+v", got)
	}
	// A UI schedule of the same command is not what the message announced.
	cancelSchedule()
	resetGraceMessage()
	if err := setSchedule("shutdown", time.Hour, originUI); err != nil {
		t.Fatal(err)
	}
	rememberGraceMessage(ev, 10, "x")
	if storedGrace().msgID != 0 {
		t.Error("remembered a remote grace message for a UI schedule")
	}
}

// TestGraceCallbackEditsThroughPollerOnly: pressing [취소]/[바로 실행] on the
// grace message itself is answered by the poller's edit (by=telegram); the
// schedule hook must not edit the same message again.
func TestGraceCallbackEditsThroughPollerOnly(t *testing.T) {
	initLogger()
	f := useFakeTelegram(t)
	withLiveConfig(t, graceCfg())
	stubTrayLauncher(t, nil)
	shutdown := stubCommand(t, "shutdown")
	startGraceSink(t)
	var h telegramControl

	original := sendGrace(t, f)
	edit, toast, err := h.HandleCallback(context.Background(), "42", 7, tgPlain(original), "cancel:")
	if err != nil || toast != "취소됨" {
		t.Fatalf("cancel: toast=%q err=%v", toast, err)
	}
	if !strings.HasPrefix(edit, original+"\n✅ 취소됨 · ") || !strings.HasSuffix(edit, " · 텔레그램") {
		t.Errorf("cancel edit = %q", edit)
	}
	if h.EditKeyboard("cancel:") != nil {
		t.Error("cancel: edit must drop the keyboard")
	}
	f.waitCalls(t, "sendMessage", 2) // remote.grace_cancelled
	if n := len(f.methodCalls("editMessageText")); n != 0 {
		t.Errorf("schedule hook edited the message %d time(s) behind the poller", n)
	}
	expectNotExecuted(t, shutdown, "shutdown")

	// Same for [바로 실행]; the message id differs, so a fresh message.
	f.setReply("sendMessage", `{"ok":true,"result":{"message_id":8,"chat":{"id":42,"type":"private"}}}`)
	req := httptest.NewRequest("GET", "/shutdown", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	newCommandHandler().ServeHTTP(httptest.NewRecorder(), req)
	waitStored(t, 8)
	edit, toast, err = h.HandleCallback(context.Background(), "42", 8, "", "runnow:")
	if err != nil || toast != "실행됨" || !strings.Contains(edit, "\n▶️ 실행됨 · ") || !strings.Contains(edit, "shutdown") {
		t.Errorf("runnow edit=%q toast=%q err=%v", edit, toast, err)
	}
	expectExecuted(t, shutdown, "shutdown")
	f.waitCalls(t, "sendMessage", 4)
	if n := len(f.methodCalls("editMessageText")); n != 0 {
		t.Errorf("schedule hook edited the message %d time(s) behind the poller", n)
	}

	// A button on some other message (an old menu) cancels the schedule too;
	// then the grace message is edited by the hook with by=telegram.
	f.setReply("sendMessage", `{"ok":true,"result":{"message_id":9,"chat":{"id":42,"type":"private"}}}`)
	newCommandHandler().ServeHTTP(httptest.NewRecorder(), req)
	original = waitStored(t, 9).html
	if _, _, err := h.HandleCallback(context.Background(), "42", 99, "", "cancel:"); err != nil {
		t.Fatal(err)
	}
	e := f.waitCalls(t, "editMessageText", 1)[0]
	if e.body["message_id"] != float64(9) {
		t.Errorf("edited message %v, want 9", e.body["message_id"])
	}
	if text, _ := e.body["text"].(string); !strings.HasPrefix(text, original+"\n✅ 취소됨 · ") || !strings.HasSuffix(text, " · 텔레그램") {
		t.Errorf("hook edit = %q", text)
	}
}

func TestTelegramStaleGraceButtonsStripKeyboard(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001})
	stubCommand(t, "shutdown")
	resetGraceMessage()
	var h telegramControl
	msgText := "🔌 원격 명령 유예 예약\n5 min 후 shutdown 실행 예정 — <취소>"

	for _, data := range []string{"cancel:", "runnow:"} {
		edit, toast, err := h.HandleCallback(context.Background(), "42", 7, msgText, data)
		if err != nil || toast != "활성 예약 없음" {
			t.Errorf("%s: toast=%q err=%v", data, toast, err)
		}
		if want := html.EscapeString(msgText) + "\n⏹ 이미 처리됨"; edit != want {
			t.Errorf("%s: edit = %q, want %q", data, edit, want)
		}
		if h.EditKeyboard(data) != nil {
			t.Errorf("%s: stale edit must drop the keyboard", data)
		}
		// No message text (no message): nothing to edit.
		if edit, _, _ := h.HandleCallback(context.Background(), "42", 0, "", data); edit != "" {
			t.Errorf("%s without a message: edit = %q", data, edit)
		}
	}
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "en"}})
	if edit, _, _ := h.HandleCallback(context.Background(), "42", 7, "old", "cancel:"); edit != "old\n⏹ Already handled" {
		t.Errorf("en stale edit = %q", edit)
	}
}

func TestTelegramConfirmKeepsMenuText(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{PCName: "MY<PC>"}})
	shutdown := stubCommand(t, "shutdown")
	stubCommand(t, "lock")
	var h telegramControl

	menu, _, _ := h.HandleCommand(context.Background(), "42", "menu", nil)
	plainMenu := tgPlain(menu)
	if plainMenu != "🖥 MY<PC>\n무엇을 할까요?" {
		t.Fatalf("plain menu = %q", plainMenu)
	}

	// confirm: appends the prompt under the (re-formatted) menu text.
	edit, toast, err := h.HandleCallback(context.Background(), "42", 7, plainMenu, "confirm:shutdown")
	if err != nil || toast != "" {
		t.Fatalf("confirm: toast=%q err=%v", toast, err)
	}
	if want := menu + "\n\n" + tgText("confirm_q", "종료"); edit != want {
		t.Errorf("confirm edit = %q, want %q", edit, want)
	}
	if got := keyboardData(h.EditKeyboard("confirm:shutdown")); fmt.Sprint(got) != "[exec:shutdown dismiss:]" {
		t.Errorf("confirm keyboard = %v", got)
	}
	expectNotExecuted(t, shutdown, "shutdown")

	// exec: keeps menu + prompt and appends the result line.
	prompt := edit
	edit, _, err = h.HandleCallback(context.Background(), "42", 7, tgPlain(prompt), "exec:shutdown")
	if err != nil || !strings.HasPrefix(edit, prompt+"\n✅ 실행됨 · ") || !strings.HasSuffix(edit, " · 텔레그램") {
		t.Errorf("exec edit = %q err=%v", edit, err)
	}
	expectExecuted(t, shutdown, "shutdown")

	// dismiss: same for a /shutdown prompt on its own; unknown text is escaped.
	q := tgText("confirm_q", "종료")
	if edit, _, _ = h.HandleCallback(context.Background(), "42", 7, tgPlain(q), "dismiss:"); !strings.HasPrefix(edit, q+"\n❎ 취소됨 · ") {
		t.Errorf("dismiss edit = %q", edit)
	}
	if edit, _, _ = h.HandleCallback(context.Background(), "42", 7, "a<b", "dismiss:"); !strings.HasPrefix(edit, "a&lt;b\n❎ 취소됨 · ") {
		t.Errorf("dismiss edit of foreign text = %q", edit)
	}

	// cmd: from the menu keeps the menu text; the buttons go.
	if edit, _, _ = h.HandleCallback(context.Background(), "42", 7, plainMenu, "cmd:lock"); !strings.HasPrefix(edit, menu+"\n✅ 실행됨 · ") {
		t.Errorf("cmd edit = %q", edit)
	}

	// cancel:menu leaves the menu usable: no edit without a schedule, menu
	// text + result with one, and the menu keyboard either way.
	if edit, toast, _ := h.HandleCallback(context.Background(), "42", 7, plainMenu, "cancel:menu"); edit != "" || toast != "활성 예약 없음" {
		t.Errorf("cancel:menu without schedule: edit=%q toast=%q", edit, toast)
	}
	stubTrayLauncher(t, nil)
	defer cancelSchedule()
	if err := setSchedule("lock", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	edit, toast, err = h.HandleCallback(context.Background(), "42", 7, plainMenu, "cancel:menu")
	if err != nil || toast != "취소됨" || edit != menu+"\n✅ 취소됨: 잠금" {
		t.Errorf("cancel:menu edit=%q toast=%q err=%v", edit, toast, err)
	}
	if got := keyboardData(h.EditKeyboard("cancel:menu")); fmt.Sprint(got) != fmt.Sprint(keyboardData(tgMenuKeyboard())) {
		t.Errorf("cancel:menu keyboard = %v, want the menu", got)
	}
	if getSchedule()["active"] == true {
		t.Error("cancel:menu did not cancel")
	}
}

func TestTgPlainAndStampBy(t *testing.T) {
	if got := tgPlain("🖥 <b>MY&lt;PC&gt;</b>\n<code>a &amp; b</code>"); got != "🖥 MY<PC>\na & b" {
		t.Errorf("tgPlain = %q", got)
	}
	if got := tgKeep("x<y"); got != "x&lt;y" {
		t.Errorf("tgKeep escapes foreign text: %q", got)
	}
	if got := tgKeep("", "<b>a</b>"); got != "" {
		t.Errorf("tgKeep(\"\") = %q", got)
	}
	setConfig(Config{})
	for by, want := range map[string]string{"toast": "토스트", "tray": "트레이", "app": "앱", "webui": "WebUI", "api": "API", "telegram": "텔레그램", "timer": "타이머", "x<y": "x&lt;y"} {
		if got := tgByLabel(by); got != want {
			t.Errorf("tgByLabel(%q) = %q, want %q", by, got, want)
		}
	}
	line := tgStampBy("stamp_replaced", "")
	if parts := strings.Split(line, " · "); len(parts) != 2 || parts[0] != "🔁 대체됨" || !stampTime.MatchString(parts[1]) {
		t.Errorf("tgStampBy without origin = %q", line)
	}
	setConfig(Config{Telegram: TelegramConfig{Lang: "en"}})
	if line := tgStampBy("stamp_ran", "timer"); !strings.HasPrefix(line, "▶️ Executed · ") || !strings.HasSuffix(line, " · timer") {
		t.Errorf("en tgStampBy = %q", line)
	}
}

// testGraceEvent is the remote.grace_scheduled event the command handler
// emits for /shutdown.
func testGraceEvent() notify.Event {
	return notify.Event{Category: "remote", Kind: "grace_scheduled", Fields: map[string]string{
		"command": "shutdown", "from": "10.0.0.5", "delay": "5 min", "execute_at": "00:05:00",
	}}
}
