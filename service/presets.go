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
	"strconv"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/config"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// findPreset looks a slot up in the live list.
func findPreset(ps []Preset, slot int) (Preset, bool) {
	for _, p := range ps {
		if p.Slot == slot {
			return p, true
		}
	}
	return Preset{}, false
}

// findPresetByName matches a Telegram /run argument: a slot number, or a
// name compared without regard to case or surrounding space.
func findPresetByName(ps []Preset, arg string) (Preset, bool) {
	arg = strings.TrimSpace(arg)
	if n, err := strconv.Atoi(arg); err == nil {
		return findPreset(ps, n)
	}
	for _, p := range ps {
		if strings.EqualFold(p.Name, arg) {
			return p, true
		}
	}
	return Preset{}, false
}

// stPresetRef is one {slot, name} of status "presets" (§10).
type stPresetRef struct {
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

// stPresetList is the status "presets" array: slot order, never null.
func stPresetList(ps []Preset) []stPresetRef {
	out := make([]stPresetRef, 0, len(ps))
	for _, p := range config.NormalizePresets(ps) {
		out = append(out, stPresetRef{Slot: p.Slot, Name: p.Name})
	}
	return out
}

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

// presetResultCode is what last_command.result records: "started" or the
// wire error code.
func presetResultCode(err error) string {
	if err == nil {
		return "started"
	}
	_, code, _ := actionErrorStatus(err)
	return code
}

// stPresetRun is the "preset" block of a preset command's reply.
type stPresetRun struct {
	Slot    int    `json:"slot"`
	Name    string `json:"name"`
	Started bool   `json:"started"`
}

// handleSTPreset runs the preset command (§10): value = slot number. It is
// not a registry command — it takes an argument and never goes through the
// grace period or a schedule. The attempt is recorded as last_command
// (with the slot, the name and the outcome); a start is also reported as
// remote.received, since it launched something on this PC.
func handleSTPreset(w http.ResponseWriter, r *http.Request, body stCommandRequest, from string) {
	if body.Minutes != 0 {
		stError(w, http.StatusBadRequest, "minutes does not apply to preset")
		return
	}
	if body.Value == nil || *body.Value < config.PresetMinSlot || *body.Value > config.PresetMaxSlot {
		stError(w, http.StatusBadRequest, fmt.Sprintf("value must be a preset slot %d-%d", config.PresetMinSlot, config.PresetMaxSlot))
		return
	}
	slot := *body.Value
	p, ok := findPreset(getConfig().Presets, slot)
	if !ok {
		logMsg("ST API: preset %d from %s: no such preset", slot, from)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_preset",
			"message": fmt.Sprintf("slot %d has no preset", slot)})
		return
	}
	err := runPreset(r.Context(), p, "smartthings "+from)
	notePresetCommand(p, from, "smartthings", presetResultCode(err))
	if err != nil {
		writeActionError(w, err)
		return
	}
	// Only a start is "executed"; a refusal is in last_command and the log.
	emit("remote", "received", map[string]string{
		"command": truncate(fmt.Sprintf("preset %d (%s)", p.Slot, p.Name), 64),
		"from":    from,
	})
	writeJSON(w, http.StatusOK, stCommandResponse{
		Accepted: true,
		Executed: true,
		Schedule: stScheduleView(),
		Preset:   &stPresetRun{Slot: p.Slot, Name: p.Name, Started: true},
	})
}

// ---- local API for the app ----------------------------------------------

// handlePresetsRunAPI serves POST /api/presets/run {slot} — the command
// tab's [실행] buttons, which run a saved preset.
var handlePresetsRunAPI = apiAuth(servePresetsRunAPI, "POST")

func servePresetsRunAPI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slot int `json:"slot"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, stMaxBody)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	p, ok := findPreset(getConfig().Presets, body.Slot)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"status": "error", "error": "no_such_preset",
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
		status, code, msg := actionErrorStatus(err)
		writeJSON(w, status, map[string]string{"status": "error", "error": code, "message": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "started": true, "slot": p.Slot, "name": p.Name})
}
