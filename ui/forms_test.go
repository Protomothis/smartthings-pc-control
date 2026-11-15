package ui

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// configAPI is a service with a config: reads return it, saves replace it
// and are recorded.
type configAPI struct {
	fakeAPI
	mu      sync.Mutex
	cfg     gui.Config
	saved   []gui.Config
	reply   gui.SaveReply
	saveErr error
}

func (a *configAPI) GetConfig() (gui.Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneConfig(a.cfg), nil
}

func (a *configAPI) SaveConfig(cfg gui.Config) (gui.SaveReply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.saveErr != nil {
		return gui.SaveReply{}, a.saveErr
	}
	a.saved = append(a.saved, cloneConfig(cfg))
	a.cfg = cloneConfig(cfg)
	return a.reply, nil
}

func (a *configAPI) LocalLogin() error { return nil }

func (a *configAPI) set(fn func(c *gui.Config)) {
	a.mu.Lock()
	fn(&a.cfg)
	a.mu.Unlock()
}

// formUI is a sampleUI on api, its forms filled from api's config.
func formUI(t *testing.T, api *configAPI) *UI {
	t.Helper()
	u := sampleUI(gui.LangKo, false)
	u.api = api
	u.snap.cfg = cloneConfig(api.cfg)
	u.snap.cfgGen++
	u.syncForms(u.current())
	return u
}

// drain runs onWindow's queue until the save is done (the window loop's
// job in the app).
func drain(t *testing.T, u *UI) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		u.runTasks()
		u.mu.Lock()
		pending := len(u.tasks)
		u.mu.Unlock()
		if !u.formSt.saving && pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("save never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func baseConfig() gui.Config {
	return gui.Config{Port: 5001, GraceSeconds: 300, Media: gui.MediaConfig{Enabled: true},
		Telegram:    gui.TelegramConfig{Enabled: true, ChatID: "1", BotToken: "****6789", BotTokenSet: true},
		SmartThings: gui.SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}}}
}

// A back press over unsaved changes only warns; the second one leaves and
// throws the edits away.
func TestBackWarnsOnUnsaved(t *testing.T) {
	u := formUI(t, &configAPI{cfg: baseConfig()})
	p := u.pages[pageService].(*servicePage)
	u.open(pageService)
	p.port.SetText("5011")
	p.read()
	if !u.formDirty(p) {
		t.Fatal("edit not dirty")
	}
	now := time.Now()
	if u.nav.back(u, now) || u.nav.top() == nil {
		t.Fatal("left on the first press")
	}
	if !u.nav.back(u, now.Add(time.Second)) || u.nav.top() != nil {
		t.Fatal("stayed on the second press")
	}
	u.open(pageService)
	if u.formDirty(p) || p.port.Text() != "5001" {
		t.Fatal("edits survived leaving")
	}
	// after the arm runs out, a press only warns again
	p.port.SetText("5011")
	p.read()
	u.nav.back(u, now)
	if u.nav.back(u, now.Add(leaveArm+time.Second)) {
		t.Fatal("an old warning still counted")
	}
}

// A fresh config refills a clean form and leaves one with edits alone.
func TestAdoptKeepsEdits(t *testing.T) {
	api := &configAPI{cfg: baseConfig()}
	u := formUI(t, api)
	p := u.pages[pageService].(*servicePage)

	next := baseConfig()
	next.Media.Enabled = false
	u.setConfig(next)
	u.syncForms(u.current())
	if p.media.Value {
		t.Fatal("clean form not refilled")
	}

	p.port.SetText("6000")
	p.read()
	next.Debug = true
	u.setConfig(next)
	u.syncForms(u.current())
	if p.port.Text() != "6000" || !u.formDirty(p) {
		t.Fatal("edits lost on adopt")
	}
	if p.debug.Value {
		t.Fatal("a form with edits was refilled")
	}
}

// A save posts the page's own fields over what the service has now, so a
// change made elsewhere meanwhile survives it.
func TestSaveMergesOverConcurrentChange(t *testing.T) {
	api := &configAPI{cfg: baseConfig()}
	u := formUI(t, api)
	p := u.pages[pageService].(*servicePage)
	u.open(pageService)
	p.port.SetText("5011")
	p.read()

	// Telegram turned off and a hub added through the WebUI meanwhile.
	api.set(func(c *gui.Config) {
		c.Telegram.Enabled = false
		c.SmartThings.AllowedHubs = []string{"192.168.1.20", "192.168.1.21"}
	})
	u.saveForm(p)
	drain(t, u)

	if len(api.saved) != 1 {
		t.Fatalf("saved %d times", len(api.saved))
	}
	got := api.saved[0]
	if got.Port != 5011 {
		t.Errorf("port = %d, want the edit", got.Port)
	}
	if got.Telegram.Enabled || !slices.Equal(got.SmartThings.AllowedHubs, []string{"192.168.1.20", "192.168.1.21"}) {
		t.Errorf("the concurrent change was overwritten: %+v", got)
	}
	if u.formDirty(p) {
		t.Error("still dirty after the save")
	}
	if c := u.current().cfg; c.Port != 5011 || c.Telegram.Enabled {
		t.Errorf("snapshot not updated: %+v", c)
	}
}

// A failed check or save is a banner on the page, and nothing is posted
// for a bad value.
func TestSaveErrorsAreNotices(t *testing.T) {
	api := &configAPI{cfg: baseConfig()}
	u := formUI(t, api)
	p := u.pages[pageService].(*servicePage)
	u.open(pageService)
	p.port.SetText("abc")
	p.read()
	u.saveForm(p)
	if n, ok := u.notices[p]; !ok || n.kind != noticeError || len(api.saved) != 0 {
		t.Fatalf("bad port: notice %+v, saved %d", n, len(api.saved))
	}

	p.port.SetText("5011")
	p.read()
	api.saveErr = errors.New("disk full")
	u.saveForm(p)
	drain(t, u)
	if n := u.notices[p]; n.text != "disk full" {
		t.Fatalf("save error notice = %+v", n)
	}
	if !u.formDirty(p) {
		t.Fatal("edits lost after a failed save")
	}
}

// A change that needs a restart offers it in the toast.
func TestSaveOffersRestart(t *testing.T) {
	api := &configAPI{cfg: baseConfig(), reply: gui.SaveReply{Message: "Settings saved. Restart service to apply."}}
	u := formUI(t, api)
	p := u.pages[pageService].(*servicePage)
	u.open(pageService)
	p.port.SetText("5011")
	p.read()
	u.saveForm(p)
	drain(t, u)
	cur, ok := u.toast.current(time.Now())
	if !ok || cur.action == "" || cur.do == nil {
		t.Fatalf("toast = %+v, want one with [지금 재시작]", cur)
	}
}

// A slot that SmartThings routines use changing hands is asked about
// before anything is posted.
func TestSaveAsksAboutSlots(t *testing.T) {
	cfg := baseConfig()
	cfg.Presets = []gui.Preset{{Slot: 1, Name: "게임", Type: "program", Path: `C:\game.exe`}}
	api := &configAPI{cfg: cfg}
	u := formUI(t, api)
	f := &slotForm{}
	u.saveForm(f)
	if u.sheetW.open == nil || len(api.saved) != 0 {
		t.Fatal("no question before a slot change")
	}
	u.sheetW.open.onOK()
	drain(t, u)
	if len(api.saved) != 1 || len(api.saved[0].Presets) != 1 || api.saved[0].Presets[0].Name != "메모장" {
		t.Fatalf("saved = %+v", api.saved)
	}
}

// slotForm puts another program in preset slot 1.
type slotForm struct{ sharePage }

func (*slotForm) fill(*UI, gui.Config)       {}
func (*slotForm) dirty(*UI, gui.Config) bool { return true }
func (*slotForm) apply(_ *UI, c *gui.Config) error {
	c.Presets = []gui.Preset{{Slot: 1, Name: "메모장", Type: "program", Path: `C:\Windows\notepad.exe`}}
	return nil
}

func TestOnWindowRunsInOrder(t *testing.T) {
	u := New(fakeAPI{}, "dev", loadPrefs(""), func() {})
	var got []int
	for i := range 3 {
		u.onWindow(func() { got = append(got, i) })
	}
	u.runTasks()
	if !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("got %v", got)
	}
}
