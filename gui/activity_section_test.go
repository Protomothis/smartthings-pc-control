package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

func TestNormalizeActivity(t *testing.T) {
	got := normalizeActivity(ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		{Process: " Steam.exe ", Label: ""},
		{Process: "", Label: " "}, // a blank row the user added
		{Process: "obs64.exe", Label: " OBS "},
	}})
	want := []ActivityWatch{
		{Process: "Steam.exe", Label: "Steam"},
		{Process: "obs64.exe", Label: "OBS"},
	}
	if !got.Enabled || !slices.Equal(got.Watch, want) {
		t.Errorf("normalized = %+v, want %+v", got, want)
	}
	if normalizeActivity(ActivityConfig{}).Watch == nil {
		t.Error("an empty list became nil (the service reads null as 'keep')")
	}
}

func TestActivityProblem(t *testing.T) {
	if activityMaxWatch != config.ActivityMaxWatch {
		t.Fatalf("activityMaxWatch = %d, the service caps at %d", activityMaxWatch, config.ActivityMaxWatch)
	}
	many := make([]ActivityWatch, 11)
	for i := range many {
		many[i] = ActivityWatch{Process: strings.Repeat("a", i+1) + ".exe"}
	}
	for _, tc := range []struct {
		name  string
		watch []ActivityWatch
		key   string // "" = fine
	}{
		{"ok", []ActivityWatch{{Process: "steam.exe", Label: "Steam"}}, ""},
		{"blank rows are ignored", []ActivityWatch{{}}, ""},
		{"ten", many[:10], ""},
		{"label only", []ActivityWatch{{Label: "Steam"}}, "activity.err.process"},
		{"path", []ActivityWatch{{Process: `C:\Games\steam.exe`}}, "activity.err.path"},
		{"not exe", []ActivityWatch{{Process: "steam"}}, "activity.err.exe"},
		{"label too long", []ActivityWatch{{Process: "a.exe", Label: strings.Repeat("x", 31)}}, "activity.err.label"},
		{"duplicate", []ActivityWatch{{Process: "a.exe"}, {Process: "A.EXE"}}, "activity.err.dup"},
		{"eleven", many, "activity.err.max"},
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
	watch := []ActivityWatch{{Process: "steam.exe", Label: "Steam"}}
	got, ok := addActivityProcess(watch, "Discord.exe")
	if !ok || len(got) != 2 || got[1] != (ActivityWatch{Process: "Discord.exe", Label: "Discord"}) {
		t.Errorf("added = %+v ok=%v (a pick goes to the bottom, lowest priority)", got, ok)
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

func TestMoveActivity(t *testing.T) {
	a, b, c := ActivityWatch{Process: "a.exe"}, ActivityWatch{Process: "b.exe"}, ActivityWatch{Process: "c.exe"}
	watch := []ActivityWatch{a, b, c}
	for _, tc := range []struct {
		name     string
		i, delta int
		want     []ActivityWatch
		moved    bool
	}{
		{"second up", 1, -1, []ActivityWatch{b, a, c}, true},
		{"second down", 1, 1, []ActivityWatch{a, c, b}, true},
		{"last up", 2, -1, []ActivityWatch{a, c, b}, true},
		{"first up", 0, -1, watch, false},
		{"last down", 2, 1, watch, false},
		{"out of range", 5, -1, watch, false},
		{"negative index", -1, 1, watch, false},
		{"a jump is not a move", 0, 2, watch, false},
	} {
		got, moved := moveActivity(watch, tc.i, tc.delta)
		if moved != tc.moved || !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v moved=%v, want %v moved=%v", tc.name, got, moved, tc.want, tc.moved)
		}
	}
	if !slices.Equal(watch, []ActivityWatch{a, b, c}) {
		t.Error("moveActivity wrote into its argument")
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

func TestShareFormCarriesActivity(t *testing.T) {
	base := stBaseConfig()
	base.Activity = ActivityConfig{Watch: []ActivityWatch{
		{Process: "steam.exe", Label: "Steam"},
		{Process: "obs64.exe", Label: "OBS"},
	}}
	s := shareStateFromConfig(base)
	if s.dirty(base) {
		t.Fatal("a freshly filled section is dirty")
	}
	s.Activity.Watch[0].Label = "Changed"
	if base.Activity.Watch[0].Label != "Steam" {
		t.Error("editing the form wrote into the baseline")
	}
	for _, mutate := range []func(*shareFormState){
		func(s *shareFormState) { s.Activity.Enabled = true },
		func(s *shareFormState) { s.Activity.Watch[0].Label = "Valve" },
		func(s *shareFormState) { s.Activity.Watch, _ = moveActivity(s.Activity.Watch, 1, -1) },
		func(s *shareFormState) { s.Activity.Watch = nil },
		func(s *shareFormState) {
			s.Activity.Watch, _ = addActivityProcess(s.Activity.Watch, "code.exe")
		},
	} {
		changed := shareStateFromConfig(base)
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
	// The saved order is the edited order.
	moved := shareStateFromConfig(base)
	moved.Activity.Watch, _ = moveActivity(moved.Activity.Watch, 1, -1)
	if got := moved.applyTo(base).Activity.Watch; got[0].Process != "obs64.exe" || got[1].Process != "steam.exe" {
		t.Errorf("saved order = %+v", got)
	}
	// A blank row the user added and left empty is not a change.
	s = shareStateFromConfig(base)
	s.Activity.Watch = append(s.Activity.Watch, ActivityWatch{})
	if s.dirty(base) {
		t.Error("an empty row counts as a change")
	}
}

func TestActivityConfigRoundTrip(t *testing.T) {
	// The service's JSON shape decodes into the mirror and back unchanged;
	// a "kind" from a v1.2.0 development service is dropped.
	raw := `{"port":5001,"activity":{"enabled":true,"watch":[{"process":"steam.exe","label":"Steam","kind":"game"},{"process":"obs64.exe","label":"OBS"}]}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Activity.Enabled || len(cfg.Activity.Watch) != 2 || cfg.Activity.Watch[0].Process != "steam.exe" {
		t.Fatalf("decoded = %+v", cfg.Activity)
	}
	out, _ := json.Marshal(cfg)
	if !strings.Contains(string(out), `"activity":{"enabled":true,"watch":[{"process":"steam.exe","label":"Steam"},{"process":"obs64.exe","label":"OBS"}]}`) {
		t.Errorf("encoded = %s", out)
	}
}

func TestClientRunningProcesses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/processes" || r.Method != http.MethodGet {
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
