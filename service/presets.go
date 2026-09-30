package service

// Presets (#109, docs/design/media-notify.md §10): actions registered in the
// app — start a program, open a URL, run a script — that SmartThings and
// Telegram can trigger by slot number. The remote side never sends a path
// or an argument; what a slot does lives only in config.json on this PC,
// and it always runs in the logged-in user's session (user-action preset),
// never as SYSTEM.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	presetMinSlot  = 1
	presetMaxSlot  = 10
	presetMaxName  = 30
	presetMaxArgs  = useraction.MaxPresetArgs
	presetTypeProg = "program"
	presetTypeURL  = "url"
	presetTypeScr  = "script"
)

// Preset is one entry of "presets" in config.json.
type Preset struct {
	Slot int      `json:"slot"`           // 1–10, unique
	Name string   `json:"name"`           // shown in SmartThings and Telegram, ≤ 30 characters
	Type string   `json:"type"`           // program | url | script
	Path string   `json:"path"`           // absolute exe/script path, or the http(s) URL
	Args []string `json:"args,omitempty"` // program/script arguments, ≤ 32
}

// argv is the user-action argument vector that runs p.
func (p Preset) argv() []string {
	args := []string{useraction.ActionPreset, "--type", p.Type, "--path", p.Path}
	for _, a := range p.Args {
		args = append(args, "--arg", a)
	}
	return args
}

// validatePreset applies the §10 rules to one entry. The path and argument
// rules are exactly the subcommand's (useraction.Parse and
// ValidatePresetLaunch), so a preset that saves is a preset that can run.
func validatePreset(p Preset) error {
	if p.Slot < presetMinSlot || p.Slot > presetMaxSlot {
		return fmt.Errorf("slot must be %d-%d (got %d)", presetMinSlot, presetMaxSlot, p.Slot)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return errors.New("name is empty")
	}
	if name != p.Name {
		return errors.New("name has leading or trailing spaces")
	}
	if utf8.RuneCountInString(name) > presetMaxName {
		return fmt.Errorf("name must be at most %d characters", presetMaxName)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("name contains control characters")
	}
	switch p.Type {
	case presetTypeProg, presetTypeURL, presetTypeScr:
	default:
		return fmt.Errorf("type must be program, url or script (got %q)", p.Type)
	}
	if len(p.Args) > presetMaxArgs {
		return fmt.Errorf("at most %d arguments", presetMaxArgs)
	}
	if strings.TrimSpace(p.Path) != p.Path {
		return errors.New("path has leading or trailing spaces")
	}
	if _, err := useraction.Parse(p.argv()); err != nil {
		return errors.New(presetRuleMessage(err))
	}
	if err := useraction.ValidatePresetLaunch(p.Type, p.Path, p.Args); err != nil {
		return errors.New(presetRuleMessage(err))
	}
	return nil
}

// presetRuleMessage strips the subcommand's "bad_args: preset: " prefix.
func presetRuleMessage(err error) string {
	var ue *useraction.Error
	msg := err.Error()
	if errors.As(err, &ue) {
		msg = ue.Message
	}
	return strings.TrimPrefix(msg, "preset: ")
}

// validatePresets checks every entry and that slots are unique; "" when
// the list is fine, otherwise one message naming the slot.
func validatePresets(ps []Preset) string {
	seen := map[int]bool{}
	for i, p := range ps {
		if err := validatePreset(p); err != nil {
			return fmt.Sprintf("presets[%d] (slot %d): %v", i, p.Slot, err)
		}
		if seen[p.Slot] {
			return fmt.Sprintf("presets: slot %d is used twice", p.Slot)
		}
		seen[p.Slot] = true
	}
	return ""
}

// dropInvalidPresets is the load-time counterpart of validatePresets: a
// config.json edited by hand keeps its valid entries, and each ignored one
// gets a log line. A later duplicate of a slot is the one dropped.
func dropInvalidPresets(ps []Preset) []Preset {
	out := []Preset{}
	seen := map[int]bool{}
	for i, p := range ps {
		if err := validatePreset(p); err != nil {
			logMsg("WARNING: config.json presets[%d] (slot %d) ignored: %v", i, p.Slot, err)
			continue
		}
		if seen[p.Slot] {
			logMsg("WARNING: config.json presets[%d] ignored: slot %d is used twice", i, p.Slot)
			continue
		}
		seen[p.Slot] = true
		out = append(out, p)
	}
	return out
}

// normalizePresets returns a sorted copy that never aliases ps and is
// never nil (config.json documents "presets": []).
func normalizePresets(ps []Preset) []Preset {
	out := make([]Preset, 0, len(ps))
	for _, p := range ps {
		p.Args = slices.Clone(p.Args)
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b Preset) int { return cmp.Compare(a.Slot, b.Slot) })
	return out
}

// changedPresetSlots lists the slots whose entry differs between old and
// new (added, removed or edited), ascending.
func changedPresetSlots(old, new []Preset) []int {
	index := func(ps []Preset) map[int]Preset {
		m := make(map[int]Preset, len(ps))
		for _, p := range ps {
			m[p.Slot] = p
		}
		return m
	}
	o, n := index(old), index(new)
	var slots []int
	for s, p := range o {
		q, ok := n[s]
		if !ok || !presetEqual(p, q) {
			slots = append(slots, s)
		}
	}
	for s := range n {
		if _, ok := o[s]; !ok {
			slots = append(slots, s)
		}
	}
	slices.Sort(slots)
	return slots
}

func presetEqual(a, b Preset) bool {
	return a.Slot == b.Slot && a.Name == b.Name && a.Type == b.Type && a.Path == b.Path && slices.Equal(a.Args, b.Args)
}

// presetChangeKey is the security.config_changed key for a preset change:
// "presets[1,3]". Only slot numbers — never names, paths or arguments.
func presetChangeKey(slots []int) string {
	parts := make([]string, len(slots))
	for i, s := range slots {
		parts[i] = strconv.Itoa(s)
	}
	return "presets[" + strings.Join(parts, ",") + "]"
}

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
	for _, p := range normalizePresets(ps) {
		out = append(out, stPresetRef{Slot: p.Slot, Name: p.Name})
	}
	return out
}

// presetRun is runUserAction, replaced by the tests.
var presetRun = runUserAction

// runPreset starts p in the user session. It does not wait for the
// program; nil means it was started.
func runPreset(ctx context.Context, p Preset, by string) error {
	res, err := presetRun(ctx, p.argv()...)
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
	if body.Value == nil || *body.Value < presetMinSlot || *body.Value > presetMaxSlot {
		stError(w, http.StatusBadRequest, fmt.Sprintf("value must be a preset slot %d-%d", presetMinSlot, presetMaxSlot))
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
func handlePresetsRunAPI(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "POST") {
		return
	}
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
func handlePresetsTestAPI(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "POST") {
		return
	}
	var p Preset
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if p.Slot == 0 {
		p.Slot = presetMinSlot // an unsaved row may not have one yet
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "test"
	}
	if err := validatePreset(p); err != nil {
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
