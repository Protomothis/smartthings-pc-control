package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// stubCommand replaces Commands[name] with one that reports on the returned
// channel instead of touching the machine.
func stubCommand(t *testing.T, name string) <-chan struct{} {
	t.Helper()
	orig := Commands[name]
	executed := make(chan struct{}, 4)
	Commands[name] = Command{Response: orig.Response, Execute: func() { executed <- struct{}{} }}
	t.Cleanup(func() { Commands[name] = orig })
	return executed
}

func expectExecuted(t *testing.T, executed <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was not executed", what)
	}
}

func expectNotExecuted(t *testing.T, executed <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-executed:
		t.Fatalf("%s was executed", what)
	case <-time.After(100 * time.Millisecond):
	}
}

func keyboardData(kb *telegram.InlineKeyboard) []string {
	if kb == nil {
		return nil
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

// tgBody drops the "🖥 <b>name</b>" header line every reply now opens with
// (#75) so a test can assert on the reply text itself. Replies that put the
// header on the body's first line (/status) are returned unchanged.
func tgBody(h string) string {
	header := tgHeader() + "\n"
	return strings.TrimPrefix(h, header)
}

// Every command reply starts with the PC-name header (#75).
func TestTelegramRepliesCarryPCNameHeader(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko", PCName: "MY<PC>"}})
	stubTrayLauncher(t, nil)
	stubCommand(t, "lock")
	defer cancelSchedule()
	var h telegramControl

	const header = "🖥 <b>MY&lt;PC&gt;</b>"
	for _, cmd := range []string{"help", "status", "menu", "lock", "shutdown", "cancel", "now", "unmute", "frobnicate", telegram.StaleCommand} {
		reply, _, _ := h.HandleCommand(context.Background(), "42", cmd, nil)
		if !strings.HasPrefix(reply, header) {
			t.Errorf("/%s reply lacks the header: %q", cmd, reply)
		}
		// Exactly one header, never a stacked pair.
		if strings.Count(reply, telegram.HeaderIcon) != 1 {
			t.Errorf("/%s reply has %d headers: %q", cmd, strings.Count(reply, telegram.HeaderIcon), reply)
		}
	}
	// Button edits too, whether or not the kept text already had one.
	for _, msgText := range []string{"", "old text", "🖥 MY<PC>\n무엇을 할까요?"} {
		edit, _, _ := h.HandleCallback(context.Background(), "42", 7, msgText, "dismiss:")
		if !strings.HasPrefix(edit, telegram.HeaderIcon) {
			t.Errorf("edit of %q lacks the header: %q", msgText, edit)
		}
		if strings.Count(edit, telegram.HeaderIcon) != 1 {
			t.Errorf("edit of %q has %d headers: %q", msgText, strings.Count(edit, telegram.HeaderIcon), edit)
		}
	}
	// An empty reply stays empty (the poller sends nothing for it).
	if got := tgWithHeader(""); got != "" {
		t.Errorf("empty reply = %q", got)
	}
	// Without telegram.pc_name the hostname is used.
	setConfig(Config{Port: 5001})
	if got := tgPCName(); got != hostname() {
		t.Errorf("tgPCName = %q, want the hostname %q", got, hostname())
	}
}

func TestTelegramStatusShowsVersionScheduleAndLastRemote(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko", PCName: "MY<PC>"}})
	stubTrayLauncher(t, nil)
	defer cancelSchedule()
	origVersion := Version
	Version = "v9.9.9-test"
	defer func() { Version = origVersion }()
	// Other tests drive newCommandHandler, which records the last remote command.
	lastRemoteMu.Lock()
	lastRemote = remoteRecord{}
	lastRemoteMu.Unlock()

	var h telegramControl
	html, kb, err := h.HandleCommand(context.Background(), "42", "status", nil)
	if err != nil || kb != nil {
		t.Fatalf("status: err=%v kb=%v", err, kb)
	}
	for _, want := range []string{"v9.9.9-test", "MY&lt;PC&gt;", "가동:", "예약: 없음", "마지막 원격 명령: 없음"} {
		if !strings.Contains(html, want) {
			t.Errorf("status lacks %q:\n%s", want, html)
		}
	}

	if err := setSchedule("lock", 30*time.Minute, originTelegram); err != nil {
		t.Fatal(err)
	}
	noteRemoteCommand("shutdown", "10.0.0.5")
	html, _, _ = h.HandleCommand(context.Background(), "42", "status", nil)
	for _, want := range []string{"예약: 잠금 · 텔레그램 · ", "s 남음 (", "마지막 원격 명령: 종료 · <code>10.0.0.5</code>"} {
		if !strings.Contains(html, want) {
			t.Errorf("status lacks %q:\n%s", want, html)
		}
	}

	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "en"}})
	html, _, _ = h.HandleCommand(context.Background(), "42", "status", nil)
	if !strings.Contains(html, "Schedule: Lock · Telegram") || !strings.Contains(html, "Last remote command: Shut down") {
		t.Errorf("en status:\n%s", html)
	}
}

func TestTelegramHelpMenuAndUnknown(t *testing.T) {
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	var h telegramControl
	help, _, _ := h.HandleCommand(context.Background(), "42", "help", nil)
	if !strings.Contains(help, "/shutdown") || !strings.Contains(help, "/mute") {
		t.Errorf("help = %q", help)
	}
	unknown, _, _ := h.HandleCommand(context.Background(), "42", "frobnicate<", nil)
	if !strings.Contains(unknown, "<code>/frobnicate&lt;</code>") || !strings.Contains(unknown, "/status") {
		t.Errorf("unknown = %q", unknown)
	}
	_, kb, _ := h.HandleCommand(context.Background(), "42", "menu", nil)
	want := []string{"cmd:lock", "cmd:turnscreenoff", "confirm:suspend", "confirm:restart", "confirm:shutdown", "cancel:menu"}
	if got := keyboardData(kb); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("menu buttons = %v, want %v", got, want)
	}
}

func TestTelegramLockAndScreenOffRunImmediately(t *testing.T) {
	initLogger()
	setConfig(Config{})
	lock := stubCommand(t, "lock")
	screen := stubCommand(t, "turnscreenoff")
	var h telegramControl
	if html, _, err := h.HandleCommand(context.Background(), "42", "lock", nil); err != nil || !strings.Contains(html, "잠금") {
		t.Errorf("lock reply = %q err=%v", html, err)
	}
	expectExecuted(t, lock, "lock")
	if _, _, err := h.HandleCommand(context.Background(), "42", "screenoff", nil); err != nil {
		t.Error(err)
	}
	expectExecuted(t, screen, "turnscreenoff")

	// /screenon mirrors /screenoff (#67).
	screenOn := stubCommand(t, "turnscreenon")
	if html, _, err := h.HandleCommand(context.Background(), "42", "screenon", nil); err != nil || !strings.Contains(html, "화면 켜기") {
		t.Errorf("screenon reply = %q err=%v", html, err)
	}
	expectExecuted(t, screenOn, "turnscreenon")
}

func TestTelegramShutdownWithoutMinutesAsksConfirmation(t *testing.T) {
	initLogger()
	setConfig(Config{})
	shutdown := stubCommand(t, "shutdown")
	defer cancelSchedule()
	var h telegramControl
	html, kb, err := h.HandleCommand(context.Background(), "42", "shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := keyboardData(kb); fmt.Sprint(got) != "[exec:shutdown dismiss:]" {
		t.Errorf("confirm keyboard = %v", got)
	}
	if !strings.Contains(html, "종료") {
		t.Errorf("confirm text = %q", html)
	}
	expectNotExecuted(t, shutdown, "shutdown")
	if getSchedule()["active"] == true {
		t.Error("confirmation prompt must not schedule anything")
	}
	// /sleep maps to the suspend command.
	_, kb, _ = h.HandleCommand(context.Background(), "42", "sleep", nil)
	if got := keyboardData(kb); fmt.Sprint(got) != "[exec:suspend dismiss:]" {
		t.Errorf("sleep keyboard = %v", got)
	}
}

func TestTelegramShutdownWithMinutesSchedulesAsTelegram(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001})
	launches := stubTrayLauncher(t, nil)
	shutdown := stubCommand(t, "shutdown")
	defer cancelSchedule()
	var h telegramControl

	html, kb, err := h.HandleCommand(context.Background(), "42", "shutdown", []string{"30"})
	if err != nil || kb != nil {
		t.Fatalf("shutdown 30: err=%v kb=%v", err, kb)
	}
	if !strings.Contains(html, "30분") {
		t.Errorf("reply = %q", html)
	}
	s := getSchedule()
	if s["active"] != true || s["command"] != "shutdown" || s["origin"] != "telegram" {
		t.Fatalf("schedule = %v", s)
	}
	if rem, _ := s["remainingSec"].(int); rem < 1795 || rem > 1800 {
		t.Errorf("remainingSec = %v, want ~1800", rem)
	}
	expectNotExecuted(t, shutdown, "shutdown")
	expectNoTrayLaunch(t, launches)

	for _, bad := range []string{"0", "abc", "-5", "99999"} {
		if _, _, err := h.HandleCommand(context.Background(), "42", "restart", []string{bad}); err == nil {
			t.Errorf("restart %q accepted", bad)
		}
	}
	if getSchedule()["command"] != "shutdown" {
		t.Error("invalid minutes must not touch the schedule")
	}
}

func TestTelegramCancelAndNow(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Notify: notify.Config{"schedule": {"cancelled": true}}})
	stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	lock := stubCommand(t, "lock")
	defer cancelSchedule()
	var h telegramControl

	html, _, _ := h.HandleCommand(context.Background(), "42", "cancel", nil)
	if tgBody(html) != "활성 예약 없음" {
		t.Errorf("cancel without schedule = %q", html)
	}
	if err := setSchedule("lock", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	html, _, _ = h.HandleCommand(context.Background(), "42", "cancel", nil)
	if !strings.Contains(html, "취소됨") || getSchedule()["active"] == true {
		t.Errorf("cancel = %q, schedule = %v", html, getSchedule())
	}
	ev := expectNotification(t, events, "schedule.cancelled")
	if ev.Fields["by"] != "telegram" {
		t.Errorf("cancelled by %q, want telegram", ev.Fields["by"])
	}

	if err := setSchedule("lock", 30*time.Minute, originUI); err != nil {
		t.Fatal(err)
	}
	html, _, _ = h.HandleCommand(context.Background(), "42", "now", nil)
	if !strings.Contains(html, "지금 실행") {
		t.Errorf("now = %q", html)
	}
	expectExecuted(t, lock, "lock")
	if getSchedule()["active"] == true {
		t.Error("schedule still active after /now")
	}
	if html, _, _ = h.HandleCommand(context.Background(), "42", "now", nil); tgBody(html) != "활성 예약 없음" {
		t.Errorf("now without schedule = %q", html)
	}
}

func TestTelegramMuteUnmute(t *testing.T) {
	initLogger()
	setConfig(Config{})
	var h telegramControl

	stopNotifier()
	if html, _, _ := h.HandleCommand(context.Background(), "42", "mute", []string{"30m"}); !strings.Contains(html, "꺼져") {
		t.Errorf("mute without bus = %q", html)
	}
	captureNotifications(t)
	if _, _, err := h.HandleCommand(context.Background(), "42", "mute", nil); err != nil {
		t.Errorf("mute usage should not be an error: %v", err)
	}
	if _, _, err := h.HandleCommand(context.Background(), "42", "mute", []string{"soon"}); err == nil {
		t.Error("mute soon accepted")
	}
	if html, _, err := h.HandleCommand(context.Background(), "42", "mute", []string{"2h"}); err != nil || !strings.HasPrefix(tgBody(html), "🔕") {
		t.Errorf("mute 2h = %q err=%v", html, err)
	}
	until := currentBus().MutedUntil()
	if d := time.Until(until); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("muted until %s (in %s), want ~2h", until, d)
	}
	if html, _, _ := h.HandleCommand(context.Background(), "42", "status", nil); !strings.Contains(html, "알림 일시 중지") {
		t.Errorf("status should show the mute:\n%s", html)
	}
	if html, _, _ := h.HandleCommand(context.Background(), "42", "unmute", nil); !strings.HasPrefix(tgBody(html), "🔔") {
		t.Errorf("unmute = %q", html)
	}
	if !currentBus().MutedUntil().IsZero() {
		t.Error("still muted after /unmute")
	}
}

func TestParseMuteDuration(t *testing.T) {
	cases := map[string]time.Duration{"30m": 30 * time.Minute, "2h": 2 * time.Hour, "1h30m": 90 * time.Minute, "45": 45 * time.Minute}
	for in, want := range cases {
		if got, ok := parseMuteDuration(in); !ok || got != want {
			t.Errorf("parseMuteDuration(%q) = %s,%v want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "x", "10s", "0", "-1h", "200h"} {
		if _, ok := parseMuteDuration(in); ok {
			t.Errorf("parseMuteDuration(%q) accepted", in)
		}
	}
}

func TestTelegramCallbackExecAndDismiss(t *testing.T) {
	initLogger()
	setConfig(Config{})
	shutdown := stubCommand(t, "shutdown")
	var h telegramControl

	edit, toast, err := h.HandleCallback(context.Background(), "42", 7, "", "exec:shutdown")
	if err != nil {
		t.Fatal(err)
	}
	expectExecuted(t, shutdown, "shutdown")
	if !strings.Contains(edit, "✅ 실행됨 · ") || !strings.HasSuffix(edit, " · 텔레그램") || toast != "실행됨" {
		t.Errorf("exec edit=%q toast=%q", edit, toast)
	}
	if h.EditKeyboard("exec:shutdown") != nil {
		t.Error("exec edit must drop the keyboard")
	}

	edit, toast, err = h.HandleCallback(context.Background(), "42", 7, "", "dismiss:")
	if err != nil || !strings.Contains(edit, "취소됨") || toast != "" {
		t.Errorf("dismiss edit=%q toast=%q err=%v", edit, toast, err)
	}

	// Only registry commands the bot may run are accepted.
	if _, _, err := h.HandleCallback(context.Background(), "42", 7, "", "exec:forceshutdown"); err == nil {
		t.Error("exec:forceshutdown accepted")
	}
	if _, _, err := h.HandleCallback(context.Background(), "42", 7, "", "bogus:"); err == nil {
		t.Error("unknown verb accepted")
	}

	setConfig(Config{Telegram: TelegramConfig{Lang: "en"}})
	edit, _, _ = h.HandleCallback(context.Background(), "42", 7, "", "exec:shutdown")
	expectExecuted(t, shutdown, "shutdown")
	if !strings.HasPrefix(tgBody(edit), "Shut down\n✅ Executed · ") || !strings.HasSuffix(edit, " · Telegram") {
		t.Errorf("en exec edit = %q", edit)
	}
}

func TestTelegramCallbackMenuButtons(t *testing.T) {
	initLogger()
	setConfig(Config{})
	lock := stubCommand(t, "lock")
	shutdown := stubCommand(t, "shutdown")
	var h telegramControl

	// cmd: runs safe commands only.
	if edit, _, err := h.HandleCallback(context.Background(), "42", 7, "", "cmd:lock"); err != nil || !strings.Contains(edit, "실행됨") {
		t.Errorf("cmd:lock edit=%q err=%v", edit, err)
	}
	expectExecuted(t, lock, "lock")
	edit, toast, err := h.HandleCallback(context.Background(), "42", 7, "", "cmd:shutdown")
	if err != nil || edit != "" || toast != "확인이 필요한 명령입니다" {
		t.Errorf("cmd:shutdown edit=%q toast=%q err=%v", edit, toast, err)
	}
	expectNotExecuted(t, shutdown, "shutdown")

	// confirm: turns the menu into a prompt that keeps [확인][취소].
	edit, toast, err = h.HandleCallback(context.Background(), "42", 7, "", "confirm:shutdown")
	if err != nil || !strings.Contains(edit, "종료") || toast != "" {
		t.Errorf("confirm edit=%q toast=%q err=%v", edit, toast, err)
	}
	if got := keyboardData(h.EditKeyboard("confirm:shutdown")); fmt.Sprint(got) != "[exec:shutdown dismiss:]" {
		t.Errorf("confirm keyboard = %v", got)
	}
	if _, _, err := h.HandleCallback(context.Background(), "42", 7, "", "confirm:lock"); err == nil {
		t.Error("confirm:lock accepted (lock needs no confirmation)")
	}
	if h.EditKeyboard("confirm:lock") != nil || h.EditKeyboard("cancel:") != nil {
		t.Error("only confirm:<power command> keeps a keyboard")
	}
}

func TestTelegramCallbackCancelAndRunnowOnGrace(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 300})
	stubTrayLauncher(t, nil)
	events := captureNotifications(t)
	shutdown := stubCommand(t, "shutdown")
	defer cancelSchedule()
	var h telegramControl

	edit, toast, err := h.HandleCallback(context.Background(), "42", 7, "", "cancel:")
	if err != nil || edit != "" || toast != "활성 예약 없음" {
		t.Errorf("cancel without schedule: edit=%q toast=%q err=%v", edit, toast, err)
	}
	edit, toast, err = h.HandleCallback(context.Background(), "42", 7, "", "runnow:")
	if err != nil || edit != "" || toast != "활성 예약 없음" {
		t.Errorf("runnow without schedule: edit=%q toast=%q err=%v", edit, toast, err)
	}
	expectNotExecuted(t, shutdown, "shutdown")

	// A remote grace deferral is what the runnow:/cancel: buttons belong to.
	req := httptest.NewRequest("GET", "/shutdown", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	newCommandHandler().ServeHTTP(httptest.NewRecorder(), req)
	expectNotification(t, events, "remote.grace_scheduled")

	edit, toast, err = h.HandleCallback(context.Background(), "42", 7, "", "cancel:")
	if err != nil || !strings.Contains(edit, "✅ 취소됨 · ") || toast != "취소됨" {
		t.Errorf("cancel: edit=%q toast=%q err=%v", edit, toast, err)
	}
	ev := expectNotification(t, events, "remote.grace_cancelled")
	if ev.Fields["by"] != "telegram" || ev.Fields["command"] != "shutdown" {
		t.Errorf("grace_cancelled fields = %v", ev.Fields)
	}
	expectNotExecuted(t, shutdown, "shutdown")

	newCommandHandler().ServeHTTP(httptest.NewRecorder(), req)
	expectNotification(t, events, "remote.grace_scheduled")
	edit, toast, err = h.HandleCallback(context.Background(), "42", 8, "", "runnow:")
	if err != nil || !strings.Contains(edit, "▶️ 실행됨 · ") || toast != "실행됨" {
		t.Errorf("runnow: edit=%q toast=%q err=%v", edit, toast, err)
	}
	expectNotification(t, events, "remote.grace_cancelled")
	expectExecuted(t, shutdown, "shutdown")
	if getSchedule()["active"] == true {
		t.Error("schedule still active after runnow:")
	}
}

func TestTelegramUnauthorizedEmitsUnknownChat(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001})
	events := captureNotifications(t)
	telegramControl{}.Unauthorized("99", "@mallory", strings.Repeat("/shutdown ", 20))
	ev := expectNotification(t, events, "security.unknown_chat")
	if ev.Fields["chat_id"] != "99" || ev.Fields["username"] != "@mallory" {
		t.Errorf("unknown_chat fields = %v", ev.Fields)
	}
	if !strings.HasSuffix(ev.Fields["text"], "…") || len([]rune(ev.Fields["text"])) != 65 {
		t.Errorf("text not truncated: %q", ev.Fields["text"])
	}
}

func TestTelegramAllowedChatIDsFallsBackToChatID(t *testing.T) {
	setConfig(Config{Telegram: TelegramConfig{ChatID: "42"}})
	if got := telegramAllowedChatIDs(); fmt.Sprint(got) != "[42]" {
		t.Errorf("allowed = %v", got)
	}
	setConfig(Config{Telegram: TelegramConfig{ChatID: "42", AllowedChatIDs: []string{" 7 ", "", "8"}}})
	if got := telegramAllowedChatIDs(); fmt.Sprint(got) != "[7 8]" {
		t.Errorf("allowed = %v", got)
	}
	setConfig(Config{Telegram: TelegramConfig{AllowedChatIDs: []string{" "}}})
	if got := telegramAllowedChatIDs(); len(got) != 0 {
		t.Errorf("allowed = %v, want none", got)
	}
}

func TestTelegramBotCommandsFollowLang(t *testing.T) {
	ko := telegramBotCommands("ko")
	en := telegramBotCommands("en")
	if len(ko) != 14 || len(en) != len(ko) {
		t.Fatalf("command count ko=%d en=%d", len(ko), len(en))
	}
	if ko[0].Command != "status" || ko[0].Description != "상태" || en[0].Description != "Status" {
		t.Errorf("status = %+v / %+v", ko[0], en[0])
	}
	if telegramBotCommands("")[0].Description != "상태" {
		t.Error("unknown lang should fall back to ko")
	}
}

func TestTelegramControlKey(t *testing.T) {
	on := TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "tok", ChatID: "42", Lang: "ko"}
	if telegramControlKey(on) == "" {
		t.Error("fully configured control should be on")
	}
	for name, cfg := range map[string]TelegramConfig{
		"disabled":   {ControlEnabled: true, BotToken: "tok", ChatID: "42"},
		"no control": {Enabled: true, BotToken: "tok", ChatID: "42"},
		"no token":   {Enabled: true, ControlEnabled: true, ChatID: "42"},
		"no chat":    {Enabled: true, ControlEnabled: true, BotToken: "tok"},
	} {
		if telegramControlKey(cfg) != "" {
			t.Errorf("%s: control should be off", name)
		}
	}
	changed := on
	changed.Lang = "en"
	if telegramControlKey(changed) == telegramControlKey(on) {
		t.Error("lang change must restart the poller (setMyCommands descriptions)")
	}
}

func TestFormatUptime(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                "0m 00s",
		4*time.Minute + 9*time.Second:    "4m 09s",
		time.Hour + 5*time.Minute:        "1h 05m",
		26*time.Hour + 30*time.Minute:    "1d 02h",
		29*time.Minute + 59*time.Second:  "29m 59s",
		-time.Second:                     "0m 00s",
		3*24*time.Hour + 4*time.Hour + 1: "3d 04h",
	}
	for d, want := range cases {
		if got := formatUptime(d); got != want {
			t.Errorf("formatUptime(%s) = %q, want %q", d, got, want)
		}
	}
}

// TestTelegramControlLifecycle drives start/reconcile/stop against a fake
// Bot API: the poller registers the command menu, polls, and goes away
// when control is switched off in the config.
func TestTelegramControlLifecycle(t *testing.T) {
	initLogger()
	calls := make(chan string, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		calls <- r.URL.Path + " " + fmt.Sprint(body["offset"])
		w.Header().Set("Content-Type", "application/json")
		if method == "getUpdates" {
			// Like a real long poll: nothing to report until the client hangs up.
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer srv.Close()
	origBase := telegramBaseURL
	telegramBaseURL = srv.URL
	t.Cleanup(func() {
		stopTelegramControl()
		telegramBaseURL = origBase
	})

	next := func() string {
		select {
		case c := <-calls:
			return c
		case <-time.After(3 * time.Second):
			t.Fatal("no Bot API call arrived")
			return ""
		}
	}

	// Off: nothing starts, and saveConfig's reconcile is a no-op until managed.
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, BotToken: "plain-token", ChatID: "42"}})
	reconcileTelegramControl()
	if telegramControlRunning() {
		t.Fatal("poller running before startTelegramControl")
	}
	startTelegramControl()
	if telegramControlRunning() {
		t.Fatal("poller running with control_enabled=false")
	}

	// On: setMyCommands once, then the drain poll (offset -1).
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "plain-token", ChatID: "42", Lang: "ko"}})
	reconcileTelegramControl()
	if !telegramControlRunning() {
		t.Fatal("poller not running after enabling control")
	}
	if got := next(); got != "/botplain-token/setMyCommands <nil>" {
		t.Errorf("first call = %q, want setMyCommands with the plaintext token", got)
	}
	if got := next(); got != "/botplain-token/getUpdates -1" {
		t.Errorf("second call = %q, want the drain poll", got)
	}

	// Same settings: reconcile leaves the running poller alone.
	reconcileTelegramControl()
	select {
	case c := <-calls:
		t.Errorf("unexpected call after no-op reconcile: %s", c)
	case <-time.After(100 * time.Millisecond):
	}

	// Token change: restart with the new token.
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "other-token", ChatID: "42", Lang: "ko"}})
	reconcileTelegramControl()
	if got := next(); got != "/botother-token/setMyCommands <nil>" {
		t.Errorf("after token change = %q", got)
	}
	next() // its drain poll

	// Off again: the poller goroutine exits.
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, BotToken: "other-token", ChatID: "42"}})
	reconcileTelegramControl()
	if telegramControlRunning() {
		t.Fatal("poller still running after control_enabled=false")
	}
	stopTelegramControl()
}
