package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

func aw(slot int, process, label string) ActivityWatch {
	return ActivityWatch{Slot: slot, Process: process, Label: label}
}

func TestNormalizeActivity(t *testing.T) {
	got := normalizeActivity(ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		aw(4, "obs64.exe", " OBS "),
		aw(2, "", " "), // a blank row the user added
		aw(1, " Steam.exe ", ""),
	}})
	// Trimmed, labelled, the blank row gone, in slot order.
	want := []ActivityWatch{aw(1, "Steam.exe", "Steam"), aw(4, "obs64.exe", "OBS")}
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
	many := make([]ActivityWatch, 6)
	for i := range many {
		many[i] = aw(i+1, strings.Repeat("a", i+1)+".exe", "")
	}
	for _, tc := range []struct {
		name  string
		watch []ActivityWatch
		key   string // "" = fine
	}{
		{"ok", []ActivityWatch{aw(1, "steam.exe", "Steam")}, ""},
		{"a gap", []ActivityWatch{aw(2, "steam.exe", "Steam"), aw(5, "obs64.exe", "")}, ""},
		{"blank rows are ignored", []ActivityWatch{aw(1, "", "")}, ""},
		{"five", many[:5], ""},
		{"no slot", []ActivityWatch{aw(0, "steam.exe", "")}, "activity.err.slot"},
		{"slot 6", []ActivityWatch{aw(6, "steam.exe", "")}, "activity.err.slot"},
		{"slot twice", []ActivityWatch{aw(2, "a.exe", ""), aw(2, "b.exe", "")}, "activity.err.slotdup"},
		{"label only", []ActivityWatch{aw(1, "", "Steam")}, "activity.err.process"},
		{"path", []ActivityWatch{aw(1, `C:\Games\steam.exe`, "")}, "activity.err.path"},
		{"not exe", []ActivityWatch{aw(1, "steam", "")}, "activity.err.exe"},
		{"label too long", []ActivityWatch{aw(1, "a.exe", strings.Repeat("x", 31))}, "activity.err.label"},
		{"duplicate", []ActivityWatch{aw(1, "a.exe", ""), aw(2, "A.EXE", "")}, "activity.err.dup"},
		{"six", many, "activity.err.max"},
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

func TestActivityFreeSlotAndChoices(t *testing.T) {
	watch := []ActivityWatch{aw(1, "a.exe", ""), aw(3, "c.exe", ""), aw(4, "d.exe", "")}
	if got := activityFreeSlot(watch); got != 2 {
		t.Errorf("free slot = %d, want 2", got)
	}
	if got := activityFreeSlot(nil); got != 1 {
		t.Errorf("free slot of an empty list = %d, want 1", got)
	}
	full := []ActivityWatch{aw(1, "", ""), aw(2, "", ""), aw(3, "", ""), aw(4, "", ""), aw(5, "", "")}
	if got := activityFreeSlot(full); got != 0 {
		t.Errorf("free slot of a full list = %d, want 0", got)
	}
	// A row is offered its own slot and the free ones, never another row's.
	if got := activitySlotChoices(watch, 1); !slices.Equal(got, []int{2, 3, 5}) {
		t.Errorf("choices for the slot-3 row = %v, want [2 3 5]", got)
	}
	if got := activitySlotChoices(full, 4); !slices.Equal(got, []int{5}) {
		t.Errorf("choices in a full list = %v, want [5]", got)
	}
}

func TestSetActivitySlot(t *testing.T) {
	watch := []ActivityWatch{aw(1, "a.exe", "A"), aw(3, "c.exe", "C")}
	got, ok := setActivitySlot(watch, 0, 5)
	if !ok || !slices.Equal(got, []ActivityWatch{aw(3, "c.exe", "C"), aw(5, "a.exe", "A")}) {
		t.Errorf("moved = %+v ok=%v (want slot order)", got, ok)
	}
	if watch[0].Slot != 1 {
		t.Error("setActivitySlot wrote into its argument")
	}
	for _, tc := range []struct {
		name    string
		i, slot int
	}{
		{"taken by another row", 0, 3},
		{"its own slot", 0, 1},
		{"out of range", 0, 6},
		{"no such row", 2, 2},
	} {
		if got, ok := setActivitySlot(watch, tc.i, tc.slot); ok || !slices.Equal(got, watch) {
			t.Errorf("%s: got %+v ok=%v", tc.name, got, ok)
		}
	}
}

func TestAddActivityRowAndProcess(t *testing.T) {
	watch := []ActivityWatch{aw(1, "steam.exe", "Steam"), aw(3, "obs64.exe", "OBS")}
	got, ok := addActivityProcess(watch, "Discord.exe")
	// A pick takes the lowest free slot, 2, and lands in slot order.
	want := []ActivityWatch{aw(1, "steam.exe", "Steam"), aw(2, "Discord.exe", "Discord"), aw(3, "obs64.exe", "OBS")}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("picked = %+v ok=%v, want %+v", got, ok, want)
	}
	if len(watch) != 2 {
		t.Error("addActivityProcess wrote into its argument")
	}
	if _, ok := addActivityProcess(watch, "STEAM.EXE"); ok {
		t.Error("a listed program (other case) was added again")
	}
	if got, ok := addActivityRow(watch); !ok || len(got) != 3 || got[1] != (ActivityWatch{Slot: 2}) {
		t.Errorf("blank row = %+v ok=%v (want it in slot 2)", got, ok)
	}
	full := []ActivityWatch{aw(1, "a.exe", ""), aw(2, "b.exe", ""), aw(3, "c.exe", ""), aw(4, "d.exe", ""), aw(5, "e.exe", "")}
	if _, ok := addActivityProcess(full, "x.exe"); ok {
		t.Error("a full list grew by a pick")
	}
	if _, ok := addActivityRow(full); ok {
		t.Error("a full list grew by a blank row")
	}
}

func TestPickerCandidates(t *testing.T) {
	running := []string{"Code.exe", "explorer.exe", "Steam.exe", "steamwebhelper.exe"}
	watch := []ActivityWatch{aw(1, "steam.exe", "")}
	if got := pickerCandidates(running, watch, ""); !slices.Equal(got, []string{"Code.exe", "explorer.exe", "steamwebhelper.exe"}) {
		t.Errorf("unfiltered = %v", got)
	}
	if got := pickerCandidates(running, watch, " STEAM "); !slices.Equal(got, []string{"steamwebhelper.exe"}) {
		t.Errorf("filtered = %v", got)
	}
}

func TestShareFormCarriesActivity(t *testing.T) {
	base := stBaseConfig()
	base.Activity = ActivityConfig{Watch: []ActivityWatch{aw(1, "steam.exe", "Steam"), aw(3, "obs64.exe", "OBS")}}
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
		func(s *shareFormState) { s.Activity.Watch, _ = setActivitySlot(s.Activity.Watch, 1, 2) },
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
	// The saved list carries the slots, in slot order.
	moved := shareStateFromConfig(base)
	moved.Activity.Watch, _ = setActivitySlot(moved.Activity.Watch, 0, 5)
	if got := moved.applyTo(base).Activity.Watch; !slices.Equal(got, []ActivityWatch{aw(3, "obs64.exe", "OBS"), aw(5, "steam.exe", "Steam")}) {
		t.Errorf("saved = %+v", got)
	}
	// A blank row the user added and left empty is not a change.
	s = shareStateFromConfig(base)
	s.Activity.Watch, _ = addActivityRow(s.Activity.Watch)
	if s.dirty(base) {
		t.Error("an empty row counts as a change")
	}
}

func TestActivityConfigRoundTrip(t *testing.T) {
	// The service's JSON shape decodes into the mirror and back unchanged;
	// a "kind" from a v1.2.0 development service is dropped.
	raw := `{"port":5001,"activity":{"enabled":true,"watch":[{"slot":1,"process":"steam.exe","label":"Steam","kind":"game"},{"slot":3,"process":"obs64.exe","label":"OBS"}]}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Activity.Enabled || len(cfg.Activity.Watch) != 2 || cfg.Activity.Watch[1] != aw(3, "obs64.exe", "OBS") {
		t.Fatalf("decoded = %+v", cfg.Activity)
	}
	out, _ := json.Marshal(cfg)
	if !strings.Contains(string(out), `"activity":{"enabled":true,"watch":[{"slot":1,"process":"steam.exe","label":"Steam"},{"slot":3,"process":"obs64.exe","label":"OBS"}]}`) {
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

// activityRowSelect is the slot selector of editor row i.
func activityRowSelect(t *testing.T, a *activityBox, i int) *widget.Select {
	t.Helper()
	var found *widget.Select
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Select:
			if found == nil {
				found = v
			}
		case *fyne.Container:
			for _, c := range v.Objects {
				walk(c)
			}
		}
	}
	walk(a.rows.Objects[i])
	if found == nil {
		t.Fatalf("row %d has no slot selector", i)
	}
	return found
}

// TestActivityEditorSlots drives the real editor: rows come in slot order,
// each selector offers its own and the free slots, choosing one re-sorts
// the rows, adding takes the lowest free slot and stops at five.
func TestActivityEditorSlots(t *testing.T) {
	u := newTestUI(t, LangKo, nil)
	a := &u.share.activity
	u.fillShareTab(Config{Activity: ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		aw(4, "obs64.exe", "OBS"), aw(1, "steam.exe", "Steam"),
	}}})
	if !slices.Equal(a.watch, []ActivityWatch{aw(1, "steam.exe", "Steam"), aw(4, "obs64.exe", "OBS")}) {
		t.Fatalf("filled rows = %+v, want slot order", a.watch)
	}
	sel := activityRowSelect(t, a, 1)
	if want := []string{"감시 2", "감시 3", "감시 4", "감시 5"}; !slices.Equal(sel.Options, want) || sel.Selected != "감시 4" {
		t.Errorf("row 2 offers %v (selected %q), want %v with 감시 4", sel.Options, sel.Selected, want)
	}
	if a.addBtn.Disabled() || a.pickBtn.Disabled() {
		t.Error("add/pick disabled with three free slots")
	}

	// OBS to slot 2: the entry moves, and slot 2 leaves Steam's choices.
	sel.SetSelected("감시 2")
	if !slices.Equal(a.watch, []ActivityWatch{aw(1, "steam.exe", "Steam"), aw(2, "obs64.exe", "OBS")}) {
		t.Errorf("after choosing 감시 2: %+v", a.watch)
	}
	if got := activityRowSelect(t, a, 0).Options; !slices.Equal(got, []string{"감시 1", "감시 3", "감시 4", "감시 5"}) {
		t.Errorf("row 1 offers %v after the move (2 is taken now)", got)
	}
	// Steam to slot 5 puts it last.
	activityRowSelect(t, a, 0).SetSelected("감시 5")
	if !slices.Equal(a.watch, []ActivityWatch{aw(2, "obs64.exe", "OBS"), aw(5, "steam.exe", "Steam")}) {
		t.Errorf("after moving Steam to 감시 5: %+v", a.watch)
	}

	// [추가] fills the lowest free slot, then 3, 4; at five it is disabled.
	for _, want := range []int{1, 3, 4} {
		test.Tap(a.addBtn)
		if !slices.ContainsFunc(a.watch, func(w ActivityWatch) bool { return w.Slot == want && w.Process == "" }) {
			t.Fatalf("add did not fill slot %d: %+v", want, a.watch)
		}
	}
	if len(a.watch) != 5 || len(a.rows.Objects) != 5 {
		t.Fatalf("%d entries, %d rows", len(a.watch), len(a.rows.Objects))
	}
	if !a.addBtn.Disabled() || !a.pickBtn.Disabled() {
		t.Error("add/pick still enabled with all five slots used")
	}
	test.Tap(a.addBtn)
	if len(a.watch) != 5 {
		t.Error("a sixth row was added")
	}
}
