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
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/stapi"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// presetRun is runUserAction, replaced by the tests.
var presetRun = runUserAction

// runPreset starts p in the user session. It does not wait for the
// program; nil means it was started.
func runPreset(ctx context.Context, p Preset, by string) error {
	res, err := presetRun(ctx, p.Argv()...)
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

// ---- local API for the app ----------------------------------------------

// handlePresetsRunAPI serves POST /api/presets/run {slot} — the command
// tab's [실행] buttons, which run a saved preset.
var handlePresetsRunAPI = apiAuth(servePresetsRunAPI, "POST")

func servePresetsRunAPI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slot int `json:"slot"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, stapi.MaxBody)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	p, ok := config.FindPreset(getConfig().Presets, body.Slot)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"status": "error", "error": "no_such_preset",
			"message": fmt.Sprintf("slot %d has no preset", body.Slot)})
		return
	}
	writePresetAPIResult(w, p, runPreset(r.Context(), p, "app"))
}

// handlePresetsTestAPI serves POST /api/presets/test {slot, name, type,
// path, args} — the editor's [테스트] button, which runs the row as typed,
// before it is saved. It passes the same validation a save does.
var handlePresetsTestAPI = apiAuth(servePresetsTestAPI, "POST")

func servePresetsTestAPI(w http.ResponseWriter, r *http.Request) {
	var p Preset
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if p.Slot == 0 {
		p.Slot = config.PresetMinSlot // an unsaved row may not have one yet
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "test"
	}
	if err := config.ValidatePreset(p); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writePresetAPIResult(w, p, runPreset(r.Context(), p, "app test"))
}

func writePresetAPIResult(w http.ResponseWriter, p Preset, err error) {
	if err != nil {
		status, code, msg := action.Status(err)
		httpx.WriteJSON(w, status, map[string]string{"status": "error", "error": code, "message": msg})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "started": true, "slot": p.Slot, "name": p.Name})
}
