package gui

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	for line, want := range map[string][]string{
		``:                          nil,
		`   `:                       nil,
		`-a -b`:                     {"-a", "-b"},
		`  -a   "two words"  `:      {"-a", "two words"},
		`--path "C:\My Files\x" -v`: {"--path", `C:\My Files\x`, "-v"},
		`C:\dir\ next`:              {`C:\dir\`, "next"},
		`'say "hi"' x`:              {`say "hi"`, "x"},
		`""`:                        {""},
		`a "" b`:                    {"a", "", "b"},
		`pre"quoted part"post`:      {"prequoted partpost"},
		"tab\tsep":                  {"tab", "sep"},
		`it's`:                      nil, // unbalanced: see below
	} {
		got, err := splitArgs(line)
		if line == `it's` {
			if !errors.Is(err, errUnbalancedQuote) {
				t.Errorf("splitArgs(%q) err = %v, want unbalanced", line, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("splitArgs(%q) = %q, %v; want %q", line, got, err, want)
		}
	}
	if _, err := splitArgs(`"open`); !errors.Is(err, errUnbalancedQuote) {
		t.Errorf("open quote: %v", err)
	}
}

func TestJoinArgsRoundTrip(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-a"},
		{"two words", "", `C:\Program Files\x`},
		{`say "hi"`, "it's"},
		{"a\tb"},
	} {
		line := joinArgs(args)
		got, err := splitArgs(line)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Errorf("%q -> %q -> %q, %v", args, line, got, err)
		}
	}
	if got := joinArgs([]string{"-silent", "two words"}); got != `-silent "two words"` {
		t.Errorf("joinArgs = %q", got)
	}
}

func TestRowProblem(t *testing.T) {
	ok := []presetRow{
		{Slot: 1, Name: "게임 모드", Type: "program", Path: `C:\Games\steam.exe`, Args: `-bigpicture "x y"`},
		{Slot: 10, Name: "x", Type: "program", Path: `\\nas\apps\x.EXE`},
		{Slot: 2, Name: "대시보드", Type: "url", Path: " https://example.com/d "},
		{Slot: 3, Name: "스크립트", Type: "script", Path: `C:\s\go.ps1`},
		{Slot: 4, Name: "bat", Type: "script", Path: `C:\s\go.CMD`, Args: "a b"},
	}
	for _, r := range ok {
		if key := rowProblem(r); key != "" {
			t.Errorf("%+v: %s", r, key)
		}
	}
	for want, r := range map[string]presetRow{
		"presets.err.slot":     {Slot: 0, Name: "x", Type: "url", Path: "https://x"},
		"presets.err.name":     {Slot: 1, Name: " ", Type: "url", Path: "https://x"},
		"presets.err.namelong": {Slot: 1, Name: strings.Repeat("가", 31), Type: "url", Path: "https://x"},
		"presets.err.type":     {Slot: 1, Name: "x", Type: "", Path: "https://x"},
		"presets.err.path":     {Slot: 1, Name: "x", Type: "program", Path: " "},
		"presets.err.quote":    {Slot: 1, Name: "x", Type: "program", Path: `C:\x.exe`, Args: `"open`},
		"presets.err.args":     {Slot: 1, Name: "x", Type: "program", Path: `C:\x.exe`, Args: strings.Repeat("a ", 33)},
		"presets.err.url":      {Slot: 1, Name: "x", Type: "url", Path: "file:///C:/x"},
		"presets.err.abs":      {Slot: 1, Name: "x", Type: "program", Path: "notepad.exe"},
		"presets.err.exe":      {Slot: 1, Name: "x", Type: "program", Path: `C:\x.bat`},
		"presets.err.script":   {Slot: 1, Name: "x", Type: "script", Path: `C:\x.vbs`},
	} {
		if got := rowProblem(r); got != want {
			t.Errorf("%+v: %q, want %q", r, got, want)
		}
	}
	rows := []presetRow{ok[0], {Slot: 1, Name: "again", Type: "url", Path: "https://x"}}
	if key, slot := rowsProblem(rows); key != "presets.err.dup" || slot != 1 {
		t.Errorf("rowsProblem = %q, %d", key, slot)
	}

	// A URL row's arguments are ignored, whatever is in them (review 3):
	// the editor clears them, and preset() drops them.
	for _, args := range []string{"a", `"open`, strings.Repeat("a ", 40)} {
		r := presetRow{Slot: 1, Name: "x", Type: "url", Path: "https://x", Args: args}
		if key := rowProblem(r); key != "" {
			t.Errorf("url row with args %q: %s", args, key)
		}
		if p, err := r.preset(); err != nil || p.Args != nil {
			t.Errorf("url row with args %q -> %+v, %v", args, p, err)
		}
	}
}

// Preset names are unique, ignoring case and outer spaces (C6): the editor
// marks every row of a clash and the save names the first.
func TestDuplicatePresetNames(t *testing.T) {
	rows := []presetRow{
		{Slot: 1, Name: "Game Mode", Type: "url", Path: "https://a"},
		{Slot: 2, Name: "work", Type: "url", Path: "https://b"},
		{Slot: 3, Name: " game mode ", Type: "url", Path: "https://c"},
		{Slot: 4, Name: "", Type: "url", Path: "https://d"},
		{Slot: 5, Name: "", Type: "url", Path: "https://e"},
	}
	if got := duplicateNames(rows); !slices.Equal(got, []bool{true, false, true, false, false}) {
		t.Errorf("duplicateNames = %v", got)
	}
	if key, slot := rowsProblem(rows[:3]); key != "presets.err.namedup" || slot != 3 {
		t.Errorf("rowsProblem = %q, %d, want namedup on slot 3", key, slot)
	}
	rows[2].Name = "game mode 2"
	if key, _ := rowsProblem(rows[:3]); key != "" {
		t.Errorf("distinct names: %q", key)
	}
}

func TestPresetsStateRoundTrip(t *testing.T) {
	base := Config{
		Port:     5001,
		NotifyPC: NotifyPCConfig{Enabled: true},
		Presets: []Preset{
			{Slot: 3, Name: "b", Type: "program", Path: `C:\b.exe`, Args: []string{"two words", "-x"}},
			{Slot: 1, Name: "a", Type: "url", Path: "https://a.example"},
		},
		Telegram: TelegramConfig{ChatID: "42"},
		Media:    MediaConfig{Enabled: true},
	}
	s := presetsStateFromConfig(base)
	if len(s.Rows) != 2 || s.Rows[0].Slot != 1 || s.Rows[1].Args != `"two words" -x` {
		t.Errorf("rows = %+v", s.Rows)
	}
	if s.dirty(base) {
		t.Error("an unedited form is dirty")
	}
	cfg, err := s.applyTo(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.ChatID != "42" || !cfg.Media.Enabled || !cfg.NotifyPC.Enabled || !presetsEqual(cfg.Presets, base.Presets) {
		t.Errorf("applyTo changed other fields or the presets: %+v", cfg)
	}

	s.Rows[1].Args = `"two words"`
	if !s.dirty(base) {
		t.Error("an argument edit is not dirty")
	}
	s.Rows[1].Args = `"open`
	if !s.dirty(base) {
		t.Error("an unparsable row is not dirty")
	}
	if _, err := s.applyTo(base); err == nil {
		t.Error("applyTo accepted an unparsable row")
	}

	s = presetsStateFromConfig(base)
	s.Rows = nil
	cfg, _ = s.applyTo(base)
	if cfg.Presets == nil || len(cfg.Presets) != 0 {
		t.Errorf("removing every row must send [], got %#v", cfg.Presets)
	}
	if len(base.Presets) != 2 {
		t.Error("applyTo modified the base")
	}
}

func TestPresetButtonLabel(t *testing.T) {
	if got := presetButtonLabel(Preset{Slot: 2, Name: "방송 시작"}); got != "2 · 방송 시작" {
		t.Errorf("label = %q", got)
	}
	if actionErrorKey(errors.New("plain")) != "" || actionErrorKey(&actionError{Code: "failed"}) != "" {
		t.Error("unknown errors should show the service's message")
	}
}

// The service's writable_by_others warnings (C6) in the user's words: on
// the row of their slot, and led by the slot in the "Saved" dialog.
func TestPresetWarningTexts(t *testing.T) {
	ws := []PresetWarning{
		{Slot: 2, Code: "writable_by_others", Path: `C:\Shared\run.ps1`},
		{Slot: 2, Code: "writable_by_others", Path: `C:\Shared`},
		{Slot: 5, Code: "something_new"},
	}
	want := T(LangKo, "presets.warn.writable") + "\n" + `C:\Shared\run.ps1`
	if got := presetWarningText(LangKo, ws[0]); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if !strings.Contains(want, "다른 사용자도 고칠 수 있습니다") {
		t.Errorf("ko text = %q", want)
	}
	if got := presetSlotWarnings(LangKo, ws, 2); strings.Count(got, T(LangKo, "presets.warn.writable")) != 2 || !strings.Contains(got, `C:\Shared`) {
		t.Errorf("slot 2 = %q", got)
	}
	if got := presetSlotWarnings(LangKo, ws, 5); got != "something_new" {
		t.Errorf("an unknown code = %q, want the code itself", got)
	}
	if got := presetSlotWarnings(LangKo, ws, 1); got != "" {
		t.Errorf("slot without warnings = %q", got)
	}
	lines := presetWarningLines(LangKo, ws)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "프리셋 2: ") || !strings.HasPrefix(lines[2], "프리셋 5: ") {
		t.Errorf("dialog lines = %q", lines)
	}
}

func TestURLPresetRow(t *testing.T) {
	// A URL row never sends arguments, whatever the entry holds.
	p, _ := presetRow{Slot: 1, Name: " x ", Type: "url", Path: " https://x ", Args: "a"}.preset()
	if p.Args != nil || p.Name != "x" || p.Path != "https://x" {
		t.Errorf("url preset = %+v", p)
	}
}

func TestFreeSlot(t *testing.T) {
	if s := freeSlot(nil); s != 1 {
		t.Errorf("empty: %d", s)
	}
	if s := freeSlot([]presetRow{{Slot: 1}, {Slot: 2}, {Slot: 4}}); s != 3 {
		t.Errorf("gap: %d", s)
	}
	var full []presetRow
	for i := 1; i <= 10; i++ {
		full = append(full, presetRow{Slot: i})
	}
	if s := freeSlot(full); s != 0 {
		t.Errorf("full: %d", s)
	}
}
