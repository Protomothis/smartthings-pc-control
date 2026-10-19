package tgcontrol

// Tests of the bot's wording and argument parsing, moved here with the
// code (#127). The command and button flows are tested against the real
// service in the root package.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

var stampTime = regexp.MustCompile(`^\d\d:\d\d$`)

var spotifyTrack = useraction.NowPlaying{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", Album: "New Jeans", App: "Spotify"}

func app(id, label string, running bool) status.ActivityApp {
	return status.ActivityApp{ID: id, Label: label, Running: running}
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

func TestTelegramAllowedChatIDsFallsBackToChatID(t *testing.T) {
	c := newTestControl(t, config.Config{})
	c.setConfig(config.Config{Telegram: config.TelegramConfig{ChatID: "42"}})
	if got := c.allowedChatIDs(); fmt.Sprint(got) != "[42]" {
		t.Errorf("allowed = %v", got)
	}
	c.setConfig(config.Config{Telegram: config.TelegramConfig{ChatID: "42", AllowedChatIDs: []string{" 7 ", "", "8"}}})
	if got := c.allowedChatIDs(); fmt.Sprint(got) != "[7 8]" {
		t.Errorf("allowed = %v", got)
	}
	c.setConfig(config.Config{Telegram: config.TelegramConfig{AllowedChatIDs: []string{" "}}})
	if got := c.allowedChatIDs(); len(got) != 0 {
		t.Errorf("allowed = %v, want none", got)
	}
}

func TestTelegramBotCommandsFollowLang(t *testing.T) {
	ko := botCommands("ko")
	en := botCommands("en")
	if len(ko) != 26 || len(en) != len(ko) {
		t.Fatalf("command count ko=%d en=%d", len(ko), len(en))
	}
	if ko[0].Command != "status" || ko[0].Description != "상태" || en[0].Description != "Status" {
		t.Errorf("status = %+v / %+v", ko[0], en[0])
	}
	if botCommands("")[0].Description != "상태" {
		t.Error("unknown lang should fall back to ko")
	}
}

func TestTelegramControlKey(t *testing.T) {
	on := config.TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "tok", ChatID: "42", Lang: "ko"}
	if controlKey(on) == "" {
		t.Error("fully configured control should be on")
	}
	for name, cfg := range map[string]config.TelegramConfig{
		"disabled":   {ControlEnabled: true, BotToken: "tok", ChatID: "42"},
		"no control": {Enabled: true, BotToken: "tok", ChatID: "42"},
		"no token":   {Enabled: true, ControlEnabled: true, ChatID: "42"},
		"no chat":    {Enabled: true, ControlEnabled: true, BotToken: "tok"},
	} {
		if controlKey(cfg) != "" {
			t.Errorf("%s: control should be off", name)
		}
	}
	changed := on
	changed.Lang = "en"
	if controlKey(changed) == controlKey(on) {
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

func TestTgPlainAndStampBy(t *testing.T) {
	c := newTestControl(t, config.Config{})
	if got := Plain("🖥 <b>MY&lt;PC&gt;</b>\n<code>a &amp; b</code>"); got != "🖥 MY<PC>\na & b" {
		t.Errorf("Plain = %q", got)
	}
	if got := keep("x<y"); got != "x&lt;y" {
		t.Errorf("keep escapes foreign text: %q", got)
	}
	if got := keep("", "<b>a</b>"); got != "" {
		t.Errorf("keep(\"\") = %q", got)
	}
	c.setConfig(config.Config{})
	for by, want := range map[string]string{"toast": "토스트", "tray": "트레이", "app": "앱", "webui": "WebUI", "api": "API", "telegram": "텔레그램", "smartthings": "SmartThings", "timer": "타이머", "x<y": "x&lt;y"} {
		if got := c.byLabel(by); got != want {
			t.Errorf("byLabel(%q) = %q, want %q", by, got, want)
		}
	}
	line := c.stampBy("stamp_replaced", "")
	if parts := strings.Split(line, " · "); len(parts) != 2 || parts[0] != "🔁 대체됨" || !stampTime.MatchString(parts[1]) {
		t.Errorf("stampBy without origin = %q", line)
	}
	c.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "en"}})
	if line := c.stampBy("stamp_ran", "timer"); !strings.HasPrefix(line, "▶️ Executed · ") || !strings.HasSuffix(line, " · timer") {
		t.Errorf("en stampBy = %q", line)
	}
}

func TestTelegramActivityLine(t *testing.T) {
	c := newTestControl(t, config.Config{})
	c.setConfig(config.Config{Port: 5001, Telegram: config.TelegramConfig{Lang: "ko"}})

	steam, obs, code := app("steam.exe", "Steam", true), app("obs64.exe", "OBS", true), app("code.exe", "<b>", true)
	for _, tc := range []struct {
		name string
		in   status.Activity
		want string
	}{
		{"off", status.Activity{Enabled: false, Apps: []status.ActivityApp{}}, ""},
		{"nothing running", status.Activity{Enabled: true, Apps: []status.ActivityApp{app("steam.exe", "Steam", false)}}, ""},
		{"one", status.Activity{Enabled: true, Top: "steam.exe", Apps: []status.ActivityApp{steam}}, "활동: Steam 실행 중"},
		{"two", status.Activity{Enabled: true, Top: "steam.exe", Apps: []status.ActivityApp{steam, obs}}, "활동: Steam 실행 중 · 외 1개"},
		{"top below a stopped one", status.Activity{Enabled: true, Top: "obs64.exe",
			Apps: []status.ActivityApp{app("steam.exe", "Steam", false), obs, steam}}, "활동: OBS 실행 중 · 외 1개"},
		{"escaped", status.Activity{Enabled: true, Top: "code.exe", Apps: []status.ActivityApp{code}}, "활동: &lt;b&gt; 실행 중"},
	} {
		if got := c.activityLine(tc.in); got != tc.want {
			t.Errorf("%s: activityLine = %q, want %q", tc.name, got, tc.want)
		}
	}
	c.setConfig(config.Config{Port: 5001, Telegram: config.TelegramConfig{Lang: "en"}})
	in := status.Activity{Enabled: true, Top: "steam.exe", Apps: []status.ActivityApp{steam, obs, code}}
	if got := c.activityLine(in); got != "Activity: Steam running · 2 more" {
		t.Errorf("en line = %q", got)
	}
}

func TestParseAwakeArg(t *testing.T) {
	c := newTestControl(t, config.Config{})

	c.setConfig(config.Config{Awake: config.AwakeConfig{DefaultMinutes: 90}})

	for _, tc := range []struct {
		args    []string
		minutes int
		off, ok bool
	}{
		{nil, 90, false, true},
		{[]string{"off"}, 0, true, true},
		{[]string{"OFF"}, 0, true, true},
		{[]string{"30"}, 30, false, true},
		{[]string{"0"}, 0, false, true},
		{[]string{"1440"}, 1440, false, true},
		{[]string{"1441"}, 0, false, false},
		{[]string{"-1"}, 0, false, false},
		{[]string{"2h"}, 0, false, false},
		{[]string{"on"}, 0, false, false},
	} {
		m, off, ok := c.parseAwakeArg(tc.args)
		if m != tc.minutes || off != tc.off || ok != tc.ok {
			t.Errorf("parseAwakeArg(%q) = %d,%v,%v want %d,%v,%v", tc.args, m, off, ok, tc.minutes, tc.off, tc.ok)
		}
	}
}

func TestParseVolArg(t *testing.T) {
	ok := map[string]struct {
		name  string
		value int
	}{
		"30": {"volume", 30}, "0": {"volume", 0}, "100": {"volume", 100}, "30%": {"volume", 30},
		"+10": {"volumeup", 10}, "-10": {"volumedown", 10}, "+1": {"volumeup", 1}, "-100": {"volumedown", 100},
	}
	for arg, want := range ok {
		name, value, good := parseVolArg(arg)
		if !good || name != want.name || value != want.value {
			t.Errorf("parseVolArg(%q) = %s %d %v, want %s %d", arg, name, value, good, want.name, want.value)
		}
	}
	for _, arg := range []string{"", "101", "+0", "-0", "+101", "loud", "1e2", "+", "3 0", "0x10", "1000"} {
		if _, _, good := parseVolArg(arg); good {
			t.Errorf("parseVolArg(%q) accepted", arg)
		}
	}
}

func TestTelegramAudioState(t *testing.T) {
	c := newTestControl(t, config.Config{})
	c.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	if got := c.audioState(useraction.Audio{Volume: 30, Device: "스피커"}); got != "볼륨 30% · 음소거 꺼짐 · 스피커" {
		t.Errorf("ko = %q", got)
	}
	if got := c.audioState(useraction.Audio{Volume: 0, Muted: true}); got != "볼륨 0% · 음소거 켜짐" {
		t.Errorf("ko without device = %q", got)
	}
	if got := c.audioState(useraction.Audio{Volume: 5, Device: "<HDMI>"}); !strings.HasSuffix(got, "&lt;HDMI&gt;") {
		t.Errorf("device not escaped: %q", got)
	}
	c.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "en"}})
	if got := c.audioState(useraction.Audio{Volume: 30, Device: "Speakers"}); got != "Volume 30% · mute off · Speakers" {
		t.Errorf("en = %q", got)
	}
}

func TestTelegramNowPlayingText(t *testing.T) {
	c := newTestControl(t, config.Config{})

	c.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	cases := []struct {
		np    useraction.NowPlaying
		share bool
		want  string
	}{
		{spotifyTrack, true, "▶ Hype Boy — NewJeans · Spotify"},
		{useraction.NowPlaying{Status: "paused", Title: "<b>Ditto</b>", App: "Chrome"}, true, "⏸ &lt;b&gt;Ditto&lt;/b&gt; · Chrome"},
		{useraction.NowPlaying{Status: "playing", App: "VLC"}, true, "▶ 재생 중 · VLC"},
		{useraction.NowPlaying{Status: "stopped"}, true, "⏹ 정지"},
		{spotifyTrack, false, "▶ 재생 중 (재생 정보 공유가 꺼져 있습니다)"},
		{useraction.NowPlaying{Status: "paused"}, false, "⏸ 일시정지 (재생 정보 공유가 꺼져 있습니다)"},
		{useraction.NowPlaying{Status: "none"}, true, "재생 중인 미디어 없음"},
		{useraction.NowPlaying{Status: "none"}, false, "재생 중인 미디어 없음"},
	}
	for _, tc := range cases {
		if got := c.nowPlayingText(tc.np, tc.share); got != tc.want {
			t.Errorf("nowPlayingText(%+v, %v) = %q, want %q", tc.np, tc.share, got, tc.want)
		}
	}
	c.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "en"}})
	if got := c.nowPlayingText(useraction.NowPlaying{Status: "none"}, true); got != "Nothing is playing" {
		t.Errorf("en none = %q", got)
	}
}

func TestTelegramMediaLine(t *testing.T) {
	c := newTestControl(t, config.Config{})

	c.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	m := status.Media{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", App: "Spotify"}
	if got := c.mediaLine(m, true); got != "미디어: ▶ Hype Boy — NewJeans · Spotify" {
		t.Errorf("line = %q", got)
	}
	if got := c.mediaLine(status.Media{Status: "playing"}, false); got != "미디어: ▶ 재생 중" {
		t.Errorf("opt-out line = %q", got)
	}
	for _, s := range []string{"paused", "stopped", "none"} {
		if got := c.mediaLine(status.Media{Status: s, Title: "x"}, true); got != "" {
			t.Errorf("%s line = %q, want none", s, got)
		}
	}
}

func TestTelegramHelpListsPresets(t *testing.T) {
	c := newTestControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	help, _, _ := c.HandleCommand(context.Background(), "42", "help", nil)
	for _, cmd := range []string{"/presets", "/run"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("help lacks %s", cmd)
		}
	}
	names := map[string]bool{}
	for _, bc := range botCommands("ko") {
		names[bc.Command] = true
	}
	if !names["presets"] || !names["run"] {
		t.Errorf("setMyCommands = %v", names)
	}
}

func TestTelegramHelpListsSay(t *testing.T) {
	c := newTestControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	help, _, _ := c.HandleCommand(context.Background(), "42", "help", nil)
	if !strings.Contains(help, "/say") {
		t.Error("help lacks /say")
	}
	for _, bc := range botCommands("ko") {
		if bc.Command == "say" {
			return
		}
	}
	t.Error("setMyCommands lacks say")
}
