package gui

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Pure form model of the presets tab (presets_tab.go, #109). Kept free of
// widgets so the round trip and the checks are unit-tested. The service
// re-validates everything on save; these checks only catch the common
// mistakes before a request is made, in the user's language.

const (
	presetMaxSlots = 10
	presetMaxName  = 30
	presetMaxArgs  = 32
)

// presetTypes are the wire values of the type select, in display order.
var presetTypes = []string{"program", "url", "script"}

// presetRow is one editor row as typed: Args is the single-line entry.
type presetRow struct {
	Slot int
	Name string
	Type string
	Path string
	Args string
}

// errUnbalancedQuote is splitArgs' only failure.
var errUnbalancedQuote = errors.New("unbalanced quote")

// splitArgs splits the argument line on spaces. A double- or single-quoted
// part is one argument with its spaces kept ("C:\My Files" or 'say "hi"');
// the quotes themselves are removed, and "" is an empty argument.
// Backslashes are ordinary characters, so Windows paths need no escaping.
func splitArgs(line string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		inArg bool
		quote rune
	)
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errUnbalancedQuote
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out, nil
}

// joinArgs is the inverse of splitArgs for the entry: an argument with a
// space, a quote or nothing in it is quoted — with ' when it contains ",
// otherwise with ".
func joinArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		switch {
		case a == "":
			parts[i] = `""`
		case strings.Contains(a, `"`):
			parts[i] = "'" + a + "'"
		case strings.ContainsAny(a, " \t'"):
			parts[i] = `"` + a + `"`
		default:
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// rowFromPreset is what the editor shows for a saved preset.
func rowFromPreset(p Preset) presetRow {
	return presetRow{Slot: p.Slot, Name: p.Name, Type: p.Type, Path: p.Path, Args: joinArgs(p.Args)}
}

// rowProblem names what is wrong with a row, as an i18n key ("" when it
// looks fine). The service has the final word; this mirrors its rules.
// Every key's text takes the slot number (%d).
func rowProblem(r presetRow) string {
	name := strings.TrimSpace(r.Name)
	path := strings.TrimSpace(r.Path)
	switch {
	case r.Slot < 1 || r.Slot > presetMaxSlots:
		return "presets.err.slot"
	case name == "":
		return "presets.err.name"
	case utf8.RuneCountInString(name) > presetMaxName:
		return "presets.err.namelong"
	case !slices.Contains(presetTypes, r.Type):
		return "presets.err.type"
	case path == "":
		return "presets.err.path"
	}
	args, err := splitArgs(r.Args)
	if err != nil {
		return "presets.err.quote"
	}
	if len(args) > presetMaxArgs {
		return "presets.err.args"
	}
	lower := strings.ToLower(path)
	switch r.Type {
	case "url":
		u, err := url.Parse(path)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "presets.err.url"
		}
		if len(args) > 0 {
			return "presets.err.urlargs"
		}
	case "program":
		if !isAbsPath(path) {
			return "presets.err.abs"
		}
		if !strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".com") {
			return "presets.err.exe"
		}
	case "script":
		if !isAbsPath(path) {
			return "presets.err.abs"
		}
		if !strings.HasSuffix(lower, ".ps1") && !strings.HasSuffix(lower, ".bat") && !strings.HasSuffix(lower, ".cmd") {
			return "presets.err.script"
		}
	}
	return ""
}

// isAbsPath accepts "C:\…" and "\\server\…".
func isAbsPath(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return strings.HasPrefix(p, `\\`) && len(p) > 2
}

// preset turns a row into the wire form (trimmed name and path, split
// arguments; a URL never carries any).
func (r presetRow) preset() (Preset, error) {
	args, err := splitArgs(r.Args)
	if err != nil {
		return Preset{}, err
	}
	if r.Type == "url" {
		args = nil
	}
	return Preset{
		Slot: r.Slot,
		Name: strings.TrimSpace(r.Name),
		Type: r.Type,
		Path: strings.TrimSpace(r.Path),
		Args: args,
	}, nil
}

// rowsProblem checks every row, that no slot is used twice and that no two
// rows share a name (C6: Telegram's /run takes a name too). It returns the
// i18n key and the slot it is about.
func rowsProblem(rows []presetRow) (string, int) {
	seen := map[int]bool{}
	names := map[string]bool{}
	for _, r := range rows {
		if key := rowProblem(r); key != "" {
			return key, r.Slot
		}
		if seen[r.Slot] {
			return "presets.err.dup", r.Slot
		}
		seen[r.Slot] = true
		name := presetNameKey(r.Name)
		if names[name] {
			return "presets.err.namedup", r.Slot
		}
		names[name] = true
	}
	return "", 0
}

// presetNameKey is the form two names are compared in: trimmed, case
// folded — the service's rule for unique names.
func presetNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// duplicateNames marks, by row index, every row whose name another row
// also has (case-insensitive, trimmed). Empty names are left to rowProblem.
func duplicateNames(rows []presetRow) []bool {
	count := map[string]int{}
	for _, r := range rows {
		if k := presetNameKey(r.Name); k != "" {
			count[k]++
		}
	}
	out := make([]bool, len(rows))
	for i, r := range rows {
		out[i] = count[presetNameKey(r.Name)] > 1
	}
	return out
}

// sortedPresets is a copy in slot order with empty argument lists as nil,
// the shape two lists are compared in.
func sortedPresets(ps []Preset) []Preset {
	out := make([]Preset, len(ps))
	for i, p := range ps {
		if len(p.Args) == 0 {
			p.Args = nil
		} else {
			p.Args = slices.Clone(p.Args)
		}
		out[i] = p
	}
	slices.SortStableFunc(out, func(a, b Preset) int { return cmp.Compare(a.Slot, b.Slot) })
	return out
}

func presetsEqual(a, b []Preset) bool {
	a, b = sortedPresets(a), sortedPresets(b)
	return slices.EqualFunc(a, b, func(x, y Preset) bool {
		return x.Slot == y.Slot && x.Name == y.Name && x.Type == y.Type && x.Path == y.Path && slices.Equal(x.Args, y.Args)
	})
}

// freeSlot is the lowest slot no row uses, or 0 when all ten are taken.
func freeSlot(rows []presetRow) int {
	used := map[int]bool{}
	for _, r := range rows {
		used[r.Slot] = true
	}
	for s := 1; s <= presetMaxSlots; s++ {
		if !used[s] {
			return s
		}
	}
	return 0
}

// presetsFormState is the tab's contents as plain values.
type presetsFormState struct {
	Rows []presetRow
}

func presetsStateFromConfig(cfg Config) presetsFormState {
	var s presetsFormState
	for _, p := range sortedPresets(cfg.Presets) {
		s.Rows = append(s.Rows, rowFromPreset(p))
	}
	return s
}

// presets converts every row; the first conversion error is returned.
func (s presetsFormState) presets() ([]Preset, error) {
	out := []Preset{} // never nil: [] clears the list, null would keep it
	for _, r := range s.Rows {
		p, err := r.preset()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// applyTo returns base with the presets written over it; the other tabs'
// values are untouched and base's slices are not aliased.
func (s presetsFormState) applyTo(base Config) (Config, error) {
	ps, err := s.presets()
	if err != nil {
		return base, err
	}
	cfg := base
	cfg.Presets = ps
	return cfg, nil
}

// dirty reports whether saving would change base. A row that does not
// parse is a change (there is something to fix and save).
func (s presetsFormState) dirty(base Config) bool {
	ps, err := s.presets()
	if err != nil {
		return true
	}
	return !presetsEqual(ps, base.Presets)
}

// presetWarningText is one service warning (C6) in the user's words, the
// file or folder it is about on the next line. An unknown code shows as
// itself.
func presetWarningText(l Lang, w PresetWarning) string {
	text := w.Code
	if w.Code == "writable_by_others" {
		text = T(l, "presets.warn.writable")
	}
	if w.Path != "" {
		text += "\n" + w.Path
	}
	return text
}

// presetSlotWarnings is what the editor row of slot shows: its warnings,
// one per paragraph; "" when it has none.
func presetSlotWarnings(l Lang, ws []PresetWarning, slot int) string {
	var parts []string
	for _, w := range ws {
		if w.Slot == slot {
			parts = append(parts, presetWarningText(l, w))
		}
	}
	return strings.Join(parts, "\n")
}

// presetWarningLines are the warnings for the "Saved" dialog, each led by
// its slot: "프리셋 3: …".
func presetWarningLines(l Lang, ws []PresetWarning) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = fmt.Sprintf(T(l, "presets.warn.line"), w.Slot, presetWarningText(l, w))
	}
	return out
}

// presetButtonLabel is the command-tab button text: "1 · 게임 모드".
func presetButtonLabel(p Preset) string {
	return strconv.Itoa(p.Slot) + " · " + p.Name
}

// actionErrorKey is the i18n key for the service codes the app words
// itself; "" means show the service's message.
func actionErrorKey(err error) string {
	if errors.Is(err, errLocalOnly) {
		return "localonly.note"
	}
	var ae *actionError
	if !errors.As(err, &ae) {
		return ""
	}
	switch ae.Code {
	case "no_user_session", "notify_disabled", "rate_limited", "no_such_preset", "timeout", "unsupported", "service_too_old":
		return "action.err." + ae.Code
	}
	return ""
}
