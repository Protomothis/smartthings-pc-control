package gui

import (
	"reflect"
	"strings"
	"testing"
)

func TestPresetSlotChanges(t *testing.T) {
	old := []Preset{
		{Slot: 3, Name: "게임 모드", Type: "program", Path: `C:\Games\a.exe`},
		{Slot: 1, Name: "Steam", Type: "program", Path: `C:\Steam\steam.exe`, Args: []string{"-x"}},
		{Slot: 2, Name: "Docs", Type: "url", Path: "https://docs"},
		{Slot: 4, Name: "Same", Type: "script", Path: `C:\s\a.ps1`},
		{Slot: 5, Name: "Moved", Type: "url", Path: "https://moved"},
	}
	next := []Preset{
		// Slot 1: arguments, type spacing and case only — the same owner.
		{Slot: 1, Name: " steam ", Type: "program", Path: `c:\steam\STEAM.exe`},
		// Slot 2: another program under the slot.
		{Slot: 2, Name: "OBS", Type: "program", Path: `C:\OBS\obs.exe`},
		// Slot 3 is gone. Slot 4: same name, other file.
		{Slot: 4, Name: "Same", Type: "script", Path: `C:\s\b.ps1`},
		// Slot 5 moved to 7; 7 and 9 were empty.
		{Slot: 7, Name: "Moved", Type: "url", Path: "https://moved"},
		{Slot: 9, Name: "New", Type: "url", Path: "https://new"},
	}
	want := []slotChange{
		{Slot: 2, From: "Docs", To: "OBS"},
		{Slot: 3, From: "게임 모드"},
		{Slot: 4, From: `Same (C:\s\a.ps1)`, To: `Same (C:\s\b.ps1)`},
		{Slot: 5, From: "Moved"},
	}
	if got := presetSlotChanges(old, next); !reflect.DeepEqual(got, want) {
		t.Errorf("changes =\n%+v\nwant\n%+v", got, want)
	}
	if got := presetSlotChanges(nil, next); len(got) != 0 {
		t.Errorf("new entries in empty slots: %+v", got)
	}
	if got := presetSlotChanges(old, old); len(got) != 0 {
		t.Errorf("no edit: %+v", got)
	}
}

func TestWatchSlotChanges(t *testing.T) {
	old := []ActivityWatch{
		{Slot: 1, Process: "steam.exe", Label: "Steam"},
		{Slot: 2, Process: "obs64.exe", Label: "OBS"},
		{Slot: 3, Process: "game.exe"}, // the label defaults to "game"
		{Slot: 4, Process: "a.exe", Label: "Tool"},
	}
	next := []ActivityWatch{
		{Slot: 1, Process: "obs64.exe", Label: "OBS"},    // another program
		{Slot: 2, Process: "OBS64.EXE", Label: "Stream"}, // case and label only
		{Slot: 4, Process: "b.exe", Label: "Tool"},       // same label, other file
		{Slot: 5, Process: "new.exe"},                    // was empty
	}
	want := []slotChange{
		{Watch: true, Slot: 1, From: "Steam", To: "OBS"},
		{Watch: true, Slot: 3, From: "game"},
		{Watch: true, Slot: 4, From: "a.exe", To: "b.exe"},
	}
	if got := watchSlotChanges(old, next); !reflect.DeepEqual(got, want) {
		t.Errorf("changes =\n%+v\nwant\n%+v", got, want)
	}
	if got := watchSlotChanges(nil, next); len(got) != 0 {
		t.Errorf("new entries in empty slots: %+v", got)
	}
	// A blank row the editor left behind is no entry.
	if got := watchSlotChanges(old, append(old, ActivityWatch{Slot: 5})); len(got) != 0 {
		t.Errorf("a blank row: %+v", got)
	}
}

func TestSlotChangeText(t *testing.T) {
	old := Config{
		Presets:  []Preset{{Slot: 3, Name: "게임 모드", Type: "url", Path: "https://a"}},
		Activity: ActivityConfig{Watch: []ActivityWatch{{Slot: 1, Process: "steam.exe", Label: "Steam"}}},
	}
	next := Config{
		Presets:  []Preset{},
		Activity: ActivityConfig{Watch: []ActivityWatch{{Slot: 1, Process: "obs.exe", Label: "OBS"}}},
	}
	changes := configSlotChanges(old, next)
	body := slotChangeBody(LangKo, changes)
	want := "프리셋 3: 게임 모드 → (비어 있음)\n감시 1: Steam → OBS\n\n" +
		"이 칸을 쓰는 SmartThings 루틴이 다른 대상을 가리키게 됩니다. 저장할까요?"
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
	if en := slotChangeBody(LangEn, changes); !strings.HasPrefix(en, "Preset 3: 게임 모드 → (empty)\nWatch 1: Steam → OBS") {
		t.Errorf("en body = %q", en)
	}
}

// The save asks before a slot changes owner; Cancel posts nothing, Save
// posts as before. A new entry in an empty slot is not asked about.
func TestSaveAsksBeforeASlotChangesOwner(t *testing.T) {
	u, svc := newFakeServiceUI(t)
	var asked []string
	answer := false
	u.confirm = func(_, body, _ string, cb func(bool)) {
		asked = append(asked, body)
		cb(answer)
	}
	ft := u.forms.tabAt(tabPresets)

	// A new preset in the empty slot 2: saved without a question.
	u.presets.addBtn.OnTapped()
	w := u.presets.rows[1]
	w.name.SetText("Docs")
	w.typ.SetSelectedIndex(1) // url
	w.path.SetText("https://docs")
	u.saveTab(ft)
	if len(asked) != 0 || len(svc.posts) != 1 {
		t.Fatalf("asked %d times, %d posts; want no question and a post", len(asked), len(svc.posts))
	}

	// Slot 1 (Steam) becomes another program: asked, and Cancel keeps it.
	u.presets.rows[0].name.SetText("OBS")
	u.presets.rows[0].path.SetText(`C:\OBS\obs.exe`)
	u.saveTab(ft)
	if len(asked) != 1 || !strings.Contains(asked[0], "Preset 1: Steam → OBS") {
		t.Fatalf("question = %q", asked)
	}
	if len(svc.posts) != 1 {
		t.Fatal("Cancel posted the change")
	}
	if !u.forms.isDirty(ft) {
		t.Error("Cancel dropped the edit")
	}
	answer = true
	u.saveTab(ft)
	if len(svc.posts) != 2 || svc.lastPost(t).Presets[0].Name != "OBS" {
		t.Errorf("Save after the question: %d posts", len(svc.posts))
	}
}
