package tgcontrol

import (
	"context"
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// This file is the service side of inbound Telegram control (design doc §9):
// Control implements telegram.CommandHandler, and the lifecycle functions
// at the bottom run the telegram.Poller while control is enabled. Message
// edits after a button press keep the original text and append a result
// line (#62); the grace-message lifecycle lives in grace.go.

// telegramAliases maps slash-command names to catalogue commands.
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

// commandName resolves arg to a catalogue command the bot may run at all;
// the power commands need a confirmation (or a delay), see isGrace.
func (c *Control) commandName(arg string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(arg))
	if alias, ok := telegramAliases[name]; ok {
		name = alias
	}
	if !telegramSafeCommands[name] && !c.isGrace(name) {
		return "", false
	}
	if !c.d.Commands.Known(name) {
		return "", false
	}
	return name, true
}

// isGrace reports whether name is one of the power commands the grace
// period defers (shutdown, restart, suspend, hibernate).
func (c *Control) isGrace(name string) bool {
	return slices.Contains(c.d.Commands.GraceCommands(), name)
}

// ---- texts ----------------------------------------------------------------

// texts holds every reply string, ko then en. Values are Telegram HTML;
// %-verbs are filled by text. Dynamic values are escaped by the callers.
var texts = map[string][2]string{
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
	// "🖥 <b>name</b>" header from WithHeader (#75).
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
	"np_none":          {"재생 중인 미디어 없음", "Nothing is playing"},
	"np_playing":       {"재생 중", "Playing"},
	"np_paused":        {"일시정지", "Paused"},
	"np_stopped":       {"정지", "Stopped"},
	"np_private":       {"(재생 정보 공유가 꺼져 있습니다)", "(sharing what is playing is turned off)"},
	"st_media":         {"미디어", "Media"},
	"media_done_play":  {"▶ 재생했습니다", "▶ Playing"},
	"media_done_pause": {"⏸ 일시정지했습니다", "⏸ Paused"},
	"media_done_stop":  {"⏹ 정지했습니다", "⏹ Stopped"},
	"media_done_next":  {"⏭ 다음 곡으로 넘겼습니다", "⏭ Skipped to the next track"},
	"media_done_prev":  {"⏮ 이전 곡으로 돌아갔습니다", "⏮ Back to the previous track"},
	"quiet_still":      {"🔕 알림은 %s까지 일시 중지 중입니다 — <code>/quiet off</code>로 재개", "🔕 Notifications stay paused until %s — <code>/quiet off</code> resumes them"},
	// battery (#112)
	"st_battery":          {"배터리", "Battery"},
	"st_battery_charging": {"충전 중", "charging"},
	"st_battery_ac":       {"전원 연결됨", "plugged in"},
	"st_battery_unknown":  {"잔량 알 수 없음", "level unknown"},
	// running-app detection (#110, #123), "활동: Steam 실행 중 · 외 1개"
	"st_activity":      {"활동", "Activity"},
	"activity_running": {"%s 실행 중", "%s running"},
	"activity_more":    {"외 %d개", "%d more"},
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

// text returns the ko/en string for key (telegram.lang), formatted with
// args when given.
func (c *Control) text(key string, args ...any) string {
	pair, ok := texts[key]
	if !ok {
		return key
	}
	s := pair[0]
	if c.d.Config().Telegram.Lang == "en" {
		s = pair[1]
	}
	if len(args) > 0 {
		s = fmt.Sprintf(s, args...)
	}
	return s
}

// Text is the bot's wording for key in the configured language, formatted
// with args when given.
func (c *Control) Text(key string, args ...any) string { return c.text(key, args...) }

// commandLabel names a Commands key for humans; unknown keys are
// escaped verbatim.
func (c *Control) commandLabel(name string) string {
	if _, ok := texts["cmd_"+name]; ok {
		return c.text("cmd_" + name)
	}
	return html.EscapeString(name)
}

func (c *Control) originLabel(origin string) string {
	if _, ok := texts["origin_"+origin]; ok {
		return c.text("origin_" + origin)
	}
	return html.EscapeString(origin)
}

// stamp is the result line an edited message ends with when a Telegram
// button or command did the work: "✅ 실행됨 · 14:32 · 텔레그램".
func (c *Control) stamp(key string) string {
	return c.stampBy(key, "telegram")
}

// stampBy is stamp for any origin: "✅ 취소됨 · 14:32 · 트레이". An empty
// by (a replaced schedule) leaves the origin off.
func (c *Control) stampBy(key, by string) string {
	line := c.text(key) + " · " + time.Now().Format("15:04")
	if by != "" {
		line += " · " + c.byLabel(by)
	}
	return line
}

// byLabel names a cancel/run origin (toast, tray, app, webui, api,
// telegram, timer); unknown values are escaped verbatim.
func (c *Control) byLabel(by string) string {
	if _, ok := texts["by_"+by]; ok {
		return c.text("by_" + by)
	}
	return html.EscapeString(by)
}

// Plain is what Telegram hands back as message.text for the HTML h: tags
// stripped, entities decoded. Used to recognise the bot's own messages.
func Plain(h string) string {
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

// keep returns the HTML to keep when editing a message whose plain text
// is msgText: the matching candidate when the text is one of the bot's own
// (formatting preserved), otherwise msgText escaped. "" stays "".
func keep(msgText string, candidates ...string) string {
	if msgText == "" {
		return ""
	}
	for _, c := range candidates {
		if Plain(c) == msgText {
			return c
		}
	}
	return html.EscapeString(msgText)
}

// promptCandidates are every confirmation prompt the bot can have sent,
// on its own (/shutdown) or appended to the /menu (confirm:<cmd>). Both the
// headered form (#75) and the bare one are listed so a prompt sent by an
// older version is still recognised when its button is pressed.
func (c *Control) promptCandidates() []string {
	menu := c.menuTitle()
	var out []string
	for _, name := range c.d.Commands.GraceCommands() {
		q := c.text("confirm_q", c.commandLabel(name))
		out = append(out, c.WithHeader(q), q, menu+"\n\n"+q)
	}
	return out
}

// PCName is the name the bot's messages carry: telegram.pc_name, else the
// hostname.
func (c *Control) PCName() string {
	if n := c.d.Config().Telegram.PCName; n != "" {
		return n
	}
	return c.d.Status.Hostname()
}

// Header is the "🖥 <b>name</b>" line every message from this PC starts
// with (#75, hub-agent doc §1).
func (c *Control) Header() string { return telegram.Header(c.PCName()) }

// WithHeader prefixes c.Header() to a reply that does not already carry
// one — including text read back from Telegram, where the tags are gone but
// the 🖥 icon is not.
func (c *Control) WithHeader(h string) string { return telegram.WithHeader(c.PCName(), h) }

// ---- keyboards --------------------------------------------------------------

func (c *Control) button(textKey, data string) telegram.InlineButton {
	return telegram.InlineButton{Text: c.text(textKey), CallbackData: data}
}

// confirmKeyboard is [확인](exec:<cmd>) [취소](dismiss:).
func (c *Control) confirmKeyboard(name string) *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{InlineKeyboard: [][]telegram.InlineButton{{
		c.button("btn_confirm", "exec:"+name),
		c.button("btn_cancel", "dismiss:"),
	}}}
}

// menuKeyboard is the /menu layout: safe commands run at once (cmd:),
// power commands go through confirm:, and the last row cancels a schedule.
// cancel:menu (rather than the grace message's bare cancel:) tells
// HandleCallback the press came from the menu, which stays usable.
func (c *Control) menuKeyboard() *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{InlineKeyboard: [][]telegram.InlineButton{
		{c.button("btn_lock", "cmd:lock"), c.button("btn_screenoff", "cmd:turnscreenoff")},
		{c.button("btn_sleep", "confirm:suspend"), c.button("btn_restart", "confirm:restart")},
		{c.button("btn_shutdown", "confirm:shutdown"), c.button("btn_cancel_sch", "cancel:menu")},
	}}
}

// ---- actions ------------------------------------------------------------------

// runCommand executes a catalogue command in the background, at once: the
// bot asked for a confirmation (or a delay) already. It is neither
// recorded as last_command nor notified.
func (c *Control) runCommand(name string) bool {
	return c.d.Commands.RunNow(name)
}

// runScheduledNow ends the active schedule (by=telegram) and executes its
// command immediately; ok is false when nothing was scheduled.
func (c *Control) runScheduledNow() (string, bool) {
	name, ok := c.d.Commands.TakeForRun("telegram")
	if !ok {
		return "", false
	}
	logx.Printf("Telegram: running scheduled %s now", name)
	c.runCommand(name)
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

// HandleCommand answers a slash command from an allowed chat. Every reply
// carries the PC-name header (#75); the work is done by handleCommand.
func (c *Control) HandleCommand(ctx context.Context, chatID string, cmd string, args []string) (string, *telegram.InlineKeyboard, error) {
	h, kb, err := c.handleCommand(ctx, chatID, cmd, args)
	return c.WithHeader(h), kb, err
}

func (c *Control) handleCommand(ctx context.Context, chatID string, cmd string, args []string) (string, *telegram.InlineKeyboard, error) {
	switch cmd {
	case "help", "start":
		return c.text("help"), nil, nil
	case telegram.StaleCommand:
		// Sent while the PC was asleep / offline; the poller already logged it.
		text := ""
		if len(args) > 0 {
			text = args[0]
		}
		return c.text("stale", html.EscapeString(text)), nil, nil
	case "status":
		return c.statusText(), nil, nil
	case "menu":
		return c.menuTitle(), c.menuKeyboard(), nil
	case "lock", "screenoff", "screenon":
		name := telegramAliases[cmd]
		c.runCommand(name)
		return c.text("executed", c.commandLabel(name)), nil, nil
	case "sleep", "hibernate", "restart", "shutdown":
		return c.powerCommand(telegramAliases[cmd], args)
	case "cancel":
		if name, ok := c.d.Commands.Take("telegram"); ok {
			return c.text("cancelled", c.commandLabel(name)), nil, nil
		}
		return c.text("no_schedule"), nil, nil
	case "now":
		if name, ok := c.runScheduledNow(); ok {
			return c.text("run_now", c.commandLabel(name)), nil, nil
		}
		return c.text("no_schedule"), nil, nil
	case "awake":
		return c.awake(args)
	case "vol":
		return c.volume(ctx, args)
	case "say":
		return c.say(chatID, args)
	case "presets":
		return c.presetList(), nil, nil
	case "run":
		return c.runPreset(args)
	case "mute":
		// /mute 30m is what paused notifications before #104; a duration
		// still does, a bare /mute mutes the PC.
		if len(args) > 0 {
			return c.mute(args)
		}
		return c.mediaCommand(ctx, "mute", nil)
	case "unmute":
		return c.mediaCommand(ctx, "unmute", nil)
	case "play", "pause", "stop", "next", "prev":
		return c.mediaCommand(ctx, cmd, nil)
	case "np":
		return c.nowPlaying(ctx)
	case "quiet":
		if len(args) > 0 && strings.EqualFold(args[0], "off") {
			b := c.d.Bus()
			if b == nil {
				return c.text("notify_off"), nil, nil
			}
			b.Unmute()
			logx.Printf("Telegram: notifications unmuted")
			return c.text("unmuted"), nil, nil
		}
		return c.mute(args)
	}
	return c.text("unknown_command", html.EscapeString("/"+cmd)) + "\n\n" + c.text("help"), nil, nil
}

// powerCommand handles /sleep /hibernate /restart /shutdown [minutes]:
// without minutes it asks for confirmation, with minutes it schedules.
func (c *Control) powerCommand(name string, args []string) (string, *telegram.InlineKeyboard, error) {
	label := c.commandLabel(name)
	if len(args) == 0 {
		return c.text("confirm_q", label), c.confirmKeyboard(name), nil
	}
	minutes, err := strconv.Atoi(args[0])
	// #89: up to three days, the same ceiling /st/v1 and the app carry.
	if err != nil || minutes < 1 || minutes > power.MaxScheduleMinutes {
		return c.text("bad_minutes"), nil, fmt.Errorf("invalid minutes %q", args[0])
	}
	delay := time.Duration(minutes) * time.Minute
	if err := c.d.Commands.Schedule(name, delay); err != nil {
		return c.text("schedule_failed", html.EscapeString(err.Error())), nil, err
	}
	return c.text("scheduled", label, c.delay(delay), time.Now().Add(delay).Format("15:04")), nil, nil
}

// delay names a schedule delay in the bot's language: "30분", "2시간",
// "1시간 30분", "1일 3시간" — and formatDelay's "1 d 3 h" in English (#89).
// "4320분 후" is not a sentence anyone reads as three days.
func (c *Control) delay(d time.Duration) string {
	if c.d.Config().Telegram.Lang == "en" {
		return power.FormatDelay(d)
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
// most config.AwakeMaxMinutes).
func (c *Control) parseAwakeArg(args []string) (minutes int, off bool, ok bool) {
	if len(args) == 0 {
		return c.d.Config().Awake.Period(nil), false, true
	}
	arg := strings.ToLower(strings.TrimSpace(args[0]))
	if arg == "off" {
		return 0, true, true
	}
	n, err := strconv.Atoi(arg)
	if err != nil || !config.ValidAwakeMinutes(n) {
		return 0, false, false
	}
	return n, false, true
}

// awake handles /awake [minutes|off] (#111).
func (c *Control) awake(args []string) (string, *telegram.InlineKeyboard, error) {
	minutes, off, ok := c.parseAwakeArg(args)
	if !ok {
		return c.text("awake_usage"), nil, fmt.Errorf("invalid /awake argument %q", args[0])
	}
	ctl := c.d.Awake
	if off {
		_, wasOn, err := ctl.TurnOff()
		if err != nil {
			return c.text("awake_failed", html.EscapeString(err.Error())), nil, err
		}
		if !wasOn {
			return c.text("awake_already_off"), nil, nil
		}
		logx.Printf("Telegram: keep-awake off")
		return c.text("awake_off"), nil, nil
	}
	v, err := ctl.TurnOn(minutes)
	if err != nil {
		return c.text("awake_failed", html.EscapeString(err.Error())), nil, err
	}
	logx.Printf("Telegram: keep-awake on (%d min)", minutes)
	return c.text("awake_on", c.awakeSpan(v, ctl.Now())), nil, nil
}

// awakeSpan is how long keep-awake lasts: "14:30까지", "내일 09:10까지"
// (a period reaches a day at most) or "끌 때까지".
func (c *Control) awakeSpan(v status.AwakeView, now time.Time) string {
	if v.Until.IsZero() {
		return c.text("st_awake_forever")
	}
	at := v.Until.Format("15:04")
	if y, m, d := v.Until.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		at = c.text("st_tomorrow", at)
	}
	return c.text("st_until", at)
}

// awakeStatus is the /status value: "켜짐 · 14:30까지" or "꺼짐".
func (c *Control) awakeStatus(v status.AwakeView, now time.Time) string {
	if !v.On {
		return c.text("st_off")
	}
	return c.text("st_on") + " · " + c.awakeSpan(v, now)
}

// batteryStatus is the /status value: "80% · 충전 중", "100% · 전원 연결됨"
// or, on battery, just "80%".
func (c *Control) batteryStatus(b status.Battery) string {
	level := c.text("st_battery_unknown")
	if b.Percent >= 0 {
		level = strconv.Itoa(b.Percent) + "%"
	}
	switch {
	case b.Charging:
		return level + " · " + c.text("st_battery_charging")
	case b.AC:
		return level + " · " + c.text("st_battery_ac")
	}
	return level
}

func (c *Control) mute(args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) == 0 {
		return c.text("mute_usage"), nil, nil
	}
	d, ok := parseMuteDuration(args[0])
	if !ok {
		return c.text("mute_usage"), nil, fmt.Errorf("invalid mute duration %q", args[0])
	}
	b := c.d.Bus()
	if b == nil {
		return c.text("notify_off"), nil, nil
	}
	b.Mute(d)
	logx.Printf("Telegram: notifications muted for %s", d)
	return c.text("muted", time.Now().Add(d).Format("15:04")), nil, nil
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
func (c *Control) HandleCallback(ctx context.Context, chatID string, msgID int, msgText string, data string) (string, string, error) {
	h, toast, err := c.handleCallback(ctx, chatID, msgID, msgText, data)
	return c.WithHeader(h), toast, err
}

func (c *Control) handleCallback(_ context.Context, chatID string, msgID int, msgText string, data string) (string, string, error) {
	verb, arg, _ := strings.Cut(data, ":")
	switch verb {
	case "exec":
		name, ok := c.commandName(arg)
		if !ok {
			return "", c.text("unknown_button"), fmt.Errorf("callback %q: unknown command", data)
		}
		c.runCommand(name)
		return appendStamp(keep(msgText, c.promptCandidates()...), c.commandLabel(name), c.stamp("stamp_executed")), c.text("toast_executed"), nil
	case "cmd":
		name, ok := c.commandName(arg)
		if !ok {
			return "", c.text("unknown_button"), fmt.Errorf("callback %q: unknown command", data)
		}
		if !telegramSafeCommands[name] {
			return "", c.text("confirm_needed"), nil
		}
		c.runCommand(name)
		return appendStamp(keep(msgText, c.menuTitle()), c.commandLabel(name), c.stamp("stamp_executed")), c.text("toast_executed"), nil
	case "confirm":
		name, ok := c.commandName(arg)
		if !ok || !c.isGrace(name) {
			return "", c.text("unknown_button"), fmt.Errorf("callback %q: not a power command", data)
		}
		q := c.text("confirm_q", c.commandLabel(name))
		if kept := keep(msgText, c.menuTitle()); kept != "" {
			return kept + "\n\n" + q, "", nil
		}
		return q, "", nil
	case "dismiss":
		return appendStamp(keep(msgText, c.promptCandidates()...), "", c.stamp("stamp_dismissed")), "", nil
	case "cancel":
		if arg == "menu" {
			name, ok := c.d.Commands.Take("telegram")
			if !ok {
				return "", c.text("no_schedule"), nil
			}
			return c.menuTitle() + "\n" + c.text("cancelled", c.commandLabel(name)), c.text("toast_cancelled"), nil
		}
		grace, mine := c.takeGraceMessage(chatID, msgID)
		name, ok := c.d.Commands.Take("telegram")
		if !ok {
			return c.stale(msgText), c.text("no_schedule"), nil
		}
		if mine {
			return grace.html + "\n" + c.stamp("stamp_cancelled"), c.text("toast_cancelled"), nil
		}
		return appendStamp(keep(msgText), c.commandLabel(name), c.stamp("stamp_cancelled")), c.text("toast_cancelled"), nil
	case "runnow":
		grace, mine := c.takeGraceMessage(chatID, msgID)
		name, ok := c.runScheduledNow()
		if !ok {
			return c.stale(msgText), c.text("no_schedule"), nil
		}
		if mine {
			return grace.html + "\n" + c.stamp("stamp_ran"), c.text("toast_executed"), nil
		}
		return appendStamp(keep(msgText), c.commandLabel(name), c.stamp("stamp_ran")), c.text("toast_executed"), nil
	}
	return "", c.text("unknown_button"), fmt.Errorf("unknown callback %q", data)
}

// menuTitle is the /menu text, header included, so keep recognises it
// in the plain text Telegram hands back on a button press.
func (c *Control) menuTitle() string {
	return c.WithHeader(c.text("menu_title"))
}

// appendStamp builds an edit: the kept text (when any) followed by the result
// line. Without kept text the command label heads the message so the
// result is still readable on its own.
func appendStamp(kept, label, stamp string) string {
	if kept != "" {
		return kept + "\n" + stamp
	}
	if label != "" {
		return label + "\n" + stamp
	}
	return stamp
}

// stale is the edit for a cancel:/runnow: press whose schedule no longer
// exists: the text stays, the buttons go, "⏹ 이미 처리됨" is appended. When
// the poller had no text (no message), nothing is edited.
func (c *Control) stale(msgText string) string {
	if msgText == "" {
		return ""
	}
	return keep(msgText) + "\n" + c.text("stamp_stale")
}

// EditKeyboard implements telegram.EditKeyboarder: a confirm:<cmd> edit
// keeps [확인][취소] buttons, cancel:menu keeps the menu, every other edit
// drops the keyboard.
func (c *Control) EditKeyboard(data string) *telegram.InlineKeyboard {
	verb, arg, _ := strings.Cut(data, ":")
	switch verb {
	case "confirm":
		if name, ok := c.commandName(arg); ok && c.isGrace(name) {
			return c.confirmKeyboard(name)
		}
	case "cancel":
		if arg == "menu" {
			return c.menuKeyboard()
		}
	}
	return nil
}

// Unauthorized raises security.unknown_chat for traffic from other chats.
func (c *Control) Unauthorized(chatID, username, text string) {
	logx.Printf("Telegram: ignored message from unknown chat %s (%s): %s", chatID, username, httpx.Truncate(text, 64))
	c.d.Emit("security", "unknown_chat", map[string]string{
		"chat_id":  chatID,
		"username": httpx.Truncate(username, 64),
		"text":     httpx.Truncate(text, 64),
	})
}

// statusText builds the /status reply.
func (c *Control) statusText() string {
	var b strings.Builder
	// The header line doubles as the /status title, with the version on it.
	fmt.Fprintf(&b, "%s · %s\n", c.Header(), html.EscapeString(c.d.Version()))
	fmt.Fprintf(&b, "%s: %s\n", c.text("st_uptime"), formatUptime(time.Since(c.d.StartedAt)))

	s := c.d.Status.Schedule()
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
		fmt.Fprintf(&b, "%s: %s\n", c.text("st_schedule"),
			c.text("st_remaining", c.commandLabel(command), c.originLabel(origin), formatUptime(time.Duration(remaining)*time.Second), at))
	} else {
		fmt.Fprintf(&b, "%s: %s\n", c.text("st_schedule"), c.text("st_none"))
	}

	if lr := c.d.Status.LastRemote(); lr.Command != "" {
		fmt.Fprintf(&b, "%s: %s · <code>%s</code> · %s", c.text("st_remote"),
			c.commandLabel(lr.Command), html.EscapeString(lr.From), lr.At.Format("15:04:05"))
	} else {
		fmt.Fprintf(&b, "%s: %s", c.text("st_remote"), c.text("st_none"))
	}

	ctl := c.d.Awake
	fmt.Fprintf(&b, "\n%s: %s", c.text("st_awake"), c.awakeStatus(ctl.View(), ctl.Now()))
	// A desktop has no battery, and no line for one.
	if bat := c.d.Status.Battery(); bat.Present {
		fmt.Fprintf(&b, "\n%s: %s", c.text("st_battery"), c.batteryStatus(bat))
	}
	if line := c.activityLine(c.d.Status.Activity(c.d.Config())); line != "" {
		b.WriteString("\n" + line)
	}
	if cfg := c.d.Config(); cfg.Media.Enabled {
		if line := c.mediaLine(c.d.Status.Media(cfg), cfg.Media.NowPlaying); line != "" {
			b.WriteString("\n" + line)
		}
	}

	if bus := c.d.Bus(); bus != nil {
		if until := bus.MutedUntil(); !until.IsZero() {
			fmt.Fprintf(&b, "\n%s: %s", c.text("st_muted"), c.text("st_until", until.Format("15:04")))
		}
	}
	return b.String()
}

// activityLine is the /status "활동: Steam 실행 중 · 외 1개" line: the
// highest-priority running app and how many other watched apps run. It is
// "" while the option is off or nothing watched is running — an idle PC
// needs no line saying so. Labels are the user's own words, so they are
// escaped.
func (c *Control) activityLine(a status.Activity) string {
	label, others := a.TopLabel()
	if !a.Enabled || label == "" {
		return ""
	}
	line := fmt.Sprintf("%s: %s", c.text("st_activity"), c.text("activity_running", html.EscapeString(label)))
	if others > 0 {
		line += " · " + c.text("activity_more", others)
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

// runner owns the running poller. managed is set between Start and Stop so
// that a config save in the installer or in tests never spins up a poller.
type runner struct {
	mu      sync.Mutex
	managed bool
	key     string // settings the running poller was built from
	cancel  context.CancelFunc
	done    chan struct{}
}

// conflict is the 409 state: Telegram hands long polling to one client per
// bot token, so a second PC sharing the token gets 409 for every
// getUpdates (#75). The poller reports the state here; /api/telegram/state
// and the app's notify tab show it.
type conflict struct {
	mu     sync.Mutex
	active bool
	since  time.Time
}

// SetConflict records a 409 state change from the poller. Since is the
// moment the conflict started and survives repeated true calls.
func (c *Control) SetConflict(active bool) {
	c.conflict.mu.Lock()
	defer c.conflict.mu.Unlock()
	if active == c.conflict.active {
		return
	}
	c.conflict.active = active
	if active {
		c.conflict.since = time.Now()
		logx.Printf("Telegram control: another PC is polling this bot; use a separate bot per PC or the hub agent")
	} else {
		c.conflict.since = time.Time{}
	}
}

// Conflict is what /api/telegram/state reports.
func (c *Control) Conflict() (bool, time.Time) {
	c.conflict.mu.Lock()
	defer c.conflict.mu.Unlock()
	return c.conflict.active, c.conflict.since
}

// Start enables the lifecycle and starts polling when the current config
// asks for it. Called once at service start.
func (c *Control) Start() {
	c.runner.mu.Lock()
	c.runner.managed = true
	c.runner.mu.Unlock()
	c.Reconcile()
}

// Stop stops the poller (if any) and disables the lifecycle.
func (c *Control) Stop() {
	c.runner.mu.Lock()
	defer c.runner.mu.Unlock()
	c.runner.managed = false
	c.stopLocked()
}

// Reconcile (re)starts or stops the poller so it matches the live config.
// The service calls it after every config save; it is a no-op until Start
// has run.
func (c *Control) Reconcile() {
	c.runner.mu.Lock()
	defer c.runner.mu.Unlock()
	if !c.runner.managed {
		return
	}
	cfg := c.d.Config().Telegram
	key := controlKey(cfg)
	if key == c.runner.key {
		return
	}
	c.stopLocked()
	if key == "" {
		return
	}
	c.startLocked(cfg, key)
}

// Running reports whether a poller goroutine is alive.
func (c *Control) Running() bool {
	c.runner.mu.Lock()
	defer c.runner.mu.Unlock()
	return c.runner.done != nil
}

// controlKey summarises the settings that require a poller restart;
// "" means control must be off. AllowedChatIDs are read live, so they are
// not part of the key.
func controlKey(cfg config.TelegramConfig) string {
	if !cfg.Enabled || !cfg.ControlEnabled || cfg.BotToken == "" || cfg.ChatID == "" {
		return ""
	}
	return cfg.BotToken + "\x00" + cfg.ChatID + "\x00" + cfg.Lang
}

// allowedChatIDs is the poller's allow-list: allowed_chat_ids, or
// just chat_id when that list is empty.
func (c *Control) allowedChatIDs() []string {
	cfg := c.d.Config().Telegram
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

// botCommands is the setMyCommands menu (design doc §9).
func botCommands(lang string) []telegram.BotCommand {
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

// startLocked builds the client and runs the poller. runner.mu is held.
func (c *Control) startLocked(cfg config.TelegramConfig, key string) {
	token, err := secret.Unprotect(cfg.BotToken)
	if err != nil || token == "" {
		logx.Printf("Telegram control: cannot read bot token (%v); control stays off", err)
		return
	}
	cli := telegram.NewClient(token, telegram.WithBaseURL(c.d.BaseURL()))
	poller := telegram.NewPoller(cli, telegram.PollerOptions{
		AllowedChatIDs: c.allowedChatIDs,
		Handler:        c,
		Log:            logx.Printf,
		OnConflict:     c.SetConflict,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.runner.key, c.runner.cancel, c.runner.done = key, cancel, done
	lang := cfg.Lang
	go func() {
		defer close(done)
		if err := cli.SetMyCommands(ctx, botCommands(lang)); err != nil && ctx.Err() == nil {
			logx.Printf("Telegram control: setMyCommands failed: %v", err)
		}
		logx.Printf("Telegram control: polling started")
		if err := poller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logx.Printf("Telegram control: poller stopped: %v", err)
		}
		logx.Printf("Telegram control: polling stopped")
	}()
}

// stopLocked cancels the poller and waits briefly for it. runner.mu is held.
func (c *Control) stopLocked() {
	r := &c.runner
	if r.cancel == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		logx.Printf("Telegram control: poller did not stop in time")
	}
	r.key, r.cancel, r.done = "", nil, nil
	// No poller, no conflict — the warning must not outlive it even if the
	// goroutine was still sleeping out its 409 back-off.
	c.SetConflict(false)
}
