package config

// The watch list's rules and its load path (#110, #123). Moved here from
// the service package with the code (#127).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func watch(process, label string) ActivityWatch {
	return ActivityWatch{Process: process, Label: label}
}

func TestValidateActivity(t *testing.T) {
	eleven := make([]ActivityWatch, 11)
	for i := range eleven {
		eleven[i] = watch(strings.Repeat("a", i+1)+".exe", "x")
	}
	for _, tc := range []struct {
		name  string
		watch []ActivityWatch
		want  string // substring of the message; "" = valid
	}{
		{"empty list", nil, ""},
		{"plain entry", []ActivityWatch{watch("steam.exe", "Steam")}, ""},
		{"upper-case extension", []ActivityWatch{watch("OBS64.EXE", "OBS")}, ""},
		{"label of 30 characters", []ActivityWatch{watch("x.exe", strings.Repeat("가", 30))}, ""},
		{"same label twice", []ActivityWatch{watch("steam.exe", "Steam"), watch("steamwebhelper.exe", "Steam")}, ""},
		{"ten entries", eleven[:10], ""},
		{"eleven entries", eleven, "at most 10"},
		{"missing process", []ActivityWatch{watch("", "Steam")}, "process is required"},
		{"a path", []ActivityWatch{watch(`C:\Games\steam.exe`, "Steam")}, "without a path"},
		{"a forward-slash path", []ActivityWatch{watch("games/steam.exe", "Steam")}, "without a path"},
		{"a wildcard", []ActivityWatch{watch("*.exe", "Any")}, "without a path"},
		{"not an exe", []ActivityWatch{watch("steam.bat", "Steam")}, "must end with .exe"},
		{"no extension", []ActivityWatch{watch("steam", "Steam")}, "must end with .exe"},
		{"only the extension", []ActivityWatch{watch(".exe", "X")}, "must end with .exe"},
		{"control character", []ActivityWatch{watch("st\x01eam.exe", "Steam")}, "control characters"},
		{"label too long", []ActivityWatch{watch("x.exe", strings.Repeat("a", 31))}, "at most 30"},
		{"label control character", []ActivityWatch{watch("x.exe", "a\nb")}, "control characters"},
		{"duplicate, other case", []ActivityWatch{watch("steam.exe", "Steam"), watch("Steam.EXE", "S")}, "listed twice"},
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

func TestActivityDefaultsFillLabel(t *testing.T) {
	a := ActivityConfig{Watch: []ActivityWatch{{Process: "  Discord.exe ", Label: " "}}}.WithDefaults()
	if want := watch("Discord.exe", "Discord"); a.Watch[0] != want {
		t.Errorf("normalized = %+v, want %+v", a.Watch[0], want)
	}
	if msg := ValidateActivity(a); msg != "" {
		t.Errorf("a normalized entry is invalid: %s", msg)
	}
	if (ActivityConfig{}).WithDefaults().Watch == nil {
		t.Error("withDefaults left watch nil (would marshal as null)")
	}
}

func TestSanitizeActivityKeepsOrderAndCaps(t *testing.T) {
	in := ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		watch("steam.exe", "Steam"),
		watch(`C:\x\bad.exe`, "Bad"),
		watch("STEAM.exe", "Again"),
		watch("obs64.exe", "OBS"),
	}}
	for i := 0; i < 15; i++ {
		in.Watch = append(in.Watch, watch(strings.Repeat("p", i+1)+".exe", "P"))
	}
	got := SanitizeActivity(in)
	if !got.Enabled {
		t.Error("sanitize turned the option off")
	}
	if len(got.Watch) != ActivityMaxWatch || ActivityMaxWatch != 10 {
		t.Fatalf("kept %d entries, want the cap 10 (ActivityMaxWatch = %d)", len(got.Watch), ActivityMaxWatch)
	}
	// Order is priority: the valid entries keep theirs, the first ten win.
	want := []string{"steam.exe", "obs64.exe", "p.exe", "pp.exe", "ppp.exe", "pppp.exe",
		"ppppp.exe", "pppppp.exe", "ppppppp.exe", "pppppppp.exe"}
	var procs []string
	for _, w := range got.Watch {
		procs = append(procs, w.Process)
	}
	if !slices.Equal(procs, want) {
		t.Errorf("kept %v, want %v", procs, want)
	}
	if msg := ValidateActivity(got); msg != "" {
		t.Errorf("sanitized config is still invalid: %s", msg)
	}
}

func TestLoadConfigDropsKindKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	// A v1.2.0 development config.json: entries still carry "kind", and
	// the order was not meant as a priority. The key is ignored, the
	// order kept, the bad entry dropped.
	writeConfig(t, dir, `{"port": 5001, "activity": {"enabled": true, "watch": [
		{"process": "code.exe", "label": "VS Code", "kind": "work"},
		{"process": "C:\\Windows\\notepad.exe", "label": "Notepad", "kind": "work"},
		{"process": "steam.exe", "label": "Steam", "kind": "game"}
	]}}`)
	cfg := Load(dir)
	want := []ActivityWatch{watch("code.exe", "VS Code"), watch("steam.exe", "Steam")}
	if !cfg.Activity.Enabled || !slices.Equal(cfg.Activity.Watch, want) {
		t.Errorf("activity = %+v, want %+v", cfg.Activity, want)
	}
	raw, err := json.Marshal(cfg.Activity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "kind") {
		t.Errorf("kind survives into the saved form: %s", raw)
	}

	// More than ten: the first ten stay.
	var b strings.Builder
	b.WriteString(`{"port": 5001, "activity": {"enabled": true, "watch": [`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"process": "app` + string(rune('a'+i)) + `.exe"}`)
	}
	b.WriteString(`]}}`)
	writeConfig(t, dir, b.String())
	cfg = Load(dir)
	if n := len(cfg.Activity.Watch); n != 10 || cfg.Activity.Watch[0].Process != "appa.exe" || cfg.Activity.Watch[9].Process != "appj.exe" {
		t.Errorf("12 entries loaded as %+v", cfg.Activity.Watch)
	}

	// An older config.json without the key: off, empty list.
	writeConfig(t, dir, `{"port": 5001}`)
	cfg = Load(dir)
	if cfg.Activity.Enabled || cfg.Activity.Watch == nil || len(cfg.Activity.Watch) != 0 {
		t.Errorf("default activity = %+v", cfg.Activity)
	}
}
