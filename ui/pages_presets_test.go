package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

func presetsUI(t *testing.T) (*UI, *presetsPage) {
	t.Helper()
	u := sampleUI(gui.LangKo, false)
	u.open(pagePresets)
	return u, u.pages[pagePresets].(*presetsPage)
}

func TestPresetsFill(t *testing.T) {
	u, p := presetsUI(t)
	if len(p.rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(p.rows))
	}
	if r := p.rows[1].row(); r.Slot != 2 || r.Type != "url" || r.Path != "https://music.youtube.com" {
		t.Fatalf("row 2 = %+v", r)
	}
	if u.formDirty(p) {
		t.Fatal("dirty right after fill")
	}
	// a refill with the same config keeps the widgets (and their state)
	first := p.rows[0]
	first.open = true
	p.fill(u, *u.formSt.base)
	if p.rows[0] != first || !first.open {
		t.Fatal("refill replaced an unchanged row")
	}
}

func TestPresetsEditApply(t *testing.T) {
	u, p := presetsUI(t)
	p.rows[2].name.SetText("메모")
	p.rows[2].args.SetText(`"C:\My Files\a.txt" -x`)
	if !u.formDirty(p) {
		t.Fatal("an edit is not dirty")
	}
	cfg := cloneConfig(*u.formSt.base)
	if err := p.apply(u, &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.Presets[2]
	if got.Name != "메모" || len(got.Args) != 2 || got.Args[0] != `C:\My Files\a.txt` {
		t.Fatalf("applied %+v", got)
	}
	u.revertForm(p)
	if u.formDirty(p) || p.rows[2].name.Text() != "메모장" {
		t.Fatal("revert did not restore the row")
	}
}

func TestPresetsApplyRefusesProblems(t *testing.T) {
	u, p := presetsUI(t)
	p.rows[1].name.SetText("게임 모드") // same name as slot 1
	cfg := cloneConfig(*u.formSt.base)
	err := p.apply(u, &cfg)
	if err == nil || !strings.Contains(err.Error(), "이름") {
		t.Fatalf("err = %v", err)
	}
	dup := gui.PresetDuplicateNames(p.state())
	if p.problem(u, 0, dup) == "" || p.problem(u, 2, dup) != "" {
		t.Fatal("duplicate name not shown on its rows only")
	}
}

func TestPresetsAddDeleteSlotType(t *testing.T) {
	u, p := presetsUI(t)
	e := p.addRow(u)
	if e == nil || e.slot != 4 || e.typ != "program" || !e.open || p.rows[0].open {
		t.Fatalf("added %+v", e)
	}
	if p.focus != e {
		t.Fatal("new row not focused")
	}
	if !p.slotTaken(e, 1) || p.slotTaken(e, 4) || p.slotTaken(e, 5) {
		t.Fatal("slotTaken wrong")
	}
	e.args.SetText("-a")
	e.setType("url")
	if e.args.Text() != "" {
		t.Fatal("a URL kept its arguments")
	}
	p.deleteRow(e)
	if len(p.rows) != 3 || u.formDirty(p) {
		t.Fatal("deleting the new row did not undo it")
	}
	for range 7 {
		p.addRow(u)
	}
	if len(p.rows) != 10 || p.addRow(u) != nil {
		t.Fatalf("rows = %d, an 11th was added", len(p.rows))
	}
}

func TestPresetsLocked(t *testing.T) {
	u, p := presetsUI(t)
	u.trust.Store(int32(trustRefused))
	if p.addRow(u) != nil {
		t.Fatal("added a row while locked")
	}
	p.rows[0].name.SetText("다른 이름")
	if u.formDirty(p) {
		t.Fatal("dirty while locked")
	}
}

// testerAPI answers /api/presets/test.
type testerAPI struct {
	fakeAPI
	got gui.Preset
	ws  []gui.PresetWarning
	err error
}

func (a *testerAPI) TestPreset(p gui.Preset) ([]gui.PresetWarning, error) {
	a.got = p
	return a.ws, a.err
}

func TestPresetsRunTest(t *testing.T) {
	u, p := presetsUI(t)
	api := &testerAPI{ws: []gui.PresetWarning{{Slot: 9, Code: "writable_by_others", Path: `C:\x.ps1`}}}
	u.api = api
	e := p.rows[0]
	p.runTest(u, e)
	if !e.testing {
		t.Fatal("not testing")
	}
	waitTasks(t, u, func() bool { return !e.testing })
	if api.got.Path != e.path.Text() || e.statusTone != toneWarn || !strings.Contains(e.status, `C:\x.ps1`) {
		t.Fatalf("status %q (%v), sent %+v", e.status, e.statusTone, api.got)
	}

	api.ws, api.err = nil, errors.New("boom")
	p.runTest(u, e)
	waitTasks(t, u, func() bool { return !e.testing })
	if e.status != "boom" || e.statusTone != toneBad {
		t.Fatalf("status %q", e.status)
	}

	// a row that does not check out is not sent
	api.got = gui.Preset{}
	e.path.SetText("notepad")
	p.runTest(u, e)
	if e.testing || api.got.Path != "" || e.statusTone != toneBad {
		t.Fatal("an invalid row was tested")
	}
}

func TestPresetsSavedWarnings(t *testing.T) {
	u, p := presetsUI(t)
	p.saved(u, gui.SaveReply{Warnings: []gui.PresetWarning{{Slot: 1, Code: "writable_by_others"}}})
	if gui.PresetSlotWarnings(u.language(), p.warnings, 1) == "" {
		t.Fatal("warning not kept")
	}
}

// waitTasks runs onWindow's queue until done reports true.
func waitTasks(t *testing.T, u *UI, done func() bool) {
	t.Helper()
	for range 500 {
		u.runTasks()
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("never finished")
}
