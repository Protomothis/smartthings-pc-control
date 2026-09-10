package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// This file is the service side of inbound Telegram control (design doc §9):
// telegramControl implements telegram.CommandHandler, and the lifecycle
// functions at the bottom run the telegram.Poller while control is enabled.
// Issue #62 extends the keyboards and message edits; keep the helpers here
// small and named so it can.

// serviceStartedAt approximates process start for the /status uptime line.
var serviceStartedAt = time.Now()

// remoteRecord is the last SmartThings command, for /status.
type remoteRecord struct {
	Command string
	From    string
	At      time.Time
}

var (
	lastRemote   remoteRecord
	lastRemoteMu sync.Mutex
)

// noteRemoteCommand is called by the command handler for every accepted
// non-ping command.
func noteRemoteCommand(command, from string) {
	lastRemoteMu.Lock()
	lastRemote = remoteRecord{Command: command, From: from, At: time.Now()}
	lastRemoteMu.Unlock()
}

func getLastRemote() remoteRecord {
	lastRemoteMu.Lock()
	defer lastRemoteMu.Unlock()
	return lastRemote
}

// telegramAliases maps slash-command names to Commands keys.
var telegramAliases = map[string]string{
	"lock":      "lock",
	"screenoff": "turnscreenoff",
	"sleep":     "suspend",
	"hibernate": "hibernate",
	"restart":   "restart",
	"shutdown":  "shutdown",
}

// telegramSafeCommands run from a button without confirmation.
var telegramSafeCommands = map[string]bool{"lock": true, "turnscreenoff": true}

// telegramCommandNames are the Commands keys the bot may run at all; the
// power commands need a confirmation (or a delay), see graceCommands.
func telegramCommandName(arg string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(arg))
	if alias, ok := telegramAliases[name]; ok {
		name = alias
	}
	if !telegramSafeCommands[name] && !graceCommands[name] {
		return "", false
	}
	if _, ok := Commands[name]; !ok {
		return "", false
	}
	return name, true
}

// ---- texts ----------------------------------------------------------------

// tgTexts holds every reply string, ko then en. Values are Telegram HTML;
// %-verbs are filled by tgText. Dynamic values are escaped by the callers.
var tgTexts = map[string][2]string{
	"help": {
		"<b>명령</b>\n" +
			"/status – 상태\n" +
			"/menu – 버튼 메뉴\n" +
			"/lock – 잠금\n" +
			"/screenoff – 화면 끄기\n" +
			"/sleep /hibernate /restart /shutdown [분] – 확인 후 즉시 실행, 분을 주면 예약\n" +
			"/cancel – 예약·유예 취소\n" +
			"/now – 예약·유예 즉시 실행\n" +
			"/mute 30m|2h – 알림 일시 중지\n" +
			"/unmute – 알림 재개\n" +
			"/help – 이 목록",
		"<b>Commands</b>\n" +
			"/status – status\n" +
			"/menu – button menu\n" +
			"/lock – lock\n" +
			"/screenoff – screen off\n" +
			"/sleep /hibernate /restart /shutdown [minutes] – confirm then run now, or schedule with minutes\n" +
			"/cancel – cancel the schedule/grace period\n" +
			"/now – run the schedule/grace command now\n" +
			"/mute 30m|2h – pause notifications\n" +
			"/unmute – resume notifications\n" +
			"/help – this list",
	},
	"unknown_command": {"알 수 없는 명령: <code>%s</code>", "Unknown command: <code>%s</code>"},
	"menu_title":      {"🖥 <b>%s</b>\n무엇을 할까요?", "🖥 <b>%s</b>\nWhat should I do?"},
	"executed":        {"✅ %s 실행", "✅ %s executed"},
	"confirm_q":       {"⚠️ <b>%s</b> – 지금 바로 실행할까요?", "⚠️ <b>%s</b> – run it right now?"},
	"bad_minutes":     {"분은 1~1440 사이의 숫자여야 합니다. 예: <code>/shutdown 30</code>", "Minutes must be a number from 1 to 1440, e.g. <code>/shutdown 30</code>"},
	"scheduled":       {"⏱ <b>%s</b> %d분 후 예약됨 (%s)", "⏱ <b>%s</b> scheduled in %d min (%s)"},
	"schedule_failed": {"예약 실패: %s", "Scheduling failed: %s"},
	"cancelled":       {"✅ 취소됨: %s", "✅ Cancelled: %s"},
	"no_schedule":     {"활성 예약 없음", "No active schedule"},
	"run_now":         {"✅ 지금 실행: %s", "✅ Running now: %s"},
	"mute_usage":      {"사용법: <code>/mute 30m</code> 또는 <code>/mute 2h</code>", "Usage: <code>/mute 30m</code> or <code>/mute 2h</code>"},
	"muted":           {"🔕 %s까지 알림 일시 중지", "🔕 Notifications paused until %s"},
	"unmuted":         {"🔔 알림 재개", "🔔 Notifications resumed"},
	"notify_off":      {"알림 파이프라인이 꺼져 있습니다", "The notification pipeline is not running"},
	"unknown_button":  {"알 수 없는 버튼", "Unknown button"},
	"confirm_needed":  {"확인이 필요한 명령입니다", "This command needs confirmation"},
	"toast_executed":  {"실행됨", "Done"},
	"toast_cancelled": {"취소됨", "Cancelled"},
	"stamp_executed":  {"✅ 실행됨", "✅ Executed"},
	"stamp_cancelled": {"✅ 취소됨", "✅ Cancelled"},
	"stamp_dismissed": {"❎ 취소됨", "❎ Dismissed"},
	"via_telegram":    {"텔레그램", "Telegram"},
	"btn_confirm":     {"확인", "Confirm"},
	"btn_cancel":      {"취소", "Cancel"},
	"btn_lock":        {"🔒 잠금", "🔒 Lock"},
	"btn_screenoff":   {"🖥 화면 끄기", "🖥 Screen off"},
	"btn_sleep":       {"🌙 절전", "🌙 Sleep"},
	"btn_restart":     {"🔄 재시작", "🔄 Restart"},
	"btn_shutdown":    {"⏻ 종료", "⏻ Shut down"},
	"btn_cancel_sch":  {"⏱ 예약 취소", "⏱ Cancel schedule"},
	// /status
	"st_uptime":    {"가동", "Uptime"},
	"st_schedule":  {"예약", "Schedule"},
	"st_none":      {"없음", "none"},
	"st_remaining": {"%s · %s · %s 남음 (%s)", "%s · %s · %s left (%s)"},
	"st_remote":    {"마지막 원격 명령", "Last remote command"},
	"st_muted":     {"알림 일시 중지", "Notifications paused"},
	"st_until":     {"%s까지", "until %s"},
	// command and origin labels
	"cmd_shutdown":      {"종료", "Shut down"},
	"cmd_restart":       {"재시작", "Restart"},
	"cmd_suspend":       {"절전", "Sleep"},
	"cmd_hibernate":     {"최대 절전", "Hibernate"},
	"cmd_lock":          {"잠금", "Lock"},
	"cmd_turnscreenoff": {"화면 끄기", "Screen off"},
	"origin_ui":         {"앱", "app"},
	"origin_remote":     {"원격", "remote"},
	"origin_telegram":   {"텔레그램", "Telegram"},
}

// tgText returns the ko/en string for key (telegram.lang), formatted with
// args when given.
func tgText(key string, args ...any) string {
	pair, ok := tgTexts[key]
	if !ok {
		return key
	}
	s := pair[0]
	if getConfig().Telegram.Lang == "en" {
		s = pair[1]
	}
	if len(args) > 0 {
		s = fmt.Sprintf(s, args...)
	}
	return s
}

// tgCommandLabel names a Commands key for humans; unknown keys are
// escaped verbatim.
func tgCommandLabel(name string) string {
	if _, ok := tgTexts["cmd_"+name]; ok {
		return tgText("cmd_" + name)
	}
	return html.EscapeString(name)
}

func tgOriginLabel(origin string) string {
	if _, ok := tgTexts["origin_"+origin]; ok {
		return tgText("origin_" + origin)
	}
	return html.EscapeString(origin)
}

// tgStamp is the result line an edited message ends with:
// "✅ 실행됨 · 14:32 · 텔레그램".
func tgStamp(key string) string {
	return tgText(key) + " · " + time.Now().Format("15:04") + " · " + tgText("via_telegram")
}

func tgPCName() string {
	if n := getConfig().Telegram.PCName; n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "PC"
}

// ---- keyboards --------------------------------------------------------------

func tgButton(textKey, data string) telegram.InlineButton {
	return telegram.InlineButton{Text: tgText(textKey), CallbackData: data}
}

// tgConfirmKeyboard is [확인](exec:<cmd>) [취소](dismiss:).
func tgConfirmKeyboard(name string) *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{InlineKeyboard: [][]telegram.InlineButton{{
		tgButton("btn_confirm", "exec:"+name),
		tgButton("btn_cancel", "dismiss:"),
	}}}
}

// tgMenuKeyboard is the /menu layout: safe commands run at once (cmd:),
// power commands go through confirm:, and the last row cancels a schedule.
func tgMenuKeyboard() *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{InlineKeyboard: [][]telegram.InlineButton{
		{tgButton("btn_lock", "cmd:lock"), tgButton("btn_screenoff", "cmd:turnscreenoff")},
		{tgButton("btn_sleep", "confirm:suspend"), tgButton("btn_restart", "confirm:restart")},
		{tgButton("btn_shutdown", "confirm:shutdown"), tgButton("btn_cancel_sch", "cancel:")},
	}}
}

// ---- actions ------------------------------------------------------------------

// runTelegramCommand executes a registry command in the background, the
// same way the HTTP handler does.
func runTelegramCommand(name string) bool {
	cmd, ok := Commands[name]
	if !ok {
		return false
	}
	logMsg("Telegram command: %s", name)
	if cmd.Execute != nil {
		go cmd.Execute()
	}
	return true
}

// runScheduledNow cancels the active schedule (by=telegram) and executes
// its command immediately; ok is false when nothing was scheduled.
func runScheduledNow() (string, bool) {
	name, ok := takeSchedule("telegram")
	if !ok {
		return "", false
	}
	logMsg("Telegram: running scheduled %s now", name)
	runTelegramCommand(name)
	return name, true
}

// parseMuteDuration accepts Go durations ("30m", "2h", "1h30m") and bare
// minutes ("45"). The result is clamped to 1 minute .. 7 days.
func parseMuteDuration(arg string) (time.Duration, bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return 0, false
	}
	var d time.Duration
	if n, err := strconv.Atoi(arg); err == nil {
		d = time.Duration(n) * time.Minute
	} else if parsed, err := time.ParseDuration(arg); err == nil {
		d = parsed
	} else {
		return 0, false
	}
	if d < time.Minute || d > 7*24*time.Hour {
		return 0, false
	}
	return d, true
}

// ---- telegram.CommandHandler ----------------------------------------------------

// telegramControl implements telegram.CommandHandler (and EditKeyboarder)
// on top of the service's schedule and command registry.
type telegramControl struct{}

// HandleCommand answers a slash command from an allowed chat.
func (c telegramControl) HandleCommand(_ context.Context, chatID string, cmd string, args []string) (string, *telegram.InlineKeyboard, error) {
	switch cmd {
	case "help", "start":
		return tgText("help"), nil, nil
	case "status":
		return tgStatusText(), nil, nil
	case "menu":
		return tgText("menu_title", html.EscapeString(tgPCName())), tgMenuKeyboard(), nil
	case "lock", "screenoff":
		name := telegramAliases[cmd]
		runTelegramCommand(name)
		return tgText("executed", tgCommandLabel(name)), nil, nil
	case "sleep", "hibernate", "restart", "shutdown":
		return c.powerCommand(telegramAliases[cmd], args)
	case "cancel":
		if name, ok := takeSchedule("telegram"); ok {
			return tgText("cancelled", tgCommandLabel(name)), nil, nil
		}
		return tgText("no_schedule"), nil, nil
	case "now":
		if name, ok := runScheduledNow(); ok {
			return tgText("run_now", tgCommandLabel(name)), nil, nil
		}
		return tgText("no_schedule"), nil, nil
	case "mute":
		return c.mute(args)
	case "unmute":
		b := currentBus()
		if b == nil {
			return tgText("notify_off"), nil, nil
		}
		b.Unmute()
		logMsg("Telegram: notifications unmuted")
		return tgText("unmuted"), nil, nil
	}
	return tgText("unknown_command", html.EscapeString("/"+cmd)) + "\n\n" + tgText("help"), nil, nil
}

// powerCommand handles /sleep /hibernate /restart /shutdown [minutes]:
// without minutes it asks for confirmation, with minutes it schedules.
func (telegramControl) powerCommand(name string, args []string) (string, *telegram.InlineKeyboard, error) {
	label := tgCommandLabel(name)
	if len(args) == 0 {
		return tgText("confirm_q", label), tgConfirmKeyboard(name), nil
	}
	minutes, err := strconv.Atoi(args[0])
	if err != nil || minutes < 1 || minutes > 1440 {
		return tgText("bad_minutes"), nil, fmt.Errorf("invalid minutes %q", args[0])
	}
	delay := time.Duration(minutes) * time.Minute
	if err := setSchedule(name, delay, originTelegram); err != nil {
		return tgText("schedule_failed", html.EscapeString(err.Error())), nil, err
	}
	return tgText("scheduled", label, minutes, time.Now().Add(delay).Format("15:04")), nil, nil
}

func (telegramControl) mute(args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) == 0 {
		return tgText("mute_usage"), nil, nil
	}
	d, ok := parseMuteDuration(args[0])
	if !ok {
		return tgText("mute_usage"), nil, fmt.Errorf("invalid mute duration %q", args[0])
	}
	b := currentBus()
	if b == nil {
		return tgText("notify_off"), nil, nil
	}
	b.Mute(d)
	logMsg("Telegram: notifications muted for %s", d)
	return tgText("muted", time.Now().Add(d).Format("15:04")), nil, nil
}

// HandleCallback acts on an inline button. Verbs:
//
//	exec:<cmd>     run now (after a confirmation prompt)
//	cmd:<cmd>      run a safe command from /menu
//	confirm:<cmd>  turn the message into a confirmation prompt (EditKeyboard adds the buttons)
//	dismiss:       close a confirmation prompt
//	cancel:        cancel the active schedule / grace period (also on remote.grace_scheduled)
//	runnow:        cancel it and run the command now (remote.grace_scheduled)
func (telegramControl) HandleCallback(_ context.Context, chatID string, msgID int, data string) (string, string, error) {
	verb, arg, _ := strings.Cut(data, ":")
	switch verb {
	case "exec":
		name, ok := telegramCommandName(arg)
		if !ok {
			return "", tgText("unknown_button"), fmt.Errorf("callback %q: unknown command", data)
		}
		runTelegramCommand(name)
		return tgCommandLabel(name) + "\n" + tgStamp("stamp_executed"), tgText("toast_executed"), nil
	case "cmd":
		name, ok := telegramCommandName(arg)
		if !ok {
			return "", tgText("unknown_button"), fmt.Errorf("callback %q: unknown command", data)
		}
		if !telegramSafeCommands[name] {
			return "", tgText("confirm_needed"), nil
		}
		runTelegramCommand(name)
		return tgCommandLabel(name) + "\n" + tgStamp("stamp_executed"), tgText("toast_executed"), nil
	case "confirm":
		name, ok := telegramCommandName(arg)
		if !ok || !graceCommands[name] {
			return "", tgText("unknown_button"), fmt.Errorf("callback %q: not a power command", data)
		}
		return tgText("confirm_q", tgCommandLabel(name)), "", nil
	case "dismiss":
		return tgStamp("stamp_dismissed"), "", nil
	case "cancel":
		name, ok := takeSchedule("telegram")
		if !ok {
			return "", tgText("no_schedule"), nil
		}
		return tgCommandLabel(name) + "\n" + tgStamp("stamp_cancelled"), tgText("toast_cancelled"), nil
	case "runnow":
		name, ok := runScheduledNow()
		if !ok {
			return "", tgText("no_schedule"), nil
		}
		return tgCommandLabel(name) + "\n" + tgStamp("stamp_executed"), tgText("toast_executed"), nil
	}
	return "", tgText("unknown_button"), fmt.Errorf("unknown callback %q", data)
}

// EditKeyboard implements telegram.EditKeyboarder: a confirm:<cmd> edit
// keeps [확인][취소] buttons, every other edit drops the keyboard.
func (telegramControl) EditKeyboard(data string) *telegram.InlineKeyboard {
	verb, arg, _ := strings.Cut(data, ":")
	if verb != "confirm" {
		return nil
	}
	if name, ok := telegramCommandName(arg); ok && graceCommands[name] {
		return tgConfirmKeyboard(name)
	}
	return nil
}

// Unauthorized raises security.unknown_chat for traffic from other chats.
func (telegramControl) Unauthorized(chatID, username, text string) {
	logMsg("Telegram: ignored message from unknown chat %s (%s): %s", chatID, username, truncate(text, 64))
	emit("security", "unknown_chat", map[string]string{
		"chat_id":  chatID,
		"username": truncate(username, 64),
		"text":     truncate(text, 64),
	})
}

// tgStatusText builds the /status reply.
func tgStatusText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "🖥 <b>%s</b> · %s\n", html.EscapeString(tgPCName()), html.EscapeString(Version))
	fmt.Fprintf(&b, "%s: %s\n", tgText("st_uptime"), formatUptime(time.Since(serviceStartedAt)))

	s := getSchedule()
	if s["active"] == true {
		command, _ := s["command"].(string)
		origin, _ := s["origin"].(string)
		remaining, _ := s["remainingSec"].(int)
		at := ""
		if ts, ok := s["executeAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				at = t.Format("15:04")
			}
		}
		fmt.Fprintf(&b, "%s: %s\n", tgText("st_schedule"),
			tgText("st_remaining", tgCommandLabel(command), tgOriginLabel(origin), formatUptime(time.Duration(remaining)*time.Second), at))
	} else {
		fmt.Fprintf(&b, "%s: %s\n", tgText("st_schedule"), tgText("st_none"))
	}

	if lr := getLastRemote(); lr.Command != "" {
		fmt.Fprintf(&b, "%s: %s · <code>%s</code> · %s", tgText("st_remote"),
			tgCommandLabel(lr.Command), html.EscapeString(lr.From), lr.At.Format("15:04:05"))
	} else {
		fmt.Fprintf(&b, "%s: %s", tgText("st_remote"), tgText("st_none"))
	}

	if bus := currentBus(); bus != nil {
		if until := bus.MutedUntil(); !until.IsZero() {
			fmt.Fprintf(&b, "\n%s: %s", tgText("st_muted"), tgText("st_until", until.Format("15:04")))
		}
	}
	return b.String()
}

// formatUptime renders a duration as "3d 04h", "1h 05m" or "4m 09s".
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %02dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// ---- lifecycle -------------------------------------------------------------------

// telegramRunner owns the running poller. managed is set between
// startTelegramControl and stopTelegramControl so that saveConfig in the
// installer or in tests never spins up a poller.
type telegramRunner struct {
	mu      sync.Mutex
	managed bool
	key     string // settings the running poller was built from
	cancel  context.CancelFunc
	done    chan struct{}
}

var tgRunner telegramRunner

// startTelegramControl enables the lifecycle and starts polling when the
// current config asks for it. Called once at service start.
func startTelegramControl() {
	tgRunner.mu.Lock()
	tgRunner.managed = true
	tgRunner.mu.Unlock()
	reconcileTelegramControl()
}

// stopTelegramControl stops the poller (if any) and disables the lifecycle.
func stopTelegramControl() {
	tgRunner.mu.Lock()
	defer tgRunner.mu.Unlock()
	tgRunner.managed = false
	tgRunner.stopLocked()
}

// reconcileTelegramControl (re)starts or stops the poller so it matches the
// live config. saveConfig calls it after every save; it is a no-op until
// startTelegramControl has run.
func reconcileTelegramControl() {
	tgRunner.mu.Lock()
	defer tgRunner.mu.Unlock()
	if !tgRunner.managed {
		return
	}
	cfg := getConfig().Telegram
	key := telegramControlKey(cfg)
	if key == tgRunner.key {
		return
	}
	tgRunner.stopLocked()
	if key == "" {
		return
	}
	tgRunner.startLocked(cfg, key)
}

// telegramControlRunning reports whether a poller goroutine is alive.
func telegramControlRunning() bool {
	tgRunner.mu.Lock()
	defer tgRunner.mu.Unlock()
	return tgRunner.done != nil
}

// telegramControlKey summarises the settings that require a poller restart;
// "" means control must be off. AllowedChatIDs are read live, so they are
// not part of the key.
func telegramControlKey(cfg TelegramConfig) string {
	if !cfg.Enabled || !cfg.ControlEnabled || cfg.BotToken == "" || cfg.ChatID == "" {
		return ""
	}
	return cfg.BotToken + "\x00" + cfg.ChatID + "\x00" + cfg.Lang
}

// telegramAllowedChatIDs is the poller's allow-list: allowed_chat_ids, or
// just chat_id when that list is empty.
func telegramAllowedChatIDs() []string {
	cfg := getConfig().Telegram
	ids := make([]string, 0, len(cfg.AllowedChatIDs)+1)
	for _, id := range cfg.AllowedChatIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 && cfg.ChatID != "" {
		ids = append(ids, cfg.ChatID)
	}
	return ids
}

// telegramBotCommands is the setMyCommands menu (design doc §9).
func telegramBotCommands(lang string) []telegram.BotCommand {
	ko := lang != "en"
	pick := func(k, e string) string {
		if ko {
			return k
		}
		return e
	}
	return []telegram.BotCommand{
		{Command: "status", Description: pick("상태", "Status")},
		{Command: "menu", Description: pick("버튼 메뉴", "Button menu")},
		{Command: "lock", Description: pick("잠금", "Lock")},
		{Command: "screenoff", Description: pick("화면 끄기", "Screen off")},
		{Command: "sleep", Description: pick("절전 [분]", "Sleep [minutes]")},
		{Command: "hibernate", Description: pick("최대 절전 [분]", "Hibernate [minutes]")},
		{Command: "restart", Description: pick("재시작 [분]", "Restart [minutes]")},
		{Command: "shutdown", Description: pick("종료 [분]", "Shut down [minutes]")},
		{Command: "cancel", Description: pick("예약·유예 취소", "Cancel schedule")},
		{Command: "now", Description: pick("예약·유예 즉시 실행", "Run schedule now")},
		{Command: "mute", Description: pick("알림 일시 중지 (30m, 2h)", "Pause notifications (30m, 2h)")},
		{Command: "unmute", Description: pick("알림 재개", "Resume notifications")},
		{Command: "help", Description: pick("도움말", "Help")},
	}
}

// startLocked builds the client and runs the poller. tgRunner.mu is held.
func (r *telegramRunner) startLocked(cfg TelegramConfig, key string) {
	token, err := secret.Unprotect(cfg.BotToken)
	if err != nil || token == "" {
		logMsg("Telegram control: cannot read bot token (%v); control stays off", err)
		return
	}
	cli := telegram.NewClient(token, telegram.WithBaseURL(telegramBaseURL))
	poller := telegram.NewPoller(cli, telegram.PollerOptions{
		AllowedChatIDs: telegramAllowedChatIDs,
		Handler:        telegramControl{},
		Log:            logMsg,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.key, r.cancel, r.done = key, cancel, done
	lang := cfg.Lang
	go func() {
		defer close(done)
		if err := cli.SetMyCommands(ctx, telegramBotCommands(lang)); err != nil && ctx.Err() == nil {
			logMsg("Telegram control: setMyCommands failed: %v", err)
		}
		logMsg("Telegram control: polling started")
		if err := poller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logMsg("Telegram control: poller stopped: %v", err)
		}
		logMsg("Telegram control: polling stopped")
	}()
}

// stopLocked cancels the poller and waits briefly for it. tgRunner.mu is held.
func (r *telegramRunner) stopLocked() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		logMsg("Telegram control: poller did not stop in time")
	}
	r.key, r.cancel, r.done = "", nil, nil
}

// currentBus returns the notification bus or nil (before startNotifier).
func currentBus() *notify.Bus {
	busMu.RLock()
	defer busMu.RUnlock()
	return bus
}
