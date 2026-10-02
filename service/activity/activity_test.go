package activity

// Tests for the matcher and the scanner (#110, #123), moved here with the
// code (#127). The status block, the push and /api/processes are tested
// against the real service in the root package.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

func watch(slot int, process, label string) config.ActivityWatch {
	return config.ActivityWatch{Slot: slot, Process: process, Label: label}
}

func app(slot int, id, label string, running bool) status.ActivityApp {
	return status.ActivityApp{Slot: slot, ID: id, Label: label, Running: running}
}

// scannerOver is a scanner over a fixed process list; the counter says how
// often the list was read.
func scannerOver(names ...string) (*Scanner, *atomic.Int32) {
	calls := &atomic.Int32{}
	return &Scanner{List: func() ([]string, error) {
		calls.Add(1)
		return slices.Clone(names), nil
	}}, calls
}

// scannerRunning reads the list from a variable the test changes.
func scannerRunning(running *[]string) *Scanner {
	return &Scanner{List: func() ([]string, error) { return slices.Clone(*running), nil }}
}

func TestMatchActivity(t *testing.T) {
	// Out of slot order on purpose: the apps come out sorted by slot.
	list := []config.ActivityWatch{
		watch(5, "code.exe", "VS Code"),
		watch(1, "Steam.exe", "Steam"),
		watch(3, "obs64.exe", "OBS"),
	}
	for _, tc := range []struct {
		name    string
		running []string
		apps    []status.ActivityApp
		top     string
	}{
		{"nothing running", nil,
			[]status.ActivityApp{app(1, "steam.exe", "Steam", false), app(3, "obs64.exe", "OBS", false), app(5, "code.exe", "VS Code", false)}, ""},
		{"nothing watched running", []string{"explorer.exe", "svchost.exe"},
			[]status.ActivityApp{app(1, "steam.exe", "Steam", false), app(3, "obs64.exe", "OBS", false), app(5, "code.exe", "VS Code", false)}, ""},
		{"case-insensitive, id lower-cased", []string{"STEAM.EXE"},
			[]status.ActivityApp{app(1, "steam.exe", "Steam", true), app(3, "obs64.exe", "OBS", false), app(5, "code.exe", "VS Code", false)}, "steam.exe"},
		{"many instances count once", []string{"code.exe", "Code.exe", "CODE.EXE"},
			[]status.ActivityApp{app(1, "steam.exe", "Steam", false), app(3, "obs64.exe", "OBS", false), app(5, "code.exe", "VS Code", true)}, "code.exe"},
		{"top is the running app in the lowest slot", []string{"code.exe", "obs64.exe"},
			[]status.ActivityApp{app(1, "steam.exe", "Steam", false), app(3, "obs64.exe", "OBS", true), app(5, "code.exe", "VS Code", true)}, "obs64.exe"},
		{"all running", []string{"code.exe", "obs64.exe", "steam.exe"},
			[]status.ActivityApp{app(1, "steam.exe", "Steam", true), app(3, "obs64.exe", "OBS", true), app(5, "code.exe", "VS Code", true)}, "steam.exe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(list, tc.running)
			if !got.Enabled || got.Top != tc.top || !slices.Equal(got.Apps, tc.apps) {
				t.Errorf("got %+v, want apps %+v top %q", got, tc.apps, tc.top)
			}
		})
	}

	// Moving an app to a lower slot makes it the top; the list order
	// itself does not matter.
	moved := []config.ActivityWatch{watch(4, "Steam.exe", "Steam"), watch(2, "code.exe", "VS Code")}
	if got := Match(moved, []string{"steam.exe", "code.exe"}); got.Top != "code.exe" || got.Apps[0].ID != "code.exe" || got.Apps[0].Slot != 2 {
		t.Errorf("moved = %+v", got)
	}
	if list[0].Slot != 5 {
		t.Error("Match sorted its argument in place")
	}
	if got := Match(nil, []string{"steam.exe"}); got.Apps == nil || len(got.Apps) != 0 || got.Top != "" {
		t.Errorf("empty list = %+v", got)
	}
}

func TestMatchActivityNeverCarriesUnlistedNames(t *testing.T) {
	list := []config.ActivityWatch{watch(1, "steam.exe", "Steam")}
	running := []string{"steam.exe", "secret-project.exe", "bank-client.exe"}
	raw, _ := json.Marshal(Match(list, running))
	if s := string(raw); strings.Contains(s, "secret") || strings.Contains(s, "bank") {
		t.Errorf("result carries an unlisted name: %s", s)
	}
}

func TestActivityTopLabel(t *testing.T) {
	a := status.Activity{Enabled: true, Top: "obs64.exe", Apps: []status.ActivityApp{
		app(1, "steam.exe", "Steam", false), app(3, "obs64.exe", "OBS", true), app(5, "code.exe", "VS Code", true), app(4, "x.exe", "X", true),
	}}
	if label, others := a.TopLabel(); label != "OBS" || others != 2 {
		t.Errorf("topLabel = %q, %d", label, others)
	}
	if label, others := Off().TopLabel(); label != "" || others != 0 {
		t.Errorf("off topLabel = %q, %d", label, others)
	}
}

func TestActivityScannerDisabledDoesNotReadProcesses(t *testing.T) {
	s, calls := scannerOver("steam.exe")
	cfg := config.ActivityConfig{Enabled: false, Watch: []config.ActivityWatch{watch(1, "steam.exe", "Steam")}}
	got, changed := s.Scan(cfg)
	if calls.Load() != 0 {
		t.Errorf("the process list was read %d times while the option is off", calls.Load())
	}
	if changed || got.Enabled || got.Top != "" || got.Apps == nil || len(got.Apps) != 0 {
		t.Errorf("disabled scan = %+v changed=%v", got, changed)
	}
	if cur := s.Current(cfg); cur.Enabled || cur.Apps == nil || len(cur.Apps) != 0 {
		t.Errorf("disabled status = %+v", cur)
	}
}

// TestActivityScannerChangeDetection: a flip, a list edit (add, slot
// change, relabel) and the switch are changes; an unchanged scan is not.
func TestActivityScannerChangeDetection(t *testing.T) {
	running := []string{"explorer.exe"}
	s := scannerRunning(&running)

	cfg := config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "steam.exe", "Steam")}}
	if _, changed := s.Scan(cfg); changed {
		t.Error("the first scan reported a change (it only sets the baseline)")
	}
	if _, changed := s.Scan(cfg); changed {
		t.Error("an unchanged process list reported a change")
	}
	running = []string{"explorer.exe", "Steam.exe"}
	got, changed := s.Scan(cfg)
	if !changed || got.Top != "steam.exe" || !slices.Equal(got.Apps, []status.ActivityApp{app(1, "steam.exe", "Steam", true)}) {
		t.Errorf("steam started: %+v changed=%v", got, changed)
	}
	if cur := s.Current(cfg); cur.Top != "steam.exe" {
		t.Errorf("status after the scan = %+v", cur)
	}
	// A second instance of the same name is no flip.
	running = []string{"explorer.exe", "Steam.exe", "steam.exe"}
	if _, changed := s.Scan(cfg); changed {
		t.Error("a second steam.exe reported a change")
	}

	steps := []struct {
		name string
		cfg  config.ActivityConfig
	}{
		{"an entry added (not running)", config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "steam.exe", "Steam"), watch(2, "code.exe", "VS Code")}}},
		{"slots swapped", config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "code.exe", "VS Code"), watch(2, "steam.exe", "Steam")}}},
		{"moved to a free slot", config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "code.exe", "VS Code"), watch(5, "steam.exe", "Steam")}}},
		{"relabelled", config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "code.exe", "Code"), watch(5, "steam.exe", "Steam")}}},
		{"disabled", config.ActivityConfig{Enabled: false, Watch: []config.ActivityWatch{watch(1, "code.exe", "Code"), watch(5, "steam.exe", "Steam")}}},
		{"enabled again", config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "code.exe", "Code"), watch(5, "steam.exe", "Steam")}}},
	}
	for _, step := range steps {
		if _, changed := s.Scan(step.cfg); !changed {
			t.Errorf("%s: no change reported", step.name)
		}
		if _, changed := s.Scan(step.cfg); changed {
			t.Errorf("%s: the scan after it reported a change again", step.name)
		}
	}
	running = []string{"explorer.exe"}
	if got, changed := s.Scan(steps[len(steps)-1].cfg); !changed || got.Top != "" {
		t.Errorf("steam stopped: %+v changed=%v", got, changed)
	}
}

func TestActivityStatusIgnoresScanForAnotherList(t *testing.T) {
	s, _ := scannerOver("steam.exe")
	cfg := config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "steam.exe", "Steam")}}
	s.Scan(cfg)
	// The user edits the list: until the (immediate) rescan the apps are
	// listed, none running, and the removed one is gone at once.
	edited := config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(3, "obs64.exe", "OBS"), watch(1, "steam.exe", "Steam")}}
	cur := s.Current(edited)
	want := []status.ActivityApp{app(1, "steam.exe", "Steam", false), app(3, "obs64.exe", "OBS", false)}
	if !cur.Enabled || cur.Top != "" || !slices.Equal(cur.Apps, want) {
		t.Errorf("status after an edit = %+v, want %+v until rescanned", cur, want)
	}
}

func TestActivityScannerListFailureKeepsLastResult(t *testing.T) {
	fail := false
	s := &Scanner{List: func() ([]string, error) {
		if fail {
			return nil, errors.New("snapshot refused")
		}
		return []string{"steam.exe"}, nil
	}}

	// No earlier scan: listed, nothing running.
	fail = true
	cfg := config.ActivityConfig{Enabled: true, Watch: []config.ActivityWatch{watch(1, "steam.exe", "Steam")}}
	if got, _ := s.Scan(cfg); !got.Enabled || got.Top != "" || len(got.Apps) != 1 || got.Apps[0].Running {
		t.Errorf("failed first scan = %+v", got)
	}
	fail = false
	s.Scan(cfg)
	// A failure after a good scan keeps it: no fake "stopped".
	fail = true
	if got, changed := s.Scan(cfg); changed || got.Top != "steam.exe" {
		t.Errorf("failed scan after a good one = %+v changed=%v", got, changed)
	}
}

func TestToolhelpProcessNamesSeesThisProcess(t *testing.T) {
	names, err := ToolhelpNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 2 {
		t.Fatalf("only %d processes listed", len(names))
	}
	// The test binary itself is running, whatever its name.
	found := false
	for _, n := range names {
		if strings.HasSuffix(strings.ToLower(n), ".test.exe") || strings.HasSuffix(strings.ToLower(n), "service.test.exe") {
			found = true
		}
	}
	if !found {
		t.Errorf("the test binary is not in the list (%d names)", len(names))
	}
}
