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
	header := tgCtl.Header() + "\n"
	return strings.TrimPrefix(h, header)
}

// Every command reply starts with the PC-name header (#75).
func TestTelegramRepliesCarryPCNameHeader(t *testing.T) {
	initLogger()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko", PCName: "MY<PC>"}})
	stubTrayLauncher(t, nil)
	stubCommand(t, "lock")
	stubMediaRun(t, UserActionResult{}, errNoUserSession) // /unmute never reaches a real session
	defer cancelScheduleBy("api")
	h := tgCtl

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
	if got := tgCtl.WithHeader(""); got != "" {
		t.Errorf("empty reply = %q", got)
	}
	// Without telegram.pc_name the hostname is used.
	setConfig(Config{Port: 5001})
	if got := tgCtl.PCName(); got != hostname() {
		t.Errorf("tgPCName = %q, want the hostname %q", got, hostname())
	}
}

// TestTelegramStatusLines: the /status reply has one line per piece of
// state, and a line only while there is something to say (no battery line
// on a desktop, no activity line with the option off, no media line with
// nothing playing). The line texts themselves are tgcontrol's
// (texts_test.go); this checks the reply is built from the live stores.
func TestTelegramStatusLines(t *testing.T) {
	initLogger()
	stubTrayLauncher(t, nil)
	onBattery := func(raw systemPowerStatus) func(*testing.T, *Config) {
		return func(t *testing.T, _ *Config) {
			f := &fakeBattery{readings: []systemPowerStatus{raw}}
			m := f.monitor()
			m.Poll()
			stubBattery(t, m)
		}
	}
	for _, tc := range []struct {
		name  string
		lang  string
		setup func(t *testing.T, cfg *Config)
		want  []string
		not   []string
	}{
		{"idle", "ko", nil,
			[]string{"v9.9.9-test", "MY&lt;PC&gt;", "가동:", "예약: 없음", "마지막 원격 명령: 없음"},
			[]string{"배터리", "활동", "미디어:"}},
		{"a schedule and a remote command", "ko", func(t *testing.T, _ *Config) {
			if err := setSchedule("lock", 30*time.Minute, originTelegram); err != nil {
				t.Fatal(err)
			}
			noteRemoteCommandBy("shutdown", "10.0.0.5", "remote")
		}, []string{"예약: 잠금 · 텔레그램 · ", "s 남음 (", "마지막 원격 명령: 종료 · <code>10.0.0.5</code>"}, nil},
		{"in English", "en", func(t *testing.T, _ *Config) {
			if err := setSchedule("lock", 30*time.Minute, originTelegram); err != nil {
				t.Fatal(err)
			}
			noteRemoteCommandBy("shutdown", "10.0.0.5", "remote")
		}, []string{"Schedule: Lock · Telegram", "Last remote command: Shut down"}, nil},
		{"laptop charging", "ko", onBattery(systemPowerStatus{ACLineStatus: 1, BatteryFlag: 1 | 8, BatteryLifePercent: 80}), []string{"배터리: 80% · 충전 중"}, nil},
		{"laptop full on AC", "ko", onBattery(systemPowerStatus{ACLineStatus: 1, BatteryFlag: 1, BatteryLifePercent: 100}), []string{"배터리: 100% · 전원 연결됨"}, nil},
		{"laptop on battery", "ko", onBattery(systemPowerStatus{ACLineStatus: 0, BatteryFlag: 2, BatteryLifePercent: 15}), []string{"배터리: 15%"}, []string{"충전", "전원"}},
		{"laptop, level unknown", "ko", onBattery(systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 255}), []string{"배터리: 잔량 알 수 없음"}, nil},
		{"laptop in English", "en", onBattery(systemPowerStatus{ACLineStatus: 1, BatteryFlag: 8, BatteryLifePercent: 42}), []string{"Battery: 42% · charging"}, nil},
		{"apps running", "ko", func(t *testing.T, cfg *Config) {
			stubProcesses(t, "steam.exe", "obs64.exe")
			cfg.Activity = ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(2, "obs64.exe", "OBS"), watch(3, "code.exe", "VS Code")}}
			activityScan.Scan(cfg.Activity)
		}, []string{"\n활동: Steam 실행 중 · 외 1개"}, nil},
		{"apps running, option off", "ko", func(t *testing.T, cfg *Config) {
			stubProcesses(t, "steam.exe")
			activityScan.Scan(ActivityConfig{Enabled: true, Watch: []ActivityWatch{watch(1, "steam.exe", "Steam")}})
		}, nil, []string{"활동"}},
		{"playing", "ko", func(t *testing.T, cfg *Config) {
			cfg.Media = MediaConfig{Enabled: true, NowPlaying: true}
			setConfig(*cfg)
			recordMediaSample(spotifyTrack, nowPlayingSetup(t, *cfg))
		}, []string{"\n미디어: ▶ Hype Boy — NewJeans · Spotify"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Port: 5001, Telegram: TelegramConfig{Lang: tc.lang, PCName: "MY<PC>"}}
			setConfig(cfg)
			t.Cleanup(func() { setConfig(Config{Port: 5001}); cancelScheduleBy("api") })
			origVersion := Version
			Version = "v9.9.9-test"
			t.Cleanup(func() { Version = origVersion })
			lastRemoteMu.Lock()
			lastRemote = remoteRecord{}
			lastRemoteMu.Unlock()
			stubAwake(t)
			onBattery(systemPowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255})(t, &cfg)
			if tc.setup != nil {
				tc.setup(t, &cfg)
				setConfig(cfg)
			}

			html, kb, err := tgCtl.HandleCommand(context.Background(), "42", "status", nil)
			if err != nil || kb != nil {
				t.Fatalf("status: err=%v kb=%v", err, kb)
			}
			for _, want := range tc.want {
				if !strings.Contains(html, want) {
					t.Errorf("/status lacks %q:\n%s", want, html)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(html, not) {
					t.Errorf("/status has %q:\n%s", not, html)
				}
			}
		})
	}
}

func TestTelegramHelpMenuAndUnknown(t *testing.T) {
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	h := tgCtl
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
	h := tgCtl
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
	defer cancelScheduleBy("api")
	h := tgCtl
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
	defer cancelScheduleBy("api")
	h := tgCtl

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
	defer cancelScheduleBy("api")
	h := tgCtl

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
	h := tgCtl

	stopNotifier()
	if html, _, _ := h.HandleCommand(context.Background(), "42", "mute", []string{"30m"}); !strings.Contains(html, "꺼져") {
		t.Errorf("mute without bus = %q", html)
	}
	captureNotifications(t)
	if _, _, err := h.HandleCommand(context.Background(), "42", "quiet", nil); err != nil {
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
	if html, _, _ := h.HandleCommand(context.Background(), "42", "quiet", []string{"off"}); !strings.HasPrefix(tgBody(html), "🔔") {
		t.Errorf("unmute = %q", html)
	}
	if !currentBus().MutedUntil().IsZero() {
		t.Error("still muted after /quiet off")
	}
}

func TestTelegramCallbackExecAndDismiss(t *testing.T) {
	initLogger()
	setConfig(Config{})
	shutdown := stubCommand(t, "shutdown")
	h := tgCtl

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
	h := tgCtl

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
	defer cancelScheduleBy("api")
	h := tgCtl

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
	tgCtl.Unauthorized("99", "@mallory", strings.Repeat("/shutdown ", 20))
	ev := expectNotification(t, events, "security.unknown_chat")
	if ev.Fields["chat_id"] != "99" || ev.Fields["username"] != "@mallory" {
		t.Errorf("unknown_chat fields = %v", ev.Fields)
	}
	if !strings.HasSuffix(ev.Fields["text"], "…") || len([]rune(ev.Fields["text"])) != 65 {
		t.Errorf("text not truncated: %q", ev.Fields["text"])
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
		tgCtl.Stop()
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
	tgCtl.Reconcile()
	if tgCtl.Running() {
		t.Fatal("poller running before startTelegramControl")
	}
	tgCtl.Start()
	if tgCtl.Running() {
		t.Fatal("poller running with control_enabled=false")
	}

	// On: setMyCommands once, then the drain poll (offset -1).
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "plain-token", ChatID: "42", Lang: "ko"}})
	tgCtl.Reconcile()
	if !tgCtl.Running() {
		t.Fatal("poller not running after enabling control")
	}
	if got := next(); got != "/botplain-token/setMyCommands <nil>" {
		t.Errorf("first call = %q, want setMyCommands with the plaintext token", got)
	}
	if got := next(); got != "/botplain-token/getUpdates -1" {
		t.Errorf("second call = %q, want the drain poll", got)
	}

	// Same settings: reconcile leaves the running poller alone.
	tgCtl.Reconcile()
	select {
	case c := <-calls:
		t.Errorf("unexpected call after no-op reconcile: %s", c)
	case <-time.After(100 * time.Millisecond):
	}

	// Token change: restart with the new token.
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "other-token", ChatID: "42", Lang: "ko"}})
	tgCtl.Reconcile()
	if got := next(); got != "/botother-token/setMyCommands <nil>" {
		t.Errorf("after token change = %q", got)
	}
	next() // its drain poll

	// Off again: the poller goroutine exits.
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Enabled: true, BotToken: "other-token", ChatID: "42"}})
	tgCtl.Reconcile()
	if tgCtl.Running() {
		t.Fatal("poller still running after control_enabled=false")
	}
	tgCtl.Stop()
}
