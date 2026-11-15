package ui

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// tgConfigAPI is a configAPI with the /api/telegram/* helpers.
type tgConfigAPI struct {
	*configAPI
	testToken, testChat string
	testErr             error
	chats               []gui.TelegramChat
	chatsToken          string
}

func (a *tgConfigAPI) TestTelegram(token, chatID string) error {
	a.testToken, a.testChat = token, chatID
	return a.testErr
}
func (a *tgConfigAPI) TelegramMe() (string, string, error) { return "my_bot", "My bot", nil }
func (a *tgConfigAPI) TelegramChats(token string) ([]gui.TelegramChat, error) {
	a.chatsToken = token
	return a.chats, nil
}

func tgConfig() gui.Config {
	cfg := baseConfig()
	cfg.Telegram.ControlEnabled = true
	cfg.Telegram.AllowedChatIDs = []string{"1", "-100"}
	cfg.Telegram.QuietHours = gui.QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	return cfg
}

func tgUI(t *testing.T, api *configAPI) (*UI, *tgPage) {
	t.Helper()
	u := formUI(t, api)
	u.open(pageTelegram)
	return u, u.pages[pageTelegram].(*tgPage)
}

// fill shows the config with an empty token field, and nothing is dirty.
func TestTelegramFill(t *testing.T) {
	u, p := tgUI(t, &configAPI{cfg: tgConfig()})
	if p.token.ed.Text() != "" || p.masked != "****6789" {
		t.Fatalf("token field %q, masked %q", p.token.ed.Text(), p.masked)
	}
	if p.chat.Text() != "1" || !p.control.Value || !slices.Equal(p.allowed, []string{"1", "-100"}) {
		t.Fatalf("fill: chat %q control %v allowed %v", p.chat.Text(), p.control.Value, p.allowed)
	}
	p.read()
	if u.formDirty(p) {
		t.Fatal("dirty right after fill")
	}
}

// Each edit makes the page dirty and lands in the saved config; the
// other pages' fields travel unchanged.
func TestTelegramApply(t *testing.T) {
	u, p := tgUI(t, &configAPI{cfg: tgConfig()})
	base := *u.formSt.base
	edits := map[string]func(){
		"chat":    func() { p.chat.SetText("42") },
		"enabled": func() { p.enabled.Value = false },
		"allowed": func() { p.addAllowed("7, 1") },
		"quiet":   func() { p.form.Quiet.Start = stepTime(p.form.Quiet.Start, 1) },
		"detail":  func() { p.form.Detail = "simple" },
		"pcname":  func() { p.pcName.SetText("서재 PC") },
		"kind":    func() { p.toggleKind(0, p.cat[0].Kinds[0]) },
		"token":   func() { p.token.ed.SetText("123456:ABCDEFGHIJKLMNOP") },
		"clear":   func() { p.clearToken = true },
	}
	for name, edit := range edits {
		u.revertForm(p)
		edit()
		p.read()
		if !p.dirty(u, base) {
			t.Errorf("%s: not dirty", name)
		}
	}
	u.revertForm(p)
	p.chat.SetText("42")
	p.addAllowed("7, 1")
	p.clearToken = true
	p.read()
	cfg := cloneConfig(base)
	if err := p.apply(u, &cfg); err != nil {
		t.Fatal(err)
	}
	tg := cfg.Telegram
	if tg.ChatID != "42" || tg.BotToken != "-" || !slices.Equal(tg.AllowedChatIDs, []string{"1", "-100", "7"}) {
		t.Fatalf("applied %+v", tg)
	}
	if cfg.Port != base.Port || cfg.GraceSeconds != base.GraceSeconds {
		t.Fatal("touched another page's fields")
	}
	if !slices.Equal(base.Telegram.AllowedChatIDs, []string{"1", "-100"}) {
		t.Fatal("apply wrote into the baseline's slice")
	}
	// an untouched token field keeps the stored token
	u.revertForm(p)
	p.read()
	cfg = cloneConfig(base)
	_ = p.apply(u, &cfg)
	if cfg.Telegram.BotToken != "****6789" {
		t.Fatalf("kept token sent as %q", cfg.Telegram.BotToken)
	}
}

// A save posts the typed token and empties the field afterwards.
func TestTelegramSaveClearsToken(t *testing.T) {
	api := &configAPI{cfg: tgConfig()}
	u, p := tgUI(t, api)
	p.token.ed.SetText("123456:ABCDEFGHIJKLMNOP")
	p.token.shown = true
	p.read()
	u.saveForm(p)
	drain(t, u)
	if len(api.saved) != 1 || api.saved[0].Telegram.BotToken != "123456:ABCDEFGHIJKLMNOP" {
		t.Fatalf("saved %+v", api.saved)
	}
	if p.token.ed.Text() != "" || p.token.shown || p.clearToken {
		t.Fatal("token field not emptied after the save")
	}
	p.read()
	if u.formDirty(p) {
		t.Fatal("dirty after the save")
	}
}

// The last kind off takes its category off; a category turned back on
// starts with all kinds on; 모두 끔/켬 set everything.
func TestTelegramCatalogueCascade(t *testing.T) {
	u, p := tgUI(t, &configAPI{cfg: tgConfig()})
	i := 2 // power: every kind on by default
	for _, k := range p.cat[i].Kinds {
		p.toggleKind(i, k)
	}
	if p.masters[i].Value {
		t.Fatal("category still on with every kind off")
	}
	p.masters[i].Value = true
	p.masterTurned(i)
	for _, k := range p.cat[i].Kinds {
		if !p.kindOn(i, k) {
			t.Fatalf("%s off after turning the category on", k)
		}
	}
	p.setAll(false)
	p.read()
	cfg := cloneConfig(*u.formSt.base)
	_ = p.apply(u, &cfg)
	for cat, kinds := range cfg.Notify {
		for k, on := range kinds {
			if on {
				t.Errorf("%s.%s still on after 모두 끔", cat, k)
			}
		}
	}
}

func TestStepTime(t *testing.T) {
	cases := []struct {
		in   string
		dir  int
		want string
	}{
		{"22:00", 1, "22:30"}, {"22:00", -1, "21:30"}, {"23:30", 1, "00:00"}, {"00:00", -1, "23:30"},
		{"22:15", 1, "22:30"}, {"22:15", -1, "22:00"}, {"bad", 1, "00:00"},
	}
	for _, c := range cases {
		if got := stepTime(c.in, c.dir); got != c.want {
			t.Errorf("stepTime(%q, %d) = %q, want %q", c.in, c.dir, got, c.want)
		}
	}
}

// The test and the chat lookup use the typed token, else the stored one;
// a found chat can be allowed.
func TestTelegramActions(t *testing.T) {
	api := &tgConfigAPI{configAPI: &configAPI{cfg: tgConfig()},
		chats: []gui.TelegramChat{{ChatID: "55", Title: "집"}}}
	u, p := tgUI(t, api.configAPI)
	u.api = api
	p.chat.SetText(" 42 ")
	p.sendTest(u)
	waitTasks(t, u, func() bool { return !p.testBusy })
	if api.testToken != "****6789" || api.testChat != "42" || p.test.tone != toneOK {
		t.Fatalf("test sent %q/%q, result %+v", api.testToken, api.testChat, p.test)
	}
	api.testErr = errors.New("chat not found")
	p.sendTest(u)
	waitTasks(t, u, func() bool { return !p.testBusy })
	if p.test.tone != toneBad || p.test.text != "chat not found" {
		t.Fatalf("failed test shown as %+v", p.test)
	}
	p.token.ed.SetText("999:NEW")
	p.findChats(u)
	waitTasks(t, u, func() bool { return !p.finding })
	if api.chatsToken != "999:NEW" || p.found == nil || len(*p.found) != 1 {
		t.Fatalf("lookup with %q found %v", api.chatsToken, p.found)
	}
	p.addAllowed((*p.found)[0].ChatID)
	if !slices.Contains(p.allowed, "55") {
		t.Fatal("found chat not allowed")
	}
	// without the helpers (an old client) the page says so
	u.api = fakeAPI{}
	p.sendTest(u)
	if p.test.tone != toneBad || p.testBusy {
		t.Fatal("no helpers: test not refused")
	}
}

// waitTasks runs onWindow's queue until done reports true.
func waitTasks(t *testing.T, u *UI, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		u.runTasks()
		if time.Now().After(deadline) {
			t.Fatal("never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
