package service

// Presets (#109, docs/design/media-notify.md §10): actions registered in the
// app — start a program, open a URL, run a script — that SmartThings and
// Telegram can trigger by slot number. The remote side never sends a path
// or an argument; what a slot does lives only in config.json on this PC,
// and it always runs in the logged-in user's session (user-action preset),
// never as SYSTEM.

import (
	"context"
	"encoding/json"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// runPreset starts p in the user session. It does not wait for the
// program; nil means it was started.
func runPreset(ctx context.Context, p Preset, by string) error {
	res, err := userRun.preset(ctx, p.Argv()...)
	if err != nil {
		logMsg("Preset %d (%s) via %s failed: %v", p.Slot, p.Name, by, err)
		return err
	}
	var started bool
	if raw, ok := res.Fields["started"]; ok {
		json.Unmarshal(raw, &started)
	}
	if !started {
		logMsg("Preset %d (%s) via %s: reply did not confirm the start", p.Slot, p.Name, by)
		return &userActionError{Code: useraction.CodeFailed, Message: "the preset did not report a start"}
	}
	logMsg("Preset %d (%s, %s) started via %s", p.Slot, p.Name, p.Type, by)
	return nil
}
