package gui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"fyne.io/fyne/v2/widget"
)

// trustAPI is fakeAPI with the local trusted session of C5 and no secret:
// every route answers, but the config read masks the preset paths and the
// local-only calls answer errLocalOnly until LocalLogin vouched for the
// app. trusted false again is the service having restarted.
type trustAPI struct {
	*fakeAPI
	refuse  bool
	trusted bool
	logins  int
	listed  int // RunningProcesses that got through
	tested  int // TestPreset calls that got through
}

func (a *trustAPI) LocalLogin() error {
	a.logins++
	if a.refuse {
		return fmt.Errorf("%w (HTTP 403)", errLocalLoginRefused)
	}
	a.trusted = true
	return nil
}

func (a *trustAPI) GetConfig() (Config, error) {
	cfg, err := a.fakeAPI.GetConfig()
	if !a.trusted {
		for i := range cfg.Presets {
			cfg.Presets[i].Path, cfg.Presets[i].Args = "", nil
		}
	}
	return cfg, err
}

func (a *trustAPI) SaveConfig(cfg Config) (SaveReply, error) {
	presetsChange := cfg.Presets != nil && !presetsEqual(cfg.Presets, a.cfg.Presets)
	watchChange := cfg.Activity.Watch != nil &&
		!activityEqual(ActivityConfig{Watch: cfg.Activity.Watch}, ActivityConfig{Watch: a.cfg.Activity.Watch})
	if !a.trusted && (presetsChange || watchChange) {
		return SaveReply{}, errLocalOnly
	}
	return a.fakeAPI.SaveConfig(cfg)
}

func (a *trustAPI) RunningProcesses() ([]string, error) {
	if !a.trusted {
		return nil, errLocalOnly
	}
	a.listed++
	return []string{"obs64.exe"}, nil
}

func (a *trustAPI) TestPreset(Preset) ([]PresetWarning, error) {
	if !a.trusted {
		return nil, errLocalOnly
	}
	a.tested++
	return nil, nil
}

func newTrustUI(t *testing.T, refuse bool) (*ui, *trustAPI) {
	t.Helper()
	cfg := storedConfig()
	cfg.Secret = "" // no secret: nothing ever answers 401
	cfg.Activity.Watch = []ActivityWatch{{Slot: 1, Process: "steam.exe", Label: "Steam"}}
	svc := &trustAPI{fakeAPI: &fakeAPI{cfg: cfg}, refuse: refuse}
	u := newTestUI(t, LangEn, svc)
	u.initialLoad()
	if !u.connected.Load() {
		t.Fatal("initialLoad did not connect")
	}
	return u, svc
}

// C5: the app asks for the local session at start even with no secret,
// so the presets arrive unmasked and the editors are open; a reload with
// the session still held does not ask again, a reconnect does.
func TestStartupObtainsLocalSessionWithoutSecret(t *testing.T) {
	u, svc := newTrustUI(t, false)
	if svc.logins != 1 || u.trust() != trustLocal {
		t.Fatalf("logins=%d trust=%d, want one local login and trustLocal", svc.logins, u.trust())
	}
	if got := u.forms.base.Presets[0].Path; got != `C:\Steam\steam.exe` {
		t.Errorf("preset path = %q, read before the local login (masked)", got)
	}
	if u.presets.lockNote.Visible() || u.presets.addBtn.Disabled() || u.presets.rows[0].test.Disabled() {
		t.Error("the presets editor is locked with a local session")
	}
	u.initialLoad() // a language switch
	if svc.logins != 1 {
		t.Errorf("logins = %d after a reload, want the session kept", svc.logins)
	}

	// The service went away and came back: the session is asked for again
	// before the config is read.
	u.markDisconnectedOnNetError(errors.New("connection refused"))
	svc.trusted = false
	u.initialLoad()
	if svc.logins != 2 || u.forms.base.Presets[0].Path == "" {
		t.Errorf("after a reconnect: logins=%d path=%q", svc.logins, u.forms.base.Presets[0].Path)
	}
}

// C5: a session lost while connected (403 local_only) is obtained again
// once and the call retried — the picker, the test button and a save.
func TestLocalOnlyRenewsTheSession(t *testing.T) {
	u, svc := newTrustUI(t, false)

	svc.trusted = false
	u.pickRunningProgram()
	if svc.logins != 2 || svc.listed != 1 {
		t.Errorf("picker: logins=%d listed=%d, want a renewed session and the list", svc.logins, svc.listed)
	}

	svc.trusted = false
	w := u.presets.rows[0]
	u.testPresetRow(w)
	if svc.logins != 3 || svc.tested != 1 || w.status.Text != T(LangEn, "presets.started") {
		t.Errorf("test: logins=%d tested=%d status=%q", svc.logins, svc.tested, w.status.Text)
	}

	svc.trusted = false
	w.args.SetText("-silent")
	u.saveForms([]*formTab{u.forms.tabAt(tabPresets)}, true, nil)
	if svc.logins != 4 || len(svc.posts) != 1 || len(svc.cfg.Presets[0].Args) != 1 {
		t.Errorf("save: logins=%d posts=%d stored=%+v", svc.logins, len(svc.posts), svc.cfg.Presets)
	}
}

// C5: refused (a standard user), the presets and watch editors are locked
// under the note instead of failing on save; the rest of the sharing tab
// still saves, with both lists sent as null (keep).
func TestRefusedLocalSessionLocksTheEditors(t *testing.T) {
	u, svc := newTrustUI(t, true)
	if u.trust() != trustRefused {
		t.Fatalf("trust = %d, want refused", u.trust())
	}
	p := u.presets
	if !p.lockNote.Visible() || p.lockNote.Text != T(LangEn, "localonly.note") {
		t.Error("no lock note on the presets tab")
	}
	w := p.rows[0]
	for name, c := range map[string]interface{ Disabled() bool }{
		"add": p.addBtn, "name": w.name, "path": w.path, "args": w.args, "type": w.typ,
		"slot": w.slot, "test": w.test, "remove": w.remove, "browse": w.browse,
	} {
		if !c.Disabled() {
			t.Errorf("presets %s is usable while refused", name)
		}
	}
	p.bar.setDirty(true)
	if !p.bar.save.Disabled() {
		t.Error("presets Save is usable while refused")
	}
	p.bar.setDirty(false)
	a := &u.share.activity
	if !a.lockNote.Visible() || !a.addBtn.Disabled() || !a.pickBtn.Disabled() {
		t.Error("the watch list is not locked")
	}
	if a.toggle.Disabled() {
		t.Error("the detection toggle is locked too")
	}

	u.share.nowPlaying.SetChecked(!u.share.nowPlaying.Checked)
	u.saveForms([]*formTab{u.forms.tabAt(tabShare)}, true, nil)
	sent := svc.lastPost(t)
	if sent.Presets != nil || sent.Activity.Watch != nil {
		t.Errorf("posted presets %v watch %v, want both null", sent.Presets, sent.Activity.Watch)
	}
	if svc.cfg.Presets[0].Path != `C:\Steam\steam.exe` || len(svc.cfg.Activity.Watch) != 1 {
		t.Errorf("the stored lists changed: %+v %+v", svc.cfg.Presets, svc.cfg.Activity.Watch)
	}

	// The refusal is remembered: a reload does not ask again at once.
	u.initialLoad()
	if svc.logins != 1 || u.trust() != trustRefused {
		t.Errorf("logins=%d trust=%d after a reload", svc.logins, u.trust())
	}
}

func TestClientLocalOnlyReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"local_only","message":"local session required"}`))
	}))
	defer srv.Close()
	c := clientFor(t, srv)
	if _, err := c.SaveConfig(Config{}); !errors.Is(err, errLocalOnly) {
		t.Errorf("SaveConfig = %v", err)
	}
	if _, err := c.RunningProcesses(); !errors.Is(err, errLocalOnly) {
		t.Errorf("RunningProcesses = %v", err)
	}
	if _, err := c.TestPreset(Preset{Slot: 1}); !errors.Is(err, errLocalOnly) {
		t.Errorf("TestPreset = %v", err)
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","message":"saved","warnings":[{"slot":2,"code":"writable_by_others","path":"C:\\x.ps1"}]}`))
	}))
	defer ok.Close()
	c = clientFor(t, ok)
	reply, err := c.SaveConfig(Config{})
	if err != nil || reply.Message != "saved" || len(reply.Warnings) != 1 || reply.Warnings[0].Slot != 2 || reply.Warnings[0].Path != `C:\x.ps1` {
		t.Errorf("SaveConfig = %+v, %v", reply, err)
	}
	ws, err := c.TestPreset(Preset{Slot: 2})
	if err != nil || len(ws) != 1 || ws[0].Code != "writable_by_others" {
		t.Errorf("TestPreset = %+v, %v", ws, err)
	}
}

// C6: the save's warnings land on the row of their slot.
func TestSaveShowsPresetWarningsOnTheRow(t *testing.T) {
	u, svc := newFakeServiceUI(t)
	svc.warnings = []PresetWarning{{Slot: 1, Code: "writable_by_others", Path: `C:\Steam`}}
	w := u.presets.rows[0]
	w.args.SetText("-silent")
	u.saveForms([]*formTab{u.forms.tabAt(tabPresets)}, true, nil)
	w = u.presets.rows[0] // the tab was refilled from the saved config
	want := T(LangEn, "presets.warn.writable") + "\n" + `C:\Steam`
	if !w.status.Visible() || w.status.Text != want || w.status.Importance != widget.WarningImportance {
		t.Errorf("row status = %q (visible %v, importance %d), want %q", w.status.Text, w.status.Visible(), w.status.Importance, want)
	}
}

// Review 3: switching a row to URL clears its arguments (the entry and
// what the row reads as), and switching back opens the entry again.
func TestURLTypeClearsTheArguments(t *testing.T) {
	u, _ := newFakeServiceUI(t)
	w := u.presets.rows[0]
	w.args.SetText(`"open`) // even an unparsable line
	w.typ.SetSelectedIndex(1)
	if w.args.Text != "" || w.row().Args != "" || !w.args.Disabled() || !w.browse.Disabled() {
		t.Errorf("url row: args %q (disabled %v)", w.args.Text, w.args.Disabled())
	}
	w.path.SetText("https://example.com")
	if key, _ := rowsProblem(u.presets.state().Rows); key != "" {
		t.Errorf("url row problem %q", key)
	}
	w.typ.SetSelectedIndex(0)
	if w.args.Disabled() || w.browse.Disabled() {
		t.Error("program row: args or browse still disabled")
	}
}

// C6: a name another row has too is marked inline on both rows.
func TestDuplicateNameInline(t *testing.T) {
	u, _ := newFakeServiceUI(t)
	u.presets.addBtn.OnTapped()
	a, b := u.presets.rows[0], u.presets.rows[1]
	b.name.SetText(" STEAM ")
	if !a.nameErr.Visible() || !b.nameErr.Visible() {
		t.Fatal("duplicate names not marked")
	}
	if b.nameErr.Text != fmt.Sprintf(T(LangEn, "presets.err.namedup"), 2) {
		t.Errorf("inline error = %q", b.nameErr.Text)
	}
	b.name.SetText("Steam 2")
	if a.nameErr.Visible() || b.nameErr.Visible() {
		t.Error("the mark stayed after the rename")
	}
}
