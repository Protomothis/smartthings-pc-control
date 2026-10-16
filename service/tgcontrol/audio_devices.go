package tgcontrol

// Telegram /output and /input (#145): the device slots with a button per
// slot, and the switch itself.
//
//	/output        "출력 장치" list: ✓ on the default, "(연결 안 됨)" on a
//	               device that is not there now; [🔊 1 · 스피커] buttons
//	/output 3      switch to slot 3 at once
//	/input [n]     the same for the recording device
//
// A button press (out:<slot>, in:<slot>) switches and edits the message to
// the new list with the result under it; the buttons stay. Everything goes
// through Media.Run, the path SmartThings and the app take.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

// audioDeviceTexts are merged into texts at start-up; ko then en.
var audioDeviceTexts = map[string][2]string{
	"dev_title_out":    {"<b>출력 장치</b>", "<b>Output devices</b>"},
	"dev_title_in":     {"<b>입력 장치</b>", "<b>Input devices</b>"},
	"dev_none_out":     {"출력 장치 칸이 비어 있습니다. PC 앱 설정 탭(미디어·알림)에서 지정하세요.", "No output device slots yet. Fill them on the PC app's Settings tab (Media &amp; notifications)."},
	"dev_none_in":      {"입력 장치 칸이 비어 있습니다. PC 앱 설정 탭(미디어·알림)에서 지정하세요.", "No input device slots yet. Fill them on the PC app's Settings tab (Media &amp; notifications)."},
	"dev_entry":        {"%d · %s", "%d · %s"},
	"dev_missing":      {"(연결 안 됨)", "(not connected)"},
	"dev_hint_out":     {"버튼을 누르거나 <code>/output 번호</code>로 전환합니다", "Tap a button, or send <code>/output number</code>"},
	"dev_hint_in":      {"버튼을 누르거나 <code>/input 번호</code>로 전환합니다", "Tap a button, or send <code>/input number</code>"},
	"dev_usage_out":    {"사용법: <code>/output</code> (목록) 또는 <code>/output 1</code>~<code>5</code>", "Usage: <code>/output</code> (list) or <code>/output 1</code>–<code>5</code>"},
	"dev_usage_in":     {"사용법: <code>/input</code> (목록) 또는 <code>/input 1</code>~<code>5</code>", "Usage: <code>/input</code> (list) or <code>/input 1</code>–<code>5</code>"},
	"dev_switched_out": {"🔊 출력 전환: <b>%s</b>", "🔊 Output switched to <b>%s</b>"},
	"dev_switched_in":  {"🎙 입력 전환: <b>%s</b>", "🎙 Input switched to <b>%s</b>"},
	"dev_slot_empty":   {"%d번 칸이 비어 있습니다", "Slot %d is empty"},
	"dev_not_there":    {"<b>%s</b>이(가) 지금 연결되어 있지 않습니다", "<b>%s</b> is not connected right now"},
	"dev_switch_fail":  {"Windows가 기본 장치를 바꾸지 않았습니다: %s", "Windows did not switch the default device: %s"},
	"st_output":        {"출력", "Output"},
}

func init() {
	for k, v := range audioDeviceTexts {
		texts[k] = v
	}
}

// devCommand is the media command of kind: "out" or "in", which is also
// the callback verb and the text suffix.
func devCommand(kind string) string {
	if kind == "in" {
		return action.CommandAudioInput
	}
	return action.CommandAudioOutput
}

// devSlots is the stored slot list of kind (status audio block).
func (c *Control) devSlots(kind string) []status.AudioSlot {
	a := c.d.Status.Audio(c.d.Config())
	if kind == "in" {
		return a.Inputs
	}
	return a.Outputs
}

// audioDevices handles /output [n] and /input [n].
func (c *Control) audioDevices(ctx context.Context, kind string, args []string) (string, *telegram.InlineKeyboard, error) {
	if !c.d.Config().Media.Enabled {
		return c.text("media_disabled"), nil, nil
	}
	if len(args) == 0 {
		return c.devList(kind), c.devKeyboard(kind), nil
	}
	slot, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || slot < 1 || slot > config.AudioSlots {
		return c.text("dev_usage_" + kind), nil, fmt.Errorf("invalid /%s argument %q", devCommandName(kind), args[0])
	}
	line, err := c.devSwitch(ctx, kind, slot)
	return line, nil, err
}

func devCommandName(kind string) string {
	if kind == "in" {
		return "input"
	}
	return "output"
}

// devList is the list text: title, one line per filled slot, the hint.
func (c *Control) devList(kind string) string {
	slots := c.devSlots(kind)
	if len(slots) == 0 {
		return c.text("dev_none_" + kind)
	}
	var b strings.Builder
	b.WriteString(c.text("dev_title_" + kind))
	for _, s := range slots {
		b.WriteString("\n")
		line := c.text("dev_entry", s.Slot, html.EscapeString(s.Name))
		switch {
		case s.Default:
			line = "✓ " + line
		case !s.Present:
			line += " " + c.text("dev_missing")
		}
		b.WriteString(line)
	}
	b.WriteString("\n\n")
	b.WriteString(c.text("dev_hint_" + kind))
	return b.String()
}

// devKeyboard is one button per filled slot, two to a row; nil without
// slots. The callback data is the slot only.
func (c *Control) devKeyboard(kind string) *telegram.InlineKeyboard {
	slots := c.devSlots(kind)
	if len(slots) == 0 || !c.d.Config().Media.Enabled {
		return nil
	}
	glyph := "🔊"
	if kind == "in" {
		glyph = "🎙"
	}
	var rows [][]telegram.InlineButton
	for i, s := range slots {
		name := []rune(s.Name)
		if len(name) > 20 {
			name = append(name[:19], '…')
		}
		text := fmt.Sprintf("%s %d · %s", glyph, s.Slot, string(name))
		if s.Default {
			text = "✓ " + text
		}
		b := telegram.InlineButton{Text: text, CallbackData: kind + ":" + strconv.Itoa(s.Slot)}
		if i%2 == 0 {
			rows = append(rows, []telegram.InlineButton{b})
		} else {
			rows[len(rows)-1] = append(rows[len(rows)-1], b)
		}
	}
	return &telegram.InlineKeyboard{InlineKeyboard: rows}
}

// devSwitch switches to slot and words the outcome. Only real failures are
// returned as errors.
func (c *Control) devSwitch(ctx context.Context, kind string, slot int) (string, error) {
	_, err := c.d.Media.Run(ctx, devCommand(kind), &slot)
	if err == nil {
		name := config.AudioSlotName(c.slotNames(kind), slot)
		return c.text("dev_switched_"+kind, html.EscapeString(name)), nil
	}
	var de *action.AudioDeviceError
	if errors.As(err, &de) {
		switch de.Code {
		case action.AudioSlotEmpty:
			return c.text("dev_slot_empty", slot), nil
		case action.AudioDeviceMissing:
			return c.text("dev_not_there", html.EscapeString(config.AudioSlotName(c.slotNames(kind), slot))), nil
		}
		return c.text("dev_switch_fail", html.EscapeString(de.Message)), err
	}
	line, _, err := c.mediaError(err)
	return line, err
}

func (c *Control) slotNames(kind string) []string {
	if kind == "in" {
		return c.d.Config().Audio.Inputs
	}
	return c.d.Config().Audio.Outputs
}

// devCallback handles out:<slot> and in:<slot>: switch, then the list as it
// is now with the result under it.
func (c *Control) devCallback(ctx context.Context, kind, arg, data string) (string, string, error) {
	slot, err := strconv.Atoi(arg)
	if err != nil || slot < 1 || slot > config.AudioSlots {
		return "", c.text("unknown_button"), fmt.Errorf("unknown callback %q", data)
	}
	if !c.d.Config().Media.Enabled {
		line := c.text("media_disabled")
		return line, toastOf(line), nil
	}
	line, err := c.devSwitch(ctx, kind, slot)
	return c.devList(kind) + "\n\n" + line, toastOf(line), err
}

// outputLine is the /status "출력: 스피커" line from the stored audio
// sample, "" while there is none.
func (c *Control) outputLine(a status.Audio) string {
	if !a.Available || a.Device == nil || *a.Device == "" {
		return ""
	}
	return c.text("st_output") + ": " + html.EscapeString(*a.Device)
}
