package tgcontrol

// Telegram /presets and /run 이름|번호 (#109). /run goes through Presets.Run
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
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// presetTexts are merged into texts at start-up; ko then en.
var presetTexts = map[string][2]string{
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
	// A failed /run names the file by its base name only (C6); the full
	// error is in service.log.
	"preset_not_found":     {"파일을 찾을 수 없음: <code>%s</code>", "File not found: <code>%s</code>"},
	"preset_access_denied": {"접근이 거부됨: <code>%s</code>", "Access denied: <code>%s</code>"},
	"preset_start_failed":  {"실행하지 못함: <code>%s</code>", "Could not start: <code>%s</code>"},
	"preset_url_failed":    {"URL을 열지 못함", "Could not open the URL"},
	"preset_bad":           {"저장된 설정으로는 실행할 수 없음: <code>%s</code>", "Cannot be run as saved: <code>%s</code>"},
	"preset_failed_detail": {"❌ 프리셋 %d · %s 실패: %s", "❌ Preset %d · %s failed: %s"},
}

func init() {
	for k, v := range presetTexts {
		texts[k] = v
	}
}

// presetList is the /presets reply: "1 · 게임 모드 (프로그램)" lines.
// Paths and arguments are not shown — the chat is a remote, not the PC.
func (c *Control) presetList() string {
	ps := config.NormalizePresets(c.d.Config().Presets)
	if len(ps) == 0 {
		return c.text("presets_none")
	}
	var b strings.Builder
	b.WriteString(c.text("presets_title"))
	for _, p := range ps {
		b.WriteString("\n")
		b.WriteString(c.text("preset_list_entry", p.Slot, html.EscapeString(p.Name), c.text("type_"+p.Type)))
	}
	b.WriteString("\n\n")
	b.WriteString(c.text("presets_hint"))
	return b.String()
}

// runPreset handles /run 이름|번호.
func (c *Control) runPreset(args []string) (string, *telegram.InlineKeyboard, error) {
	arg := strings.TrimSpace(joinText(args))
	if arg == "" {
		return c.text("run_usage"), nil, nil
	}
	p, ok := config.FindPresetByName(c.d.Config().Presets, arg)
	if !ok {
		return c.text("run_no_such", html.EscapeString(httpx.Truncate(arg, 64))), nil, fmt.Errorf("no preset %q", httpx.Truncate(arg, 64))
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.d.ActionTimeout()+time.Second)
	defer cancel()
	err := c.d.Presets.Run(ctx, p, "telegram")
	c.d.Presets.Record(p, "telegram", action.ResultCode(err))
	if err != nil {
		return c.presetError(err, p), nil, err
	}
	return c.text("run_started", p.Slot, html.EscapeString(p.Name)), nil, nil
}

// presetError words a failed /run without the preset's path, folder,
// arguments or URL (C6): a fixed text and the file's base name. Failures
// that are not about the file (nobody logged in, a timeout) keep their
// usual texts.
func (c *Control) presetError(err error, p config.Preset) string {
	f, reason := action.PresetFailure(err, p)
	name := html.EscapeString(useraction.PresetFileName(p.Path))
	var why string
	switch {
	case reason == useraction.PresetNotFound:
		why = c.text("preset_not_found", name)
	case reason == useraction.PresetAccessDenied:
		why = c.text("preset_access_denied", name)
	case reason == useraction.PresetURLFailed:
		why = c.text("preset_url_failed")
	case reason == useraction.PresetStartFailed:
		why = c.text("preset_start_failed", name)
	case f.Code == useraction.CodeBadArgs:
		why = c.text("preset_bad", name)
	default:
		// no_user_session, timeout, unsupported, an unreadable reply:
		// fixed texts that never quote the preset.
		return c.actionError(err)
	}
	return c.text("preset_failed_detail", p.Slot, html.EscapeString(p.Name), why)
}
