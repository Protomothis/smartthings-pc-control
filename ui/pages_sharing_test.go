package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// shareConfig is a config with sharing partly on and two watched programs.
func shareConfig() gui.Config {
	c := baseConfig()
	c.SmartThings.ExposeSession = true
	c.SmartThings.ExposeSessionUser = true
	c.Media.NowPlaying = true
	c.Activity = gui.ActivityConfig{Enabled: true, Watch: []gui.ActivityWatch{
		{Slot: 3, Process: "Discord.exe", Label: "Discord"},
		{Slot: 1, Process: "steam.exe", Label: "Steam"},
	}}
	return c
}

func sharingUI(t *testing.T, api *configAPI) (*UI, *sharePage) {
	t.Helper()
	u := formUI(t, api)
	u.open(pageSharing)
	return u, u.pages[pageSharing].(*sharePage)
}

// fill shows the config in slot order and is clean; each switch makes it
// dirty, and apply writes only the page's own fields.
func TestSharingFillDirtyApply(t *testing.T) {
	u, p := sharingUI(t, &configAPI{cfg: shareConfig()})
	if !p.session.Value || !p.user.Value || !p.nowPlaying.Value || !p.activity.Value {
		t.Fatal("switches not filled")
	}
	if len(p.watch) != 2 || p.watch[0].Slot != 1 || p.procEd[0].Text() != "steam.exe" || p.labelEd[1].Text() != "Discord" {
		t.Fatalf("watch not filled in slot order: %+v", p.watch)
	}
	if u.formDirty(p) {
		t.Fatal("dirty right after fill")
	}
	for _, b := range []*bool{&p.session.Value, &p.nowPlaying.Value, &p.activity.Value} {
		*b = !*b
		p.read()
		if !u.formDirty(p) {
			t.Fatal("switch change not dirty")
		}
		*b = !*b
		p.read()
		if u.formDirty(p) {
			t.Fatal("switch back still dirty")
		}
	}

	// session off: the user name goes off with it, other fields stay
	p.session.Value = false
	p.read()
	cfg := cloneConfig(*u.formSt.base)
	if err := p.apply(u, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SmartThings.ExposeSession || cfg.SmartThings.ExposeSessionUser {
		t.Errorf("session = %+v", cfg.SmartThings)
	}
	if cfg.Port != 5001 || !slices.Equal(cfg.SmartThings.AllowedHubs, []string{"192.168.1.20"}) || !cfg.Media.Enabled {
		t.Errorf("apply touched another page's fields: %+v", cfg)
	}
	if len(cfg.Activity.Watch) != 2 || cfg.Activity.Watch[0].Process != "steam.exe" {
		t.Errorf("watch = %+v", cfg.Activity.Watch)
	}
}

// Typing into a row is an edit; a bad name is refused in the app's
// language before anything is sent.
func TestSharingWatchEditAndCheck(t *testing.T) {
	api := &configAPI{cfg: shareConfig()}
	u, p := sharingUI(t, api)
	p.labelEd[0].SetText("스팀")
	p.read()
	if !u.formDirty(p) {
		t.Fatal("label edit not dirty")
	}
	p.procEd[0].SetText(`C:\Games\steam.exe`)
	p.read()
	u.saveForm(p)
	n, ok := u.notices[p]
	if !ok || n.kind != noticeError || !strings.Contains(n.text, "경로 없이") || len(api.saved) != 0 {
		t.Fatalf("notice %+v, saved %d", n, len(api.saved))
	}
}

// Adding, removing and moving rows keep the editors in step with the list.
func TestSharingRowsStayInStep(t *testing.T) {
	_, p := sharingUI(t, &configAPI{cfg: shareConfig()})
	p.labelEd[0].SetText("스팀") // typed, then a row is added: the text stays
	p.readEditors()
	w, ok := gui.AddActivityRow(p.watch)
	if !ok {
		t.Fatal("no free slot")
	}
	p.setWatch(w)
	if len(p.procEd) < 3 || p.watch[1].Slot != 2 || p.procEd[1].Text() != "" || p.labelEd[0].Text() != "스팀" {
		t.Fatalf("after add: %+v", p.watch)
	}
	// steam (slot 1) to slot 5: it moves to the end, its texts with it
	w, ok = gui.SetActivitySlot(p.watch, 0, 5)
	if !ok {
		t.Fatal("slot not moved")
	}
	p.setWatch(w)
	if last := len(p.watch) - 1; p.watch[last].Slot != 5 || p.procEd[last].Text() != "steam.exe" || p.labelEd[last].Text() != "스팀" {
		t.Fatalf("after move: %+v", p.watch)
	}
	if p.procEd[0].Text() != "" || p.procEd[1].Text() != "Discord.exe" {
		t.Fatalf("editors not re-synced: %q %q", p.procEd[0].Text(), p.procEd[1].Text())
	}
}

// Emptying a watch slot asks first, then saves.
func TestSharingSlotQuestion(t *testing.T) {
	api := &configAPI{cfg: shareConfig()}
	u, p := sharingUI(t, api)
	p.setWatch(slices.Delete(slices.Clone(p.watch), 0, 1))
	p.read()
	u.saveForm(p)
	if u.sheetW.open == nil || len(api.saved) != 0 {
		t.Fatal("no question before emptying a slot")
	}
	u.sheetW.open.onOK()
	drain(t, u)
	if len(api.saved) != 1 || len(api.saved[0].Activity.Watch) != 1 || api.saved[0].Activity.Watch[0].Slot != 3 {
		t.Fatalf("saved = %+v", api.saved)
	}
}

// Without the local session the list is sent as "keep" while the switch
// still saves.
func TestSharingLockedKeepsList(t *testing.T) {
	api := &configAPI{cfg: shareConfig()}
	u, p := sharingUI(t, api)
	u.trust.Store(int32(trustRefused))
	p.activity.Value = false
	p.read()
	u.saveForm(p)
	drain(t, u)
	if len(api.saved) != 1 {
		t.Fatalf("saved %d times", len(api.saved))
	}
	if got := api.saved[0].Activity; got.Enabled || got.Watch != nil {
		t.Fatalf("activity sent = %+v, want off and the list kept (nil)", got)
	}
}

// processAPI lists running programs.
type processAPI struct {
	configAPI
	names []string
	err   error
}

func (a *processAPI) RunningProcesses() ([]string, error) { return a.names, a.err }

// The picker offers the running programs not on the list; picking one
// adds it in the lowest free slot and closes the panel.
func TestSharingPicker(t *testing.T) {
	api := &processAPI{configAPI: configAPI{cfg: shareConfig()}, names: []string{"steam.exe", "Code.exe", "explorer.exe"}}
	u := formUI(t, &api.configAPI)
	u.api = api
	u.open(pageSharing)
	p := u.pages[pageSharing].(*sharePage)

	p.openPicker(u)
	if !p.picker.open || !p.picker.loading {
		t.Fatal("picker not loading")
	}
	waitFor(t, u, func() bool { return !p.picker.loading })
	if got := p.candidates(); !slices.Equal(got, []string{"Code.exe", "explorer.exe"}) {
		t.Fatalf("candidates = %v", got)
	}
	p.picker.search.SetText("EXP")
	if got := p.candidates(); !slices.Equal(got, []string{"explorer.exe"}) {
		t.Fatalf("searched = %v", got)
	}

	// a failure closes the panel and says why
	api.err = errors.New("HTTP 500")
	p.openPicker(u)
	waitFor(t, u, func() bool { return !p.picker.open })
	if n := u.notices[p]; n.kind != noticeError || !strings.Contains(n.text, "HTTP 500") {
		t.Fatalf("notice = %+v", n)
	}
}

// waitFor runs onWindow's queue until cond holds.
func waitFor(t *testing.T, u *UI, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		u.runTasks()
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
