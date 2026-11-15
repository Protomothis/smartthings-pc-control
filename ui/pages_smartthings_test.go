package ui

import (
	"slices"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// The SmartThings page fills from the config, is dirty only for its own
// two fields, and writes nothing else.
func TestSTPageForm(t *testing.T) {
	u := New(fakeAPI{}, "dev", loadPrefs(""), func() {})
	p := u.pages[pageSmartThings].(*stPage)
	base := gui.Config{
		Port: 5001, Secret: "s",
		SmartThings: gui.SmartThingsConfig{AllowedHubs: []string{" 192.168.1.20 ", "192.168.1.20"}, WoLMAC: "AA", ExposeSession: true},
		Telegram:    gui.TelegramConfig{BotToken: "****6789"},
	}
	p.fill(u, base)
	if !slices.Equal(p.form.Hubs, []string{"192.168.1.20"}) || p.form.WoLMAC != "AA" {
		t.Fatalf("fill = %+v", p.form)
	}
	if p.dirty(u, base) {
		t.Fatal("dirty right after fill")
	}

	p.form.Hubs = gui.AddHub(p.form.Hubs, "192.168.1.21")
	p.form.WoLMAC = ""
	if !p.dirty(u, base) {
		t.Fatal("not dirty after edits")
	}
	cfg := base
	if err := p.apply(u, &cfg); err != nil {
		t.Fatal(err)
	}
	st := cfg.SmartThings
	if !slices.Equal(st.AllowedHubs, []string{"192.168.1.20", "192.168.1.21"}) || st.WoLMAC != "" {
		t.Errorf("apply wrote %+v", st)
	}
	if !st.ExposeSession || cfg.Port != 5001 || cfg.Telegram.BotToken != "****6789" {
		t.Errorf("apply touched other fields: %+v", cfg)
	}
	if len(base.SmartThings.AllowedHubs) != 2 || base.SmartThings.AllowedHubs[0] != " 192.168.1.20 " {
		t.Errorf("apply wrote into the baseline: %v", base.SmartThings.AllowedHubs)
	}

	// Clearing the list sends [] (not null: that keeps the stored list).
	p.form.Hubs = gui.RemoveHub(gui.RemoveHub(p.form.Hubs, "192.168.1.20"), "192.168.1.21")
	cfg = base
	_ = p.apply(u, &cfg)
	if cfg.SmartThings.AllowedHubs == nil || len(cfg.SmartThings.AllowedHubs) != 0 {
		t.Errorf("cleared list = %#v", cfg.SmartThings.AllowedHubs)
	}
}

// The connected hub can be added only while it is not on the list.
func TestSTPageCanAddHub(t *testing.T) {
	p := &stPage{form: gui.STForm{Hubs: []string{"10.0.0.1"}}}
	cases := []struct {
		hub  gui.STHub
		want bool
	}{
		{gui.STHub{}, false},
		{gui.STHub{Connected: true}, false},
		{gui.STHub{Connected: true, IP: "10.0.0.1"}, false},
		{gui.STHub{Connected: true, IP: "10.0.0.2"}, true},
	}
	for _, c := range cases {
		if got := p.canAddHub(c.hub); got != c.want {
			t.Errorf("canAddHub(%+v) = %v", c.hub, got)
		}
	}
}

// The WoL list: automatic names its pick, an adapter shows its WoL state,
// and a saved MAC no adapter has keeps a row of its own.
func TestSTWoLItems(t *testing.T) {
	u := New(fakeAPI{}, "dev", loadPrefs(""), func() {})
	info := gui.STWoLInfo{
		Auto:     &gui.STWoLSelected{Name: "eth", MAC: "AA"},
		Adapters: []gui.STWoLAdapter{{Name: "eth", MAC: "AA", WoLEnabled: true, WoLCapable: true}, {Name: "wifi", MAC: "BB", WoLCapable: true}},
	}
	_, macs := gui.STWoLOptions(u.language(), info, "CC")
	if !slices.Equal(macs, []string{"", "AA", "BB", "CC"}) {
		t.Fatalf("macs = %v", macs)
	}
	auto := stWoLItem(u, info, "", true)
	if auto.sub != "eth · AA" || auto.pill != "" {
		t.Errorf("auto = %+v", auto)
	}
	if it := stWoLItem(u, info, "aa", false); it.title != "eth" || it.tone != toneOK {
		t.Errorf("eth = %+v", it)
	}
	if it := stWoLItem(u, info, "BB", false); it.tone != toneWarn {
		t.Errorf("wifi = %+v", it)
	}
	if it := stWoLItem(u, info, "CC", false); it.title != u.t("ui.smartthings.wol.missing") || it.sub != "CC" {
		t.Errorf("missing = %+v", it)
	}
	if it := stWoLItem(u, gui.STWoLInfo{}, "", true); it.sub != u.t("ui.smartthings.wol.auto.none") {
		t.Errorf("auto without adapters = %+v", it)
	}
}

// A client without the adapter list hides the section without a request.
func TestSTPageNoAdapterAPI(t *testing.T) {
	u := New(fakeAPI{}, "dev", loadPrefs(""), func() {})
	p := u.pages[pageSmartThings].(*stPage)
	p.reload(u)
	if !p.net.unsupported {
		t.Error("fake API without GetWoLStatus should mark the list unsupported")
	}
}
