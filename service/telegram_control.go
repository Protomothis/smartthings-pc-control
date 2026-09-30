package service

import (
	"context"
	"errors"
	"fmt"
	"html"
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
// Message edits after a button press keep the original text and append a
// result line (#62); the grace-message lifecycle lives in telegram_grace.go.

// serviceStartedAt approximates process start for the /status uptime line.
var serviceStartedAt = time.Now()

// remoteRecord is the last SmartThings command, for /status.
type remoteRecord struct {
	Command string
	From    string
	// Origin is the path it arrived on: "remote" for the legacy
	// /{secret}/{command} URL, "smartthings" for /st/v1/command (#67).
	Origin string
	At     time.Time
	// Preset is set for a "preset" command (#109): which slot ran, under
	// which name, and the outcome ("started" or an error code).
	Preset *presetRecord
}

// presetRecord is the preset part of a remoteRecord.
type presetRecord struct {
	Slot   int
	Name   string
	Result string
}

var (
	lastRemote   remoteRecord
	lastRemoteMu sync.Mutex
)

// noteRemoteCommand is called by the legacy command handler for every
// accepted non-ping command.
func noteRemoteCommand(command, from string) {
	noteRemoteCommandBy(command, from, "remote")
}

// noteRemoteCommandBy is noteRemoteCommand for a caller that knows which
// protocol the command arrived on.
func noteRemoteCommandBy(command, from, origin string) {
	lastRemoteMu.Lock()
	lastRemote = remoteRecord{Command: command, From: from, Origin: origin, At: time.Now()}
	lastRemoteMu.Unlock()
}

// notePresetCommand records a preset run as the last remote command.
func notePresetCommand(p Preset, from, origin, result string) {
	lastRemoteMu.Lock()
	lastRemote = remoteRecord{Command: "preset", From: from, Origin: origin, At: time.Now(),
		Preset: &presetRecord{Slot: p.Slot, Name: p.Name, Result: result}}
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
	"screenon":  "turnscreenon",
	"sleep":     "suspend",
	"hibernate": "hibernate",
	"restart":   "restart",
	"shutdown":  "shutdown",
}

// telegramSafeCommands run from a button without confirmation.
var telegramSafeCommands = map[string]bool{"lock": true, "turnscreenoff": true, "turnscreenon": true}

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
			"/screenon – 화면 켜기\n" +
			"/sleep /hibernate /restart /shutdown [분] – 확인 후 즉시 실행, 분을 주면 예약\n" +
			"/cancel – 예약·유예 취소\n" +
			"/now – 예약·유예 즉시 실행\n" +
			"/awake [분|off] – 잠들지 않기 (자동 절전 막기, 0 = 끌 때까지)\n" +
			"/vol [0-100|+n|-n] – PC 볼륨 (값을 빼면 현재 볼륨)\n" +
			"/mute · /unmute – PC 음소거 켜기·끄기\n" +
			"/play /pause /stop /next /prev – 미디어 재생 제어\n" +
			"/np – 지금 재생 중인 미디어\n" +
			"/quiet 30m|2h|off – 알림 일시 중지·재개\n" +
			"/say 문구 – PC 화면에 알림 띄우기\n" +
			"/presets – 프리셋 목록\n" +
			"/run 이름|번호 – 프리셋 실행\n" +
			"/help – 이 목록",
		"<b>Commands</b>\n" +
			"/status – status\n" +
			"/menu – button menu\n" +
			"/lock – lock\n" +
			"/screenoff – screen off\n" +
			"/screenon – screen on\n" +
			"/sleep /hibernate /restart /shutdown [minutes] – confirm then run now, or schedule with minutes\n" +
			"/cancel – cancel the schedule/grace period\n" +
			"/now – run the schedule/grace command now\n" +
			"/awake [minutes|off] – keep awake (hold off idle sleep, 0 = until turned off)\n" +
			"/vol [0-100|+n|-n] – PC volume (no value: the current volume)\n" +
			"/mute · /unmute – mute or unmute the PC\n" +
			"/play /pause /stop /next /prev – media playback\n" +
			"/np – what is playing now\n" +
			"/quiet 30m|2h|off – pause or resume notifications\n" +
			"/say text – show a notification on the PC\n" +
			"/presets – list the presets\n" +
			"/run name|number – run a preset\n" +
			"/help – this list",
	},
	"unknown_command": {"알 수 없는 명령: <code>%s</code>", "Unknown command: <code>%s</code>"},
	// The PC name is no longer part of this text: every reply gets the
	// "🖥 <b>name</b>" header from tgWithHeader (#75).
	"menu_title":      {"무엇을 할까요?", "What should I do?"},
	"executed":        {"✅ %s 실행", "✅ %s executed"},
	"confirm_q":       {"⚠️ <b>%s</b> – 지금 바로 실행할까요?", "⚠️ <b>%s</b> – run it right now?"},
	"bad_minutes":     {"분은 1~4320(3일) 사이의 숫자여야 합니다. 예: <code>/shutdown 30</code>", "Minutes must be a number from 1 to 4320 (3 days), e.g. <code>/shutdown 30</code>"},
	"scheduled":       {"⏱ <b>%s</b> %s 후 예약됨 (%s)", "⏱ <b>%s</b> scheduled in %s (%s)"},
	"schedule_failed": {"예약 실패: %s", "Scheduling failed: %s"},
	"cancelled":       {"✅ 취소됨: %s", "✅ Cancelled: %s"},
	"no_schedule":     {"활성 예약 없음", "No active schedule"},
	"run_now":         {"✅ 지금 실행: %s", "✅ Running now: %s"},
	"mute_usage":      {"사용법: <code>/quiet 30m</code>, <code>/quiet 2h</code> 또는 <code>/quiet off</code>", "Usage: <code>/quiet 30m</code>, <code>/quiet 2h</code> or <code>/quiet off</code>"},
	"muted":           {"🔕 %s까지 알림 일시 중지", "🔕 Notifications paused until %s"},
	"unmuted":         {"🔔 알림 재개", "🔔 Notifications resumed"},
	"notify_off":      {"알림 파이프라인이 꺼져 있습니다", "The notification pipeline is not running"},
	"unknown_button":  {"알 수 없는 버튼", "Unknown button"},
	"stale":           {"⏹ <code>%s</code> — PC가 꺼져 있거나 절전 중일 때 보낸 오래된 명령이라 실행하지 않았습니다. 필요하면 다시 보내 주세요.", "⏹ <code>%s</code> — sent while this PC was off or asleep, so it was not run. Send it again if you still want it."},
	"confirm_needed":  {"확인이 필요한 명령입니다", "This command needs confirmation"},
	"toast_executed":  {"실행됨", "Done"},
	"toast_cancelled": {"취소됨", "Cancelled"},
	"stamp_executed":  {"✅ 실행됨", "✅ Executed"},
	"stamp_cancelled": {"✅ 취소됨", "✅ Cancelled"},
	"stamp_dismissed": {"❎ 취소됨", "❎ Dismissed"},
	"stamp_ran":       {"▶️ 실행됨", "▶️ Executed"},
	"stamp_replaced":  {"🔁 대체됨", "🔁 Replaced"},
	"stamp_stale":     {"⏹ 이미 처리됨", "⏹ Already handled"},
	// who cancelled / ran the schedule (the by field of takeSchedule)
	"by_toast":       {"토스트", "toast"},
	"by_tray":        {"트레이", "tray"},
	"by_app":         {"앱", "app"},
	"by_webui":       {"WebUI", "WebUI"},
	"by_api":         {"API", "API"},
	"by_telegram":    {"텔레그램", "Telegram"},
	"by_smartthings": {"SmartThings", "SmartThings"},
	"by_timer":       {"타이머", "timer"},
	"btn_confirm":    {"확인", "Confirm"},
	"btn_cancel":     {"취소", "Cancel"},
	"btn_lock":       {"🔒 잠금", "🔒 Lock"},
	"btn_screenoff":  {"🖥 화면 끄기", "🖥 Screen off"},
	"btn_sleep":      {"🌙 절전", "🌙 Sleep"},
	"btn_restart":    {"🔄 재시작", "🔄 Restart"},
	"btn_shutdown":   {"⏻ 종료", "⏻ Shut down"},
	"btn_cancel_sch": {"⏱ 예약 취소", "⏱ Cancel schedule"},
	// /status
	"st_uptime":    {"가동", "Uptime"},
	"st_schedule":  {"예약", "Schedule"},
	"st_none":      {"없음", "none"},
	"st_remaining": {"%s · %s · %s 남음 (%s)", "%s · %s · %s left (%s)"},
	"st_remote":    {"마지막 원격 명령", "Last remote command"},
	"st_muted":     {"알림 일시 중지", "Notifications paused"},
	"st_until":     {"%s까지", "until %s"},
	// keep-awake (#111)
	"st_awake":          {"잠들지 않기", "Keep awake"},
	"st_on":             {"켜짐", "on"},
	"st_off":            {"꺼짐", "off"},
	"st_awake_forever":  {"끌 때까지", "until turned off"},
	"st_tomorrow":       {"내일 %s", "tomorrow %s"},
	"awake_on":          {"☕ 잠들지 않기 켜짐 · %s", "☕ Keep awake on · %s"},
	"awake_off":         {"💤 잠들지 않기 꺼짐 — 자동 절전 설정을 다시 따릅니다", "💤 Keep awake off — the PC may sleep on its idle timer again"},
	"awake_already_off": {"잠들지 않기는 이미 꺼져 있습니다", "Keep awake is already off"},
	"awake_usage":       {"사용법: <code>/awake</code> (기본 시간), <code>/awake 90</code> (분, 0 = 끌 때까지, 최대 1440), <code>/awake off</code>", "Usage: <code>/awake</code> (default period), <code>/awake 90</code> (minutes, 0 = until turned off, at most 1440), <code>/awake off</code>"},
	"awake_failed":      {"잠들지 않기 실패: %s", "Keep awake failed: %s"},
	// volume and media keys (#104, #105)
	"vol_state":         {"볼륨 %d%% · 음소거 %s", "Volume %d%% · mute %s"},
	"vol_usage":         {"사용법: <code>/vol</code> (현재 볼륨), <code>/vol 30</code> (0–100), <code>/vol +10</code>, <code>/vol -10</code>", "Usage: <code>/vol</code> (current volume), <code>/vol 30</code> (0–100), <code>/vol +10</code>, <code>/vol -10</code>"},
	"media_sent":        {"%s 키를 보냈습니다", "Sent %s"},
	"media_playpause":   {"⏯ 재생/일시정지", "⏯ play/pause"},
	"media_stop":        {"⏹ 정지", "⏹ stop"},
	"media_next":        {"⏭ 다음 곡", "⏭ next track"},
	"media_prev":        {"⏮ 이전 곡", "⏮ previous track"},
	"media_disabled":    {"미디어 제어가 꺼져 있습니다 (설정 <code>media.enabled</code>)", "Media control is turned off (setting <code>media.enabled</code>)"},
	"media_no_user":     {"로그인한 사용자가 없어 실행할 수 없습니다", "Nobody is logged in to this PC"},
	"media_unsupported": {"이 PC에서는 할 수 없습니다: %s", "Not possible on this PC: %s"},
	"media_timeout":     {"PC가 3초 안에 답하지 않았습니다", "The PC did not answer within 3 seconds"},
	"media_failed":      {"실패: %s", "Failed: %s"},
	// now playing (#117)
	"np_none":           {"재생 중인 미디어 없음", "Nothing is playing"},
	"np_playing":        {"재생 중", "Playing"},
	"np_paused":         {"일시정지", "Paused"},
	"np_stopped":        {"정지", "Stopped"},
	"np_private":        {"(재생 정보 공유가 꺼져 있습니다)", "(sharing what is playing is turned off)"},
	"st_media":          {"미디어", "Media"},
	"media_done_play":   {"▶ 재생했습니다", "▶ Playing"},
	"media_done_pause":  {"⏸ 일시정지했습니다", "⏸ Paused"},
	"media_done_stop":   {"⏹ 정지했습니다", "⏹ Stopped"},
	"media_done_next":   {"⏭ 다음 곡으로 넘겼습니다", "⏭ Skipped to the next track"},
	"media_done_prev":   {"⏮ 이전 곡으로 돌아갔습니다", "⏮ Back to the previous track"},
	"quiet_still":       {"🔕 알림은 %s까지 일시 중지 중입니다 — <code>/quiet off</code>로 재개", "🔕 Notifications stay paused until %s — <code>/quiet off</code> resumes them"},
	// battery (#112)
	"st_battery":          {"배터리", "Battery"},
	"st_battery_charging": {"충전 중", "charging"},
	"st_battery_ac":       {"전원 연결됨", "plugged in"},
	"st_battery_unknown":  {"잔량 알 수 없음", "level unknown"},
	// running-app detection (#110), "활동: 게임 중 · Steam"
	"st_activity":          {"활동", "Activity"},
	"activity_kind_game":   {"게임 중", "Gaming"},
	"activity_kind_stream": {"방송 중", "Streaming"},
	"activity_kind_media":  {"미디어 재생 중", "Playing media"},
	"activity_kind_work":   {"작업 중", "Working"},
	"activity_kind_other":  {"실행 중", "Running"},
	// command and origin labels
	"cmd_shutdown":       {"종료", "Shut down"},
	"cmd_restart":        {"재시작", "Restart"},
	"cmd_suspend":        {"절전", "Sleep"},
	"cmd_hibernate":      {"최대 절전", "Hibernate"},
	"cmd_lock":           {"잠금", "Lock"},
	"cmd_turnscreenoff":  {"화면 끄기", "Screen off"},
	"cmd_turnscreenon":   {"화면 켜기", "Screen on"},
	"cmd_preset":         {"프리셋", "Preset"},
	"origin_ui":          {"앱", "app"},
	"origin_remote":      {"원격", "remote"},
	"origin_telegram":    {"텔레그램", "Telegram"},
	"origin_smartthings": {"SmartThings", "SmartThings"},
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

// tgStamp is the result line an edited message ends with when a Telegram
// button or command did the work: "✅ 실행됨 · 14:32 · 텔레그램".
func tgStamp(key string) string {
	return tgStampBy(key, "telegram")
}

// tgStampBy is tgStamp for any origin: "✅ 취소됨 · 14:32 · 트레이". An empty
// by (a replaced schedule) leaves the origin off.
func tgStampBy(key, by string) string {
	line := tgText(key) + " · " + time.Now().Format("15:04")
	if by != "" {
		line += " · " + tgByLabel(by)
	}
	return line
}

// tgByLabel names a cancel/run origin (toast, tray, app, webui, api,
// telegram, timer); unknown values are escaped verbatim.
func tgByLabel(by string) string {
	if _, ok := tgTexts["by_"+by]; ok {
		return tgText("by_" + by)
	}
	return html.EscapeString(by)
}

// tgPlain is what Telegram hands back as message.text for the HTML h: tags
// stripped, entities decoded. Used to recognise the bot's own messages.
func tgPlain(h string) string {
	var b strings.Builder
	inTag := false
	for _, r := range h {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(b.String())
}

// tgKeep returns the HTML to keep when editing a message whose plain text
// is msgText: the matching candidate when the text is one of the bot's own
// (formatting preserved), otherwise msgText escaped. "" stays "".
func tgKeep(msgText string, candidates ...string) string {
	if msgText == "" {
		return ""
	}
	for _, c := range candidates {
		if tgPlain(c) == msgText {
			return c
		}
	}
	return html.EscapeString(msgText)
}

// tgPromptCandidates are every confirmation prompt the bot can have sent,
// on its own (/shutdown) or appended to the /menu (confirm:<cmd>). Both the
// headered form (#75) and the bare one are listed so a prompt sent by an
// older version is still recognised when its button is pressed.
func tgPromptCandidates() []string {
	menu := tgMenuTitle()
	var out []string
	for name := range graceCommands {
		q := tgText("confirm_q", tgCommandLabel(name))
		out = append(out, tgWithHeader(q), q, menu+"\n\n"+q)
	}
	return out
}

func tgPCName() string {
	if n := getConfig().Telegram.PCName; n != "" {
		return n
	}
	return hostname()
}

// tgHeader is the "🖥 <b>name</b>" line every message from this PC starts
// with (#75, hub-agent doc §1).
func tgHeader() string { return telegram.Header(tgPCName()) }

// tgWithHeader prefixes tgHeader() to a reply that does not already carry
// one — including text read back from Telegram, where the tags are gone but
// the 🖥 icon is not.
func tgWithHeader(h string) string { return telegram.WithHeader(tgPCName(), h) }

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
// cancel:menu (rather than the grace message's bare cancel:) tells
// HandleCallback the press came from the menu, which stays usable.
func tgMenuKeyboard() *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{InlineKeyboard: [][]telegram.InlineButton{
		{tgButton("btn_lock", "cmd:lock"), tgButton("btn_screenoff", "cmd:turnscreenoff")},
		{tgButton("btn_sleep", "confirm:suspend"), tgButton("btn_restart", "confirm:restart")},
		{tgButton("btn_shutdown", "confirm:shutdown"), tgButton("btn_cancel_sch", "cancel:menu")},
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

// runScheduledNow ends the active schedule (by=telegram) and executes its
// command immediately; ok is false when nothing was scheduled.
func runScheduledNow() (string, bool) {
	name, ok := takeScheduleForRun("telegram")
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

// HandleCommand answers a slash command from an allowed chat. Every reply
// carries the PC-name header (#75); the work is done by handleCommand.
func (c telegramControl) HandleCommand(ctx context.Context, chatID string, cmd string, args []string) (string, *telegram.InlineKeyboard, error) {
	h, kb, err := c.handleCommand(ctx, chatID, cmd, args)
	return tgWithHeader(h), kb, err
}

func (c telegramControl) handleCommand(ctx context.Context, chatID string, cmd string, args []string) (string, *telegram.InlineKeyboard, error) {
	switch cmd {
	case "help", "start":
		return tgText("help"), nil, nil
	case telegram.StaleCommand:
		// Sent while the PC was asleep / offline; the poller already logged it.
		text := ""
		if len(args) > 0 {
			text = args[0]
		}
		return tgText("stale", html.EscapeString(text)), nil, nil
	case "status":
		return tgStatusText(), nil, nil
	case "menu":
		return tgMenuTitle(), tgMenuKeyboard(), nil
	case "lock", "screenoff", "screenon":
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
	case "awake":
		return c.awake(args)
	case "vol":
		return tgVolume(ctx, args)
	case "say":
		return c.say(chatID, args)
	case "presets":
		return tgPresetList(), nil, nil
	case "run":
		return c.runPreset(args)
	case "mute":
		// /mute 30m is what paused notifications before #104; a duration
		// still does, a bare /mute mutes the PC.
		if len(args) > 0 {
			return c.mute(args)
		}
		return tgMediaCommand(ctx, "mute", nil)
	case "unmute":
		return tgMediaCommand(ctx, "unmute", nil)
	case "play", "pause", "stop", "next", "prev":
		return tgMediaCommand(ctx, cmd, nil)
	case "np":
		return tgNowPlaying(ctx)
	case "quiet":
		if len(args) > 0 && strings.EqualFold(args[0], "off") {
			b := currentBus()
			if b == nil {
				return tgText("notify_off"), nil, nil
			}
			b.Unmute()
			logMsg("Telegram: notifications unmuted")
			return tgText("unmuted"), nil, nil
		}
		return c.mute(args)
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
	// #89: up to three days, the same ceiling /st/v1 and the app carry.
	if err != nil || minutes < 1 || minutes > maxScheduleMinutes {
		return tgText("bad_minutes"), nil, fmt.Errorf("invalid minutes %q", args[0])
	}
	delay := time.Duration(minutes) * time.Minute
	if err := setSchedule(name, delay, originTelegram); err != nil {
		return tgText("schedule_failed", html.EscapeString(err.Error())), nil, err
	}
	return tgText("scheduled", label, tgDelay(delay), time.Now().Add(delay).Format("15:04")), nil, nil
}

// tgDelay names a schedule delay in the bot's language: "30분", "2시간",
// "1시간 30분", "1일 3시간" — and formatDelay's "1 d 3 h" in English (#89).
// "4320분 후" is not a sentence anyone reads as three days.
func tgDelay(d time.Duration) string {
	if getConfig().Telegram.Lang == "en" {
		return formatDelay(d)
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		return fmt.Sprintf("%d초", int(d/time.Second))
	case minutes < 60:
		return fmt.Sprintf("%d분", minutes)
	case minutes < 1440:
		if rest := minutes % 60; rest != 0 {
			return fmt.Sprintf("%d시간 %d분", minutes/60, rest)
		}
		return fmt.Sprintf("%d시간", minutes/60)
	default:
		// The odd minutes are noise at a day's distance.
		if hours := (minutes % 1440) / 60; hours != 0 {
			return fmt.Sprintf("%d일 %d시간", minutes/1440, hours)
		}
		return fmt.Sprintf("%d일", minutes/1440)
	}
}

// parseAwakeArg reads the /awake argument: none is the configured default,
// "off" turns keep-awake off, a number is minutes (0 = until turned off, at
// most awakeMaxMinutes).
func parseAwakeArg(args []string) (minutes int, off bool, ok bool) {
	if len(args) == 0 {
		return awakeMinutesOrDefault(nil), false, true
	}
	arg := strings.ToLower(strings.TrimSpace(args[0]))
	if arg == "off" {
		return 0, true, true
	}
	n, err := strconv.Atoi(arg)
	if err != nil || !validAwakeMinutes(n) {
		return 0, false, false
	}
	return n, false, true
}

// awake handles /awake [minutes|off] (#111).
func (telegramControl) awake(args []string) (string, *telegram.InlineKeyboard, error) {
	minutes, off, ok := parseAwakeArg(args)
	if !ok {
		return tgText("awake_usage"), nil, fmt.Errorf("invalid /awake argument %q", args[0])
	}
	ctl := currentAwake()
	if off {
		_, wasOn, err := ctl.TurnOff()
		if err != nil {
			return tgText("awake_failed", html.EscapeString(err.Error())), nil, err
		}
		if !wasOn {
			return tgText("awake_already_off"), nil, nil
		}
		logMsg("Telegram: keep-awake off")
		return tgText("awake_off"), nil, nil
	}
	v, err := ctl.TurnOn(minutes)
	if err != nil {
		return tgText("awake_failed", html.EscapeString(err.Error())), nil, err
	}
	logMsg("Telegram: keep-awake on (%d min)", minutes)
	return tgText("awake_on", tgAwakeSpan(v, ctl.now())), nil, nil
}

// tgAwakeSpan is how long keep-awake lasts: "14:30까지", "내일 09:10까지"
// (a period reaches a day at most) or "끌 때까지".
func tgAwakeSpan(v awakeView, now time.Time) string {
	if v.Until.IsZero() {
		return tgText("st_awake_forever")
	}
	at := v.Until.Format("15:04")
	if y, m, d := v.Until.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		at = tgText("st_tomorrow", at)
	}
	return tgText("st_until", at)
}

// tgAwakeStatus is the /status value: "켜짐 · 14:30까지" or "꺼짐".
func tgAwakeStatus(v awakeView, now time.Time) string {
	if !v.On {
		return tgText("st_off")
	}
	return tgText("st_on") + " · " + tgAwakeSpan(v, now)
}

// tgBatteryStatus is the /status value: "80% · 충전 중", "100% · 전원 연결됨"
// or, on battery, just "80%".
func tgBatteryStatus(b batteryInfo) string {
	level := tgText("st_battery_unknown")
	if b.Percent >= 0 {
		level = strconv.Itoa(b.Percent) + "%"
	}
	switch {
	case b.Charging:
		return level + " · " + tgText("st_battery_charging")
	case b.AC:
		return level + " · " + tgText("st_battery_ac")
	}
	return level
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
//	confirm:<cmd>  append a confirmation prompt to the menu (EditKeyboard adds the buttons)
//	dismiss:       close a confirmation prompt
//	cancel:        cancel the grace period (remote.grace_scheduled)
//	cancel:menu    cancel the active schedule from /menu; the menu stays
//	runnow:        end the grace period and run the command now (remote.grace_scheduled)
//
// Edits keep the message's text (msgText, or the HTML we sent when it is
// known) and append a result line. A cancel:/runnow: press on a message
// whose schedule is gone marks it "already handled" and drops the buttons.
// The edited text keeps (or gains) the PC-name header (#75).
func (c telegramControl) HandleCallback(ctx context.Context, chatID string, msgID int, msgText string, data string) (string, string, error) {
	h, toast, err := c.handleCallback(ctx, chatID, msgID, msgText, data)
	return tgWithHeader(h), toast, err
}

func (telegramControl) handleCallback(_ context.Context, chatID string, msgID int, msgText string, data string) (string, string, error) {
	verb, arg, _ := strings.Cut(data, ":")
	switch verb {
	case "exec":
		name, ok := telegramCommandName(arg)
		if !ok {
			return "", tgText("unknown_button"), fmt.Errorf("callback %q: unknown command", data)
		}
		runTelegramCommand(name)
		return tgAppend(tgKeep(msgText, tgPromptCandidates()...), tgCommandLabel(name), tgStamp("stamp_executed")), tgText("toast_executed"), nil
	case "cmd":
		name, ok := telegramCommandName(arg)
		if !ok {
			return "", tgText("unknown_button"), fmt.Errorf("callback %q: unknown command", data)
		}
		if !telegramSafeCommands[name] {
			return "", tgText("confirm_needed"), nil
		}
		runTelegramCommand(name)
		return tgAppend(tgKeep(msgText, tgMenuTitle()), tgCommandLabel(name), tgStamp("stamp_executed")), tgText("toast_executed"), nil
	case "confirm":
		name, ok := telegramCommandName(arg)
		if !ok || !graceCommands[name] {
			return "", tgText("unknown_button"), fmt.Errorf("callback %q: not a power command", data)
		}
		q := tgText("confirm_q", tgCommandLabel(name))
		if kept := tgKeep(msgText, tgMenuTitle()); kept != "" {
			return kept + "\n\n" + q, "", nil
		}
		return q, "", nil
	case "dismiss":
		return tgAppend(tgKeep(msgText, tgPromptCandidates()...), "", tgStamp("stamp_dismissed")), "", nil
	case "cancel":
		if arg == "menu" {
			name, ok := takeSchedule("telegram")
			if !ok {
				return "", tgText("no_schedule"), nil
			}
			return tgMenuTitle() + "\n" + tgText("cancelled", tgCommandLabel(name)), tgText("toast_cancelled"), nil
		}
		grace, mine := takeGraceMessage(chatID, msgID)
		name, ok := takeSchedule("telegram")
		if !ok {
			return tgStale(msgText), tgText("no_schedule"), nil
		}
		if mine {
			return grace.html + "\n" + tgStamp("stamp_cancelled"), tgText("toast_cancelled"), nil
		}
		return tgAppend(tgKeep(msgText), tgCommandLabel(name), tgStamp("stamp_cancelled")), tgText("toast_cancelled"), nil
	case "runnow":
		grace, mine := takeGraceMessage(chatID, msgID)
		name, ok := runScheduledNow()
		if !ok {
			return tgStale(msgText), tgText("no_schedule"), nil
		}
		if mine {
			return grace.html + "\n" + tgStamp("stamp_ran"), tgText("toast_executed"), nil
		}
		return tgAppend(tgKeep(msgText), tgCommandLabel(name), tgStamp("stamp_ran")), tgText("toast_executed"), nil
	}
	return "", tgText("unknown_button"), fmt.Errorf("unknown callback %q", data)
}

// tgMenuTitle is the /menu text, header included, so tgKeep recognises it
// in the plain text Telegram hands back on a button press.
func tgMenuTitle() string {
	return tgWithHeader(tgText("menu_title"))
}

// tgAppend builds an edit: the kept text (when any) followed by the result
// line. Without kept text the command label heads the message so the
// result is still readable on its own.
func tgAppend(kept, label, stamp string) string {
	if kept != "" {
		return kept + "\n" + stamp
	}
	if label != "" {
		return label + "\n" + stamp
	}
	return stamp
}

// tgStale is the edit for a cancel:/runnow: press whose schedule no longer
// exists: the text stays, the buttons go, "⏹ 이미 처리됨" is appended. When
// the poller had no text (no message), nothing is edited.
func tgStale(msgText string) string {
	if msgText == "" {
		return ""
	}
	return tgKeep(msgText) + "\n" + tgText("stamp_stale")
}

// EditKeyboard implements telegram.EditKeyboarder: a confirm:<cmd> edit
// keeps [확인][취소] buttons, cancel:menu keeps the menu, every other edit
// drops the keyboard.
func (telegramControl) EditKeyboard(data string) *telegram.InlineKeyboard {
	verb, arg, _ := strings.Cut(data, ":")
	switch verb {
	case "confirm":
		if name, ok := telegramCommandName(arg); ok && graceCommands[name] {
			return tgConfirmKeyboard(name)
		}
	case "cancel":
		if arg == "menu" {
			return tgMenuKeyboard()
		}
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
	// The header line doubles as the /status title, with the version on it.
	fmt.Fprintf(&b, "%s · %s\n", tgHeader(), html.EscapeString(Version))
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

	ctl := currentAwake()
	fmt.Fprintf(&b, "\n%s: %s", tgText("st_awake"), tgAwakeStatus(ctl.View(), ctl.now()))
	// A desktop has no battery, and no line for one.
	if bat := battery.info(); bat.Present {
		fmt.Fprintf(&b, "\n%s: %s", tgText("st_battery"), tgBatteryStatus(bat))
	}
	if line := tgActivityLine(stActivityStatus(getConfig())); line != "" {
		b.WriteString("\n" + line)
	}
	if cfg := getConfig(); cfg.Media.Enabled {
		if line := tgMediaLine(stMediaStatus(cfg), cfg.Media.NowPlaying); line != "" {
			b.WriteString("\n" + line)
		}
	}

	if bus := currentBus(); bus != nil {
		if until := bus.MutedUntil(); !until.IsZero() {
			fmt.Fprintf(&b, "\n%s: %s", tgText("st_muted"), tgText("st_until", until.Format("15:04")))
		}
	}
	return b.String()
}

// tgActivityLine is the /status "활동: 게임 중 · Steam" line, or "" while
// the option is off or nothing watched is running — an idle PC needs no
// line saying so. Labels are the user's own words, so they are escaped.
func tgActivityLine(a stActivity) string {
	if !a.Enabled || a.Kind == activityKindNone {
		return ""
	}
	line := fmt.Sprintf("%s: %s", tgText("st_activity"), tgText("activity_kind_"+a.Kind))
	if len(a.Labels) > 0 {
		line += " · " + html.EscapeString(strings.Join(a.Labels, ", "))
	}
	return line
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

// Telegram hands long polling to one client per bot token, so a second PC
// sharing the token gets 409 for every getUpdates (#75). The poller reports
// the state here; /api/telegram/state and the GUI notify tab show it.
var (
	tgConflictMu          sync.Mutex
	telegramConflict      bool
	telegramConflictSince time.Time
)

// setTelegramConflict records a 409 state change from the poller. Since is
// the moment the conflict started and survives repeated true calls.
func setTelegramConflict(active bool) {
	tgConflictMu.Lock()
	defer tgConflictMu.Unlock()
	if active == telegramConflict {
		return
	}
	telegramConflict = active
	if active {
		telegramConflictSince = time.Now()
		logMsg("Telegram control: another PC is polling this bot; use a separate bot per PC or the hub agent")
	} else {
		telegramConflictSince = time.Time{}
	}
}

// telegramConflictState is what /api/telegram/state reports.
func telegramConflictState() (bool, time.Time) {
	tgConflictMu.Lock()
	defer tgConflictMu.Unlock()
	return telegramConflict, telegramConflictSince
}

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
		{Command: "screenon", Description: pick("화면 켜기", "Screen on")},
		{Command: "sleep", Description: pick("절전 [분]", "Sleep [minutes]")},
		{Command: "hibernate", Description: pick("최대 절전 [분]", "Hibernate [minutes]")},
		{Command: "restart", Description: pick("재시작 [분]", "Restart [minutes]")},
		{Command: "shutdown", Description: pick("종료 [분]", "Shut down [minutes]")},
		{Command: "cancel", Description: pick("예약·유예 취소", "Cancel schedule")},
		{Command: "now", Description: pick("예약·유예 즉시 실행", "Run schedule now")},
		{Command: "say", Description: pick("PC에 알림 띄우기", "Show a notification on the PC")},
		{Command: "presets", Description: pick("프리셋 목록", "List presets")},
		{Command: "run", Description: pick("프리셋 실행 (이름 또는 번호)", "Run a preset (name or number)")},
		{Command: "awake", Description: pick("잠들지 않기 [분|off]", "Keep awake [minutes|off]")},
		{Command: "vol", Description: pick("PC 볼륨 [0-100|+n|-n]", "PC volume [0-100|+n|-n]")},
		{Command: "mute", Description: pick("PC 음소거", "Mute the PC")},
		{Command: "unmute", Description: pick("PC 음소거 해제", "Unmute the PC")},
		{Command: "play", Description: pick("재생", "Play")},
		{Command: "pause", Description: pick("일시정지", "Pause")},
		{Command: "next", Description: pick("다음 곡", "Next track")},
		{Command: "prev", Description: pick("이전 곡", "Previous track")},
		{Command: "stop", Description: pick("정지", "Stop")},
		{Command: "np", Description: pick("지금 재생 중", "Now playing")},
		{Command: "quiet", Description: pick("알림 일시 중지 (30m, 2h, off)", "Pause notifications (30m, 2h, off)")},
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
		OnConflict:     setTelegramConflict,
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
	// No poller, no conflict — the warning must not outlive it even if the
	// goroutine was still sleeping out its 409 back-off.
	setTelegramConflict(false)
}

// currentBus returns the notification bus or nil (before startNotifier).
func currentBus() *notify.Bus {
	busMu.RLock()
	defer busMu.RUnlock()
	return bus
}
