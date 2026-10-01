package service

// Telegram /presets and /run 이름|번호 (#109). /run goes through runPreset
// like the SmartThings preset command; /presets lists slots, names and
// types only — what a preset runs never leaves the PC.

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// tgPresetTexts are merged into tgTexts at start-up; ko then en.
var tgPresetTexts = map[string][2]string{
	"presets_title":     {"<b>프리셋</b>", "<b>Presets</b>"},
	"presets_none":      {"등록된 프리셋이 없습니다. PC 앱의 프리셋 탭에서 추가하세요.", "No presets yet. Add them on the PC app's Presets tab."},
	"presets_hint":      {"실행: <code>/run 번호</code> 또는 <code>/run 이름</code>", "Run one: <code>/run number</code> or <code>/run name</code>"},
	"run_usage":         {"사용법: <code>/run 이름</code> 또는 <code>/run 번호</code> — 목록은 /presets", "Usage: <code>/run name</code> or <code>/run number</code> — see /presets"},
	"run_no_such":       {"그런 프리셋이 없습니다: <code>%s</code> — 목록은 /presets", "No such preset: <code>%s</code> — see /presets"},
	"run_started":       {"▶️ 프리셋 실행: <b>%d · %s</b>", "▶️ Preset started: <b>%d · %s</b>"},
	"type_program":      {"프로그램", "program"},
	"type_url":          {"URL", "URL"},
	"type_script":       {"스크립트", "script"},
	"preset_list_entry": {"%d · %s <i>(%s)</i>", "%d · %s <i>(%s)</i>"},
}

func init() {
	for k, v := range tgPresetTexts {
		tgTexts[k] = v
	}
}

// tgPresetList is the /presets reply: "1 · 게임 모드 (프로그램)" lines.
// Paths and arguments are not shown — the chat is a remote, not the PC.
func tgPresetList() string {
	ps := config.NormalizePresets(getConfig().Presets)
	if len(ps) == 0 {
		return tgText("presets_none")
	}
	var b strings.Builder
	b.WriteString(tgText("presets_title"))
	for _, p := range ps {
		b.WriteString("\n")
		b.WriteString(tgText("preset_list_entry", p.Slot, html.EscapeString(p.Name), tgText("type_"+p.Type)))
	}
	b.WriteString("\n\n")
	b.WriteString(tgText("presets_hint"))
	return b.String()
}

// runPreset handles /run 이름|번호.
func (telegramControl) runPreset(args []string) (string, *telegram.InlineKeyboard, error) {
	arg := strings.TrimSpace(tgJoinText(args))
	if arg == "" {
		return tgText("run_usage"), nil, nil
	}
	p, ok := config.FindPresetByName(getConfig().Presets, arg)
	if !ok {
		return tgText("run_no_such", html.EscapeString(httpx.Truncate(arg, 64))), nil, fmt.Errorf("no preset %q", httpx.Truncate(arg, 64))
	}
	ctx, cancel := context.WithTimeout(context.Background(), userActions.Timeout+time.Second)
	defer cancel()
	err := runPreset(ctx, p, "telegram")
	notePresetCommand(p, "telegram", "telegram", action.ResultCode(err))
	if err != nil {
		return tgActionError(err), nil, err
	}
	return tgText("run_started", p.Slot, html.EscapeString(p.Name)), nil, nil
}
