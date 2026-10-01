package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeService answers the API the window uses: GET/POST /api/config with
// the service's token rules (masked on GET; "" or "****…" keeps, "-"
// clears, anything else replaces), "{}" for every other GET and ok for
// every other POST.
type fakeService struct {
	mu    sync.Mutex
	cfg   Config // BotToken in plain text
	posts []Config
}

func (f *fakeService) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/config" && r.Method == http.MethodGet:
		out := f.cfg
		out.Telegram.BotToken = maskToken(f.cfg.Telegram.BotToken)
		out.Telegram.BotTokenSet = f.cfg.Telegram.BotToken != ""
		json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/api/config":
		var in Config
		json.NewDecoder(r.Body).Decode(&in)
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
		w.Write([]byte(`{"status":"ok","message":"saved"}`))
	case r.Method == http.MethodGet:
		w.Write([]byte(`{}`))
	default:
		w.Write([]byte(`{"status":"ok"}`))
	}
}

func (f *fakeService) lastPost(t *testing.T) Config {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) == 0 {
		t.Fatal("nothing was posted")
	}
	return f.posts[len(f.posts)-1]
}

func newFakeServiceUI(t *testing.T) (*ui, *fakeService) {
	t.Helper()
	svc := &fakeService{cfg: storedConfig()}
	svc.cfg.Telegram.BotToken = "123456:SECRETTOKEN6789"
	srv := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(srv.Close)
	u := newTestUI(t, LangEn, &Client{base: srv.URL, http: srv.Client()})
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
	if d := dirtyIndices(u.forms); len(d) != 1 || d[0] != tabNotify {
		t.Errorf("dirty after saving settings = %v, want only notify", d)
	}
	if u.forms.tabAt(tabSettings).bar.save.Disabled() == false {
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
