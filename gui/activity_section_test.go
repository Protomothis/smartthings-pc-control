package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeActivity(t *testing.T) {
	got := normalizeActivity(ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		{Process: " Steam.exe ", Label: "", Kind: ""},
		{Process: "", Label: " ", Kind: "game"}, // a blank row the user added
		{Process: "obs64.exe", Label: " OBS ", Kind: "STREAM"},
	}})
	want := []ActivityWatch{
		{Process: "Steam.exe", Label: "Steam", Kind: "other"},
		{Process: "obs64.exe", Label: "OBS", Kind: "stream"},
	}
	if !got.Enabled || !slices.Equal(got.Watch, want) {
		t.Errorf("normalized = %+v, want %+v", got, want)
	}
	if normalizeActivity(ActivityConfig{}).Watch == nil {
		t.Error("an empty list became nil (the service reads null as 'keep')")
	}
}

func TestActivityProblem(t *testing.T) {
	many := make([]ActivityWatch, 21)
	for i := range many {
		many[i] = ActivityWatch{Process: strings.Repeat("a", i+1) + ".exe", Kind: "other"}
	}
	for _, tc := range []struct {
		name  string
		watch []ActivityWatch
		key   string // "" = fine
	}{
		{"ok", []ActivityWatch{{Process: "steam.exe", Label: "Steam", Kind: "game"}}, ""},
		{"blank rows are ignored", []ActivityWatch{{}}, ""},
		{"label only", []ActivityWatch{{Label: "Steam", Kind: "game"}}, "activity.err.process"},
		{"path", []ActivityWatch{{Process: `C:\Games\steam.exe`, Kind: "game"}}, "activity.err.path"},
		{"not exe", []ActivityWatch{{Process: "steam", Kind: "game"}}, "activity.err.exe"},
		{"label too long", []ActivityWatch{{Process: "a.exe", Label: strings.Repeat("x", 31), Kind: "game"}}, "activity.err.label"},
		{"bad kind", []ActivityWatch{{Process: "a.exe", Kind: "nap"}}, "activity.err.kind"},
		{"duplicate", []ActivityWatch{{Process: "a.exe", Kind: "game"}, {Process: "A.EXE", Kind: "work"}}, "activity.err.dup"},
		{"too many", many, "activity.err.max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := activityProblem(LangEn, ActivityConfig{Watch: tc.watch})
			if tc.key == "" {
				if got != "" {
					t.Errorf("rejected: %s", got)
				}
				return
			}
			// The longest literal stretch of the message's format string.
			fragment := ""
			for _, part := range regexp.MustCompile(`%[sdv]`).Split(T(LangEn, tc.key), -1) {
				if len(part) > len(fragment) {
					fragment = part
				}
			}
			if got == "" || !strings.Contains(got, fragment) {
				t.Errorf("message = %q, want %s", got, tc.key)
			}
		})
	}
}

func TestAddActivityProcess(t *testing.T) {
	watch := []ActivityWatch{{Process: "steam.exe", Label: "Steam", Kind: "game"}}
	got, ok := addActivityProcess(watch, "Discord.exe")
	if !ok || len(got) != 2 || got[1] != (ActivityWatch{Process: "Discord.exe", Label: "Discord", Kind: "other"}) {
		t.Errorf("added = %+v ok=%v", got, ok)
	}
	if len(watch) != 1 {
		t.Error("addActivityProcess wrote into its argument")
	}
	if _, ok := addActivityProcess(watch, "STEAM.EXE"); ok {
		t.Error("a listed program (other case) was added again")
	}
	full := make([]ActivityWatch, activityMaxWatch)
	if _, ok := addActivityProcess(full, "x.exe"); ok {
		t.Error("a full list grew")
	}
}

func TestPickerCandidates(t *testing.T) {
	running := []string{"Code.exe", "explorer.exe", "Steam.exe", "steamwebhelper.exe"}
	watch := []ActivityWatch{{Process: "steam.exe"}}
	if got := pickerCandidates(running, watch, ""); !slices.Equal(got, []string{"Code.exe", "explorer.exe", "steamwebhelper.exe"}) {
		t.Errorf("unfiltered = %v", got)
	}
	if got := pickerCandidates(running, watch, " STEAM "); !slices.Equal(got, []string{"steamwebhelper.exe"}) {
		t.Errorf("filtered = %v", got)
	}
}

func TestSTFormCarriesActivity(t *testing.T) {
	base := stBaseConfig()
	base.Activity = ActivityConfig{Watch: []ActivityWatch{{Process: "steam.exe", Label: "Steam", Kind: "game"}}}
	s := stStateFromConfig(base)
	if s.dirty(base) {
		t.Fatal("a freshly filled section is dirty")
	}
	s.Activity.Watch[0].Label = "Changed"
	if base.Activity.Watch[0].Label != "Steam" {
		t.Error("editing the form wrote into the baseline")
	}
	for _, mutate := range []func(*stFormState){
		func(s *stFormState) { s.Activity.Enabled = true },
		func(s *stFormState) { s.Activity.Watch[0].Kind = "work" },
		func(s *stFormState) { s.Activity.Watch = nil },
		func(s *stFormState) {
			s.Activity.Watch, _ = addActivityProcess(s.Activity.Watch, "obs64.exe")
		},
	} {
		changed := stStateFromConfig(base)
		mutate(&changed)
		if !changed.dirty(base) {
			t.Errorf("an edit went unnoticed: %+v", changed.Activity)
		}
		saved := changed.applyTo(base)
		if changed.dirty(saved) {
			t.Errorf("still dirty after saving %+v", changed.Activity)
		}
		if saved.Activity.Watch == nil {
			t.Error("applyTo sent a null watch list")
		}
	}
	// A blank row the user added and left empty is not a change.
	s = stStateFromConfig(base)
	s.Activity.Watch = append(s.Activity.Watch, ActivityWatch{Kind: "other"})
	if s.dirty(base) {
		t.Error("an empty row counts as a change")
	}
}

func TestActivityConfigRoundTrip(t *testing.T) {
	// The service's JSON shape decodes into the mirror and back unchanged.
	raw := `{"port":5001,"activity":{"enabled":true,"watch":[{"process":"steam.exe","label":"Steam","kind":"game"}]}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Activity.Enabled || len(cfg.Activity.Watch) != 1 || cfg.Activity.Watch[0].Kind != "game" {
		t.Fatalf("decoded = %+v", cfg.Activity)
	}
	out, _ := json.Marshal(cfg)
	if !strings.Contains(string(out), `"activity":{"enabled":true,"watch":[{"process":"steam.exe","label":"Steam","kind":"game"}]}`) {
		t.Errorf("encoded = %s", out)
	}
}

func TestClientRunningProcesses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/processes" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"processes":["Code.exe","steam.exe"]}`))
	}))
	defer srv.Close()
	c := NewClient(0)
	c.base = srv.URL
	got, err := c.RunningProcesses()
	if err != nil || !slices.Equal(got, []string{"Code.exe", "steam.exe"}) {
		t.Errorf("RunningProcesses = %v, %v", got, err)
	}
}

func TestActivityTranslations(t *testing.T) {
	keys := []string{
		"activity.toggle", "activity.hint", "activity.watch", "activity.watch.hint",
		"activity.col.process", "activity.col.label", "activity.col.kind", "activity.ph.label",
		"activity.add", "activity.pick", "activity.empty",
		"activity.pick.title", "activity.pick.search", "activity.pick.hint", "activity.pick.none",
		"activity.pick.close", "activity.pick.fail",
		"activity.err.max", "activity.err.process", "activity.err.path", "activity.err.exe",
		"activity.err.label", "activity.err.kind", "activity.err.dup",
	}
	for _, k := range activityKinds {
		keys = append(keys, "activity.kind."+k)
	}
	for _, key := range keys {
		for _, l := range []Lang{LangKo, LangEn} {
			if T(l, key) == key {
				t.Errorf("missing %s translation for %s", l, key)
			}
		}
	}
	if got := activityKindLabels(LangKo); len(got) != len(activityKinds) || got[0] != "게임" {
		t.Errorf("kind labels = %v", got)
	}
}
