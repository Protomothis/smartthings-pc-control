package config

// Presets (#109, docs/design/media-notify.md §10): actions registered in the
// app that SmartThings and Telegram can trigger by slot number. This file
// is the config.json side — the shape and the rules; running one is
// service/presets.go.

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	PresetMinSlot  = 1
	PresetMaxSlot  = 10
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

// Argv is the user-action argument vector that runs p.
func (p Preset) Argv() []string {
	args := []string{useraction.ActionPreset, "--type", p.Type, "--path", p.Path}
	for _, a := range p.Args {
		args = append(args, "--arg", a)
	}
	return args
}

// ValidatePreset applies the §10 rules to one entry. The path and argument
// rules are exactly the subcommand's (useraction.Parse and
// ValidatePresetLaunch), so a preset that saves is a preset that can run.
func ValidatePreset(p Preset) error {
	if p.Slot < PresetMinSlot || p.Slot > PresetMaxSlot {
		return fmt.Errorf("slot must be %d-%d (got %d)", PresetMinSlot, PresetMaxSlot, p.Slot)
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
	if _, err := useraction.Parse(p.Argv()); err != nil {
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

// ValidatePresets checks every entry and that slots are unique; "" when
// the list is fine, otherwise one message naming the slot.
func ValidatePresets(ps []Preset) string {
	seen := map[int]bool{}
	for i, p := range ps {
		if err := ValidatePreset(p); err != nil {
			return fmt.Sprintf("presets[%d] (slot %d): %v", i, p.Slot, err)
		}
		if seen[p.Slot] {
			return fmt.Sprintf("presets: slot %d is used twice", p.Slot)
		}
		seen[p.Slot] = true
	}
	return ""
}

// DropInvalidPresets is the load-time counterpart of ValidatePresets: a
// config.json edited by hand keeps its valid entries, and each ignored one
// gets a log line. A later duplicate of a slot is the one dropped.
func DropInvalidPresets(ps []Preset) []Preset {
	out := []Preset{}
	seen := map[int]bool{}
	for i, p := range ps {
		if err := ValidatePreset(p); err != nil {
			logx.Printf("WARNING: config.json presets[%d] (slot %d) ignored: %v", i, p.Slot, err)
			continue
		}
		if seen[p.Slot] {
			logx.Printf("WARNING: config.json presets[%d] ignored: slot %d is used twice", i, p.Slot)
			continue
		}
		seen[p.Slot] = true
		out = append(out, p)
	}
	return out
}

// FindPreset looks a slot up in the live list.
func FindPreset(ps []Preset, slot int) (Preset, bool) {
	for _, p := range ps {
		if p.Slot == slot {
			return p, true
		}
	}
	return Preset{}, false
}

// FindPresetByName matches a Telegram /run argument: a slot number, or a
// name compared without regard to case or surrounding space.
func FindPresetByName(ps []Preset, arg string) (Preset, bool) {
	arg = strings.TrimSpace(arg)
	if n, err := strconv.Atoi(arg); err == nil {
		return FindPreset(ps, n)
	}
	for _, p := range ps {
		if strings.EqualFold(p.Name, arg) {
			return p, true
		}
	}
	return Preset{}, false
}

// NormalizePresets returns a sorted copy that never aliases ps and is
// never nil (config.json documents "presets": []).
func NormalizePresets(ps []Preset) []Preset {
	out := make([]Preset, 0, len(ps))
	for _, p := range ps {
		p.Args = slices.Clone(p.Args)
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b Preset) int { return cmp.Compare(a.Slot, b.Slot) })
	return out
}

// ChangedPresetSlots lists the slots whose entry differs between old and
// new (added, removed or edited), ascending.
func ChangedPresetSlots(old, new []Preset) []int {
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

// PresetChangeKey is the security.config_changed key for a preset change:
// "presets[1,3]". Only slot numbers — never names, paths or arguments.
func PresetChangeKey(slots []int) string {
	parts := make([]string, len(slots))
	for i, s := range slots {
		parts[i] = strconv.Itoa(s)
	}
	return "presets[" + strings.Join(parts, ",") + "]"
}
