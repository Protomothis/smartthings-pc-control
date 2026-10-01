package gui

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// fakeAPI is the service behind serviceAPI, in memory. The embedded
// interface is nil: a call the fake does not implement panics, so a test
// shows exactly what it touches. GET/POST of the config follow the
// service's token rules (masked on GET; "" or "****…" keeps, "-" clears,
// anything else replaces) and go through JSON like the real request.
type fakeAPI struct {
	serviceAPI
	mu    sync.Mutex
	cfg   Config // BotToken in plain text
	posts []Config
}

func (f *fakeAPI) GetConfig() (Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cfg
	out.Telegram.BotToken = maskToken(f.cfg.Telegram.BotToken)
	out.Telegram.BotTokenSet = f.cfg.Telegram.BotToken != ""
	return jsonRoundTrip(out), nil
}

func (f *fakeAPI) SaveConfig(cfg Config) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := jsonRoundTrip(cfg)
	f.posts = append(f.posts, in)
	token := f.cfg.Telegram.BotToken
	switch t := in.Telegram.BotToken; {
	case t == "" || strings.HasPrefix(t, maskedTokenPrefix):
	case t == "-":
		token = ""
	default:
		token = t
	}
	f.cfg = in
	f.cfg.Telegram.BotToken = token
	return "saved", nil
}

func (f *fakeAPI) Login(string) error                    { return nil }
func (f *fakeAPI) GetSchedule() (Schedule, error)        { return Schedule{}, nil }
func (f *fakeAPI) GetWoLStatus() (WoLStatus, error)      { return WoLStatus{}, nil }
func (f *fakeAPI) GetSTHub() (STHub, error)              { return STHub{}, nil }
func (f *fakeAPI) TelegramMe() (string, string, error)   { return "bot", "Bot", nil }
func (f *fakeAPI) TelegramState() (TelegramState, error) { return TelegramState{}, nil }

func jsonRoundTrip(cfg Config) Config {
	b, _ := json.Marshal(cfg)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}

func (f *fakeAPI) lastPost(t *testing.T) Config {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) == 0 {
		t.Fatal("nothing was posted")
	}
	return f.posts[len(f.posts)-1]
}

// newFakeServiceUI is the whole window on a fakeAPI, loaded once.
func newFakeServiceUI(t *testing.T) (*ui, *fakeAPI) {
	t.Helper()
	svc := &fakeAPI{cfg: storedConfig()}
	svc.cfg.Telegram.BotToken = "123456:SECRETTOKEN6789"
	u := newTestUI(t, LangEn, svc)
	u.initialLoad()
	if !u.connected.Load() || u.forms.base == nil {
		t.Fatal("initialLoad did not connect")
	}
	return u, svc
}

// The widgets end to end: Save on one tab posts the baseline with only
// that tab's edits (secret and masked token intact), and another tab's
// unsaved edits stay in its widgets and keep it dirty.
func TestSaveFromOneTabKeepsTheOthers(t *testing.T) {
	u, svc := newFakeServiceUI(t)
	if d := dirtyIndices(u.forms); d != nil {
		t.Fatalf("dirty after the load: %v", d)
	}
	u.portEntry.SetText("5005")
	u.notify.chatEntry.SetText("99")
	if d := dirtyIndices(u.forms); len(d) != 2 {
		t.Fatalf("dirty = %v, want settings and notify", d)
	}

	u.saveForms([]*formTab{u.forms.tabAt(tabSettings)}, true, nil)
	sent := svc.lastPost(t)
	if sent.Port != 5005 || sent.Secret != "s3cret" || sent.Telegram.BotToken != "****6789" || sent.Telegram.ChatID != "42" {
		t.Errorf("posted port %d secret %q token %q chat %q", sent.Port, sent.Secret, sent.Telegram.BotToken, sent.Telegram.ChatID)
	}
	if svc.cfg.Telegram.BotToken != "123456:SECRETTOKEN6789" {
		t.Errorf("stored token = %q, want it kept", svc.cfg.Telegram.BotToken)
	}
	if u.notify.chatEntry.Text != "99" {
		t.Errorf("notify chat entry = %q, the unsaved edit was lost", u.notify.chatEntry.Text)
	}
	if d := dirtyIndices(u.forms); len(d) != 1 || d[0] != tabTelegram {
		t.Errorf("dirty after saving settings = %v, want only notify", d)
	}
	if !u.forms.tabAt(tabSettings).bar.save.Disabled() {
		t.Error("settings Save still enabled after the save")
	}

	// A refresh (what a reconnect does) must not overwrite the edit either.
	u.initialLoad()
	if u.notify.chatEntry.Text != "99" {
		t.Errorf("refresh overwrote the unsaved chat id: %q", u.notify.chatEntry.Text)
	}
}

// A language switch rebuilds every widget; the edits come back in the new
// widgets and stay unsaved.
func TestRebuildKeepsUnsavedEdits(t *testing.T) {
	u, _ := newFakeServiceUI(t)
	u.secretEntry.SetText("n3w")
	u.presets.rows[0].name.SetText("Games")
	u.rebuild()
	if u.secretEntry.Text != "n3w" || u.presets.rows[0].name.Text != "Games" {
		t.Errorf("after rebuild: secret %q preset %q", u.secretEntry.Text, u.presets.rows[0].name.Text)
	}
	if d := dirtyIndices(u.forms); len(d) != 2 {
		t.Errorf("dirty after rebuild = %v, want settings and presets", d)
	}
	if !u.forms.tabAt(tabPresets).bar.dirty {
		t.Error("presets save bar not dirty after the rebuild")
	}
}

// The tab order of #128, with the form tabs where the constants say, and
// the forced switch to settings while the service is away going back to
// where the user was once it answers (the commands tab after a start).
func TestTabsAndTheForcedSettingsSwitch(t *testing.T) {
	u := newTestUI(t, LangEn, nil) // never connected
	if len(u.tabs.Items) != len(tabKeys) {
		t.Fatalf("%d tabs, want %d", len(u.tabs.Items), len(tabKeys))
	}
	for i, it := range u.tabs.Items {
		if it.Text != T(LangEn, tabKeys[i]) || it.Icon != nil {
			t.Errorf("tab %d = %q (icon %v), want %q without an icon", i, it.Text, it.Icon != nil, T(LangEn, tabKeys[i]))
		}
	}
	for _, i := range []int{tabPresets, tabShare, tabSmartThings, tabTelegram, tabSettings} {
		if u.forms.tabAt(i) == nil {
			t.Errorf("tab %d (%s) has no form", i, tabKeys[i])
		}
	}
	if u.curTab != tabSettings || u.tabs.SelectedIndex() != tabSettings {
		t.Fatalf("disconnected start shows tab %d, want settings", u.tabs.SelectedIndex())
	}
	if !u.tabs.Items[tabCommands].Disabled() {
		t.Error("the commands tab is usable without the service")
	}

	u.connected.Store(true)
	u.applyConnected(true)
	if u.tabs.SelectedIndex() != tabCommands || u.tabs.Items[tabCommands].Disabled() {
		t.Errorf("after connecting: tab %d, want commands (enabled)", u.tabs.SelectedIndex())
	}

	// The user moves on, the service restarts, and comes back.
	u.selectTab(tabShare)
	u.connected.Store(false)
	u.applyConnected(false)
	if u.tabs.SelectedIndex() != tabSettings {
		t.Errorf("service gone: tab %d, want settings", u.tabs.SelectedIndex())
	}
	u.connected.Store(true)
	u.applyConnected(true)
	if u.tabs.SelectedIndex() != tabShare {
		t.Errorf("service back: tab %d, want sharing again", u.tabs.SelectedIndex())
	}
}
