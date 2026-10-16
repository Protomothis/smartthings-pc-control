package tgcontrol

// The /menu buttons beyond the power commands, and the preset buttons
// under /presets (#147). Every press here edits the message it was on and
// keeps its buttons (EditKeyboard): the menu works like a remote, the
// newest result line replacing the last one. Nothing in this file runs a
// power command — those keep their confirm: step in control.go.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
)

// menuResult is the menu with one result line under it, as cancel:menu
// has always edited it.
func (c *Control) menuResult(line string) string {
	return c.menuTitle() + "\n" + line
}

// presetMenuTitle is the preset submenu's text, header included.
func (c *Control) presetMenuTitle() string {
	if len(c.d.Config().Presets) == 0 {
		return c.WithHeader(c.text("presets_none"))
	}
	return c.WithHeader(c.text("presets_menu"))
}

// toastOf is the answerCallbackQuery text for a result line: plain text,
// within Telegram's 200 characters. The message edit carries the line too,
// but Telegram refuses an edit that changes nothing (a second ⏭ in the same
// minute), and the toast still shows then.
func toastOf(h string) string {
	return httpx.Truncate(Plain(h), 190)
}

// menuCallback handles the awake:, media:, menu: and run: buttons.
func (c *Control) menuCallback(ctx context.Context, verb, arg, data string) (string, string, error) {
	switch verb {
	case "awake":
		if arg != "toggle" {
			break
		}
		var args []string
		if c.d.Awake.View().On {
			args = []string{"off"}
		}
		line, _, err := c.awake(args)
		return c.menuResult(line), toastOf(line), err
	case "media":
		line, err := c.mediaButton(ctx, arg)
		if line == "" {
			break
		}
		return c.menuResult(line), toastOf(line), err
	case "menu":
		switch arg {
		case "presets":
			return c.presetMenuTitle(), "", nil
		case "main":
			return c.menuTitle(), "", nil
		}
	case "run":
		slot, where, _ := strings.Cut(arg, ":")
		if _, err := strconv.Atoi(slot); err != nil || (where != "" && where != "menu") {
			break
		}
		// The /run path (#109): the same lookup, run, record and wording.
		line, _, err := c.runPreset([]string{slot})
		base := c.presetList()
		if where == "menu" {
			base = c.presetMenuTitle()
		}
		return base + "\n\n" + line, toastOf(line), err
	}
	return "", c.text("unknown_button"), fmt.Errorf("unknown callback %q", data)
}

// mediaButton runs one media button: mute toggles by the state the user
// session reports now, the others are the media keys. "" for a key the
// menu does not have.
func (c *Control) mediaButton(ctx context.Context, key string) (string, error) {
	switch key {
	case "mute":
		a, err := c.d.Media.ReadAudio(ctx)
		if err != nil {
			line, _, err := c.mediaError(err)
			return line, err
		}
		name := "mute"
		if a.Muted {
			name = "unmute"
		}
		line, _, err := c.mediaCommand(ctx, name, nil)
		return line, err
	case "playpause", "prev", "next":
		line, _, err := c.mediaCommand(ctx, key, nil)
		return line, err
	}
	return "", nil
}
