package config

// The watch list's rules and its load path (#110, #123). Moved here from
// the service package with the code (#127).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func watch(slot int, process, label string) ActivityWatch {
	return ActivityWatch{Slot: slot, Process: process, Label: label}
}

func TestValidateActivity(t *testing.T) {
	six := make([]ActivityWatch, 6)
	for i := range six {
		six[i] = watch(i+1, strings.Repeat("a", i+1)+".exe", "x")
	}
	for _, tc := range []struct {
		name  string
		watch []ActivityWatch
		want  string // substring of the message; "" = valid
	}{
		{"empty list", nil, ""},
		{"plain entry", []ActivityWatch{watch(1, "steam.exe", "Steam")}, ""},
		{"a gap in the slots", []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(3, "obs64.exe", "OBS")}, ""},
		{"slot 5 alone", []ActivityWatch{watch(5, "steam.exe", "Steam")}, ""},
		{"upper-case extension", []ActivityWatch{watch(1, "OBS64.EXE", "OBS")}, ""},
		{"label of 30 characters", []ActivityWatch{watch(1, "x.exe", strings.Repeat("가", 30))}, ""},
		{"same label twice", []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(2, "steamwebhelper.exe", "Steam")}, ""},
		{"five entries", six[:5], ""},
		{"six entries", six, "at most 5"},
		{"no slot", []ActivityWatch{watch(0, "steam.exe", "Steam")}, "must be 1-5 (got 0)"},
		{"slot 6", []ActivityWatch{watch(6, "steam.exe", "Steam")}, "must be 1-5 (got 6)"},
		{"negative slot", []ActivityWatch{watch(-1, "steam.exe", "Steam")}, "must be 1-5"},
		{"slot used twice", []ActivityWatch{watch(2, "steam.exe", "Steam"), watch(2, "obs64.exe", "OBS")}, "slot 2 is used twice"},
		{"missing process", []ActivityWatch{watch(1, "", "Steam")}, "process is required"},
		{"a path", []ActivityWatch{watch(1, `C:\Games\steam.exe`, "Steam")}, "without a path"},
		{"a forward-slash path", []ActivityWatch{watch(1, "games/steam.exe", "Steam")}, "without a path"},
		{"a wildcard", []ActivityWatch{watch(1, "*.exe", "Any")}, "without a path"},
		{"not an exe", []ActivityWatch{watch(1, "steam.bat", "Steam")}, "must end with .exe"},
		{"no extension", []ActivityWatch{watch(1, "steam", "Steam")}, "must end with .exe"},
		{"only the extension", []ActivityWatch{watch(1, ".exe", "X")}, "must end with .exe"},
		{"control character", []ActivityWatch{watch(1, "st\x01eam.exe", "Steam")}, "control characters"},
		{"label too long", []ActivityWatch{watch(1, "x.exe", strings.Repeat("a", 31))}, "at most 30"},
		{"label control character", []ActivityWatch{watch(1, "x.exe", "a\nb")}, "control characters"},
		{"duplicate, other case", []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(2, "Steam.EXE", "S")}, "listed twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ActivityConfig{Enabled: true, Watch: tc.watch}.WithDefaults()
			got := ValidateActivity(a)
			switch {
			case tc.want == "" && got != "":
				t.Errorf("rejected: %s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("message = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

func TestActivityDefaultsFillLabelAndSort(t *testing.T) {
	a := ActivityConfig{Watch: []ActivityWatch{{Slot: 1, Process: "  Discord.exe ", Label: " "}}}.WithDefaults()
	if want := watch(1, "Discord.exe", "Discord"); a.Watch[0] != want {
		t.Errorf("normalized = %+v, want %+v", a.Watch[0], want)
	}
	if msg := ValidateActivity(a); msg != "" {
		t.Errorf("a normalized entry is invalid: %s", msg)
	}
	if (ActivityConfig{}).WithDefaults().Watch == nil {
		t.Error("withDefaults left watch nil (would marshal as null)")
	}
	// The list is kept in slot order, whatever order the client sent.
	in := []ActivityWatch{watch(4, "d.exe", "D"), watch(1, "a.exe", "A"), watch(3, "c.exe", "C")}
	got := ActivityConfig{Watch: in}.WithDefaults().Watch
	if want := []ActivityWatch{watch(1, "a.exe", "A"), watch(3, "c.exe", "C"), watch(4, "d.exe", "D")}; !slices.Equal(got, want) {
		t.Errorf("sorted = %+v, want %+v", got, want)
	}
	if in[0].Slot != 4 {
		t.Error("WithDefaults sorted its argument in place")
	}
}

func procs(watch []ActivityWatch) []string {
	var out []string
	for _, w := range watch {
		out = append(out, w.Process)
	}
	return out
}

func slotsOf(watch []ActivityWatch) []int {
	var out []int
	for _, w := range watch {
		out = append(out, w.Slot)
	}
	return out
}

func TestSanitizeActivity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    []ActivityWatch
		procs []string
		slots []int
	}{
		{
			// A config.json from before slots: 1.. in list order, the
			// first five kept, the bad entries dropped on the way.
			"no slots",
			[]ActivityWatch{
				watch(0, "steam.exe", "Steam"),
				watch(0, `C:\x\bad.exe`, "Bad"),
				watch(0, "STEAM.exe", "Again"),
				watch(0, "obs64.exe", "OBS"),
				watch(0, "a.exe", ""), watch(0, "b.exe", ""), watch(0, "c.exe", ""),
				watch(0, "d.exe", ""), watch(0, "e.exe", ""),
			},
			[]string{"steam.exe", "obs64.exe", "a.exe", "b.exe", "c.exe"},
			[]int{1, 2, 3, 4, 5},
		},
		{
			"valid slots are kept and sorted",
			[]ActivityWatch{watch(4, "d.exe", "D"), watch(2, "b.exe", "B")},
			[]string{"b.exe", "d.exe"},
			[]int{2, 4},
		},
		{
			// A taken, out-of-range or missing slot moves to the lowest
			// free one, in list order; the valid ones stay where they are.
			"repairs around valid slots",
			[]ActivityWatch{
				watch(0, "x.exe", "X"),
				watch(2, "b.exe", "B"),
				watch(2, "again.exe", "Again"),
				watch(9, "nine.exe", "Nine"),
				watch(1, "a.exe", "A"),
			},
			[]string{"a.exe", "b.exe", "x.exe", "again.exe", "nine.exe"},
			[]int{1, 2, 3, 4, 5},
		},
		{
			"no free slot left",
			[]ActivityWatch{
				watch(1, "a.exe", "A"), watch(2, "b.exe", "B"), watch(3, "c.exe", "C"),
				watch(0, "late.exe", "Late"),
				watch(4, "d.exe", "D"), watch(5, "e.exe", "E"),
			},
			[]string{"a.exe", "b.exe", "c.exe", "d.exe", "e.exe"},
			[]int{1, 2, 3, 4, 5},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeActivity(ActivityConfig{Enabled: true, Watch: tc.in})
			if !got.Enabled {
				t.Error("sanitize turned the option off")
			}
			if p, s := procs(got.Watch), slotsOf(got.Watch); !slices.Equal(p, tc.procs) || !slices.Equal(s, tc.slots) {
				t.Errorf("kept %v at %v, want %v at %v", p, s, tc.procs, tc.slots)
			}
			if msg := ValidateActivity(got); msg != "" {
				t.Errorf("sanitized config is still invalid: %s", msg)
			}
		})
	}
	if got := SanitizeActivity(ActivityConfig{}); got.Watch == nil {
		t.Error("an empty list came back nil")
	}
}

func TestLoadConfigMigratesWatchSlots(t *testing.T) {
	dir := t.TempDir()
	// A v1.2.0 development config.json: entries carry "kind" and no
	// "slot". The key is ignored, the bad entry dropped, and the rest get
	// slots 1.. in their order.
	writeConfig(t, dir, `{"port": 5001, "activity": {"enabled": true, "watch": [
		{"process": "code.exe", "label": "VS Code", "kind": "work"},
		{"process": "C:\\Windows\\notepad.exe", "label": "Notepad", "kind": "work"},
		{"process": "steam.exe", "label": "Steam", "kind": "game"}
	]}}`)
	cfg := Load(dir)
	want := []ActivityWatch{watch(1, "code.exe", "VS Code"), watch(2, "steam.exe", "Steam")}
	if !cfg.Activity.Enabled || !slices.Equal(cfg.Activity.Watch, want) {
		t.Errorf("activity = %+v, want %+v", cfg.Activity, want)
	}
	raw, err := json.Marshal(cfg.Activity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "kind") || !strings.Contains(string(raw), `"slot":2`) {
		t.Errorf("saved form = %s (want slots, no kind)", raw)
	}

	// More than five without slots: the first five stay, in slots 1–5.
	var b strings.Builder
	b.WriteString(`{"port": 5001, "activity": {"enabled": true, "watch": [`)
	for i := 0; i < 7; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"process": "app` + string(rune('a'+i)) + `.exe"}`)
	}
	b.WriteString(`]}}`)
	writeConfig(t, dir, b.String())
	cfg = Load(dir)
	if p := procs(cfg.Activity.Watch); !slices.Equal(p, []string{"appa.exe", "appb.exe", "appc.exe", "appd.exe", "appe.exe"}) ||
		!slices.Equal(slotsOf(cfg.Activity.Watch), []int{1, 2, 3, 4, 5}) {
		t.Errorf("7 entries loaded as %+v", cfg.Activity.Watch)
	}

	// A saved list keeps its slots, gaps included.
	writeConfig(t, dir, `{"port": 5001, "activity": {"enabled": true, "watch": [
		{"slot": 3, "process": "obs64.exe", "label": "OBS"},
		{"slot": 1, "process": "steam.exe", "label": "Steam"}
	]}}`)
	cfg = Load(dir)
	if want := []ActivityWatch{watch(1, "steam.exe", "Steam"), watch(3, "obs64.exe", "OBS")}; !slices.Equal(cfg.Activity.Watch, want) {
		t.Errorf("slotted list loaded as %+v, want %+v", cfg.Activity.Watch, want)
	}

	// An older config.json without the key: off, empty list.
	writeConfig(t, dir, `{"port": 5001}`)
	cfg = Load(dir)
	if cfg.Activity.Enabled || cfg.Activity.Watch == nil || len(cfg.Activity.Watch) != 0 {
		t.Errorf("default activity = %+v", cfg.Activity)
	}
}
