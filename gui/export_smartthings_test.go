package gui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// stBaseConfig is what GET /api/config hands the SmartThings section, with
// the telegram/notify fields of another tab alongside (a save from this
// section must leave them alone).
func stBaseConfig() Config {
	return Config{
		Port: 5001, Secret: "s3cret",
		Telegram: TelegramConfig{Enabled: true, BotToken: "****6789", ChatID: "42"},
		Notify:   map[string]map[string]bool{"remote": {"received": true}},
		SmartThings: SmartThingsConfig{
			AllowedHubs: []string{"192.168.1.20"},
		},
	}
}

func TestSTRelKey(t *testing.T) {
	cases := []struct {
		age  time.Duration
		key  string
		want int
	}{
		{-5 * time.Second, "st.rel.now", -1}, // service clock ahead
		{300 * time.Millisecond, "st.rel.now", -1},
		{3 * time.Second, "st.rel.sec", 3},
		{59 * time.Second, "st.rel.sec", 59},
		{time.Minute, "st.rel.min", 1},
		{90 * time.Second, "st.rel.min", 1},
		{59 * time.Minute, "st.rel.min", 59},
		{time.Hour, "st.rel.hour", 1},
		{23 * time.Hour, "st.rel.hour", 23},
		{24 * time.Hour, "st.rel.day", 1},
		{50 * time.Hour, "st.rel.day", 2},
	}
	for _, c := range cases {
		key, n := stRelKey(c.age)
		if key != c.key || n != c.want {
			t.Errorf("stRelKey(%v) = %q,%d; want %q,%d", c.age, key, n, c.key, c.want)
		}
	}
}

// ---- this PC's id and the search diagnostics (#95) -------------------------

func TestSTMachineIDShort(t *testing.T) {
	cases := map[string]string{
		"58bff996-6d9a-4a2f-8e1c-0123456789ab": "58bff996",
		"58bff996":                             "58bff996",
		"short":                                "short",
		"":                                     "",
	}
	for id, want := range cases {
		if got := stMachineIDShort(id); got != want {
			t.Errorf("stMachineIDShort(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestNormalizeHubs(t *testing.T) {
	got := normalizeHubs([]string{" 192.168.1.20 ", "", "192.168.1.20", "10.0.0.2"})
	if !slices.Equal(got, []string{"192.168.1.20", "10.0.0.2"}) {
		t.Errorf("normalizeHubs = %v", got)
	}
	// Never nil: nil would mean "keep the stored list" to the service.
	if got := normalizeHubs(nil); got == nil || len(got) != 0 {
		t.Errorf("normalizeHubs(nil) = %#v, want an empty non-nil slice", got)
	}
}

func TestAddRemoveHub(t *testing.T) {
	hubs := addHub(nil, "192.168.1.20")
	if !slices.Equal(hubs, []string{"192.168.1.20"}) {
		t.Fatalf("addHub to an empty list = %v", hubs)
	}
	if got := addHub(hubs, "192.168.1.20"); !slices.Equal(got, hubs) {
		t.Errorf("adding a listed hub changed the list: %v", got)
	}
	if got := addHub(hubs, "  "); !slices.Equal(got, hubs) {
		t.Errorf("adding a blank hub changed the list: %v", got)
	}
	hubs = addHub(hubs, "10.0.0.2")
	if !slices.Equal(hubs, []string{"192.168.1.20", "10.0.0.2"}) {
		t.Fatalf("addHub appended wrongly: %v", hubs)
	}
	hubs = removeHub(hubs, "192.168.1.20")
	if !slices.Equal(hubs, []string{"10.0.0.2"}) {
		t.Errorf("removeHub = %v", hubs)
	}
	if got := removeHub(hubs, "1.2.3.4"); !slices.Equal(got, hubs) {
		t.Errorf("removing an unlisted hub changed the list: %v", got)
	}
	if got := removeHub(hubs, "10.0.0.2"); len(got) != 0 || got == nil {
		t.Errorf("removing the last hub = %#v, want an empty non-nil slice", got)
	}
}

func TestSTStateFromConfigCopiesHubs(t *testing.T) {
	base := stBaseConfig()
	s := stStateFromConfig(base)
	s.Hubs = addHub(s.Hubs, "10.0.0.2")
	if len(base.SmartThings.AllowedHubs) != 1 {
		t.Errorf("editing the form state wrote into the baseline: %v", base.SmartThings.AllowedHubs)
	}
}

// The SmartThings tab writes the hub list and the WoL adapter only; the
// session switches in the same smartthings object are the sharing tab's.
func TestSTApplyToTouchesOnlyItsFields(t *testing.T) {
	base := stBaseConfig()
	base.SmartThings.ExposeSession, base.SmartThings.ExposeSessionUser = true, true
	s := stFormState{Hubs: []string{"10.0.0.2"}, WoLMAC: "B4-2E-99-45-B4-F5"}
	cfg := s.applyTo(base)

	if cfg.Port != base.Port || cfg.Secret != base.Secret ||
		!reflect.DeepEqual(cfg.Telegram, base.Telegram) || !reflect.DeepEqual(cfg.Notify, base.Notify) {
		t.Errorf("applyTo changed fields owned by another tab: %+v", cfg)
	}
	if !cfg.SmartThings.ExposeSession || !cfg.SmartThings.ExposeSessionUser {
		t.Errorf("applyTo reset the sharing tab's session switches: %+v", cfg.SmartThings)
	}
	if !slices.Equal(cfg.SmartThings.AllowedHubs, []string{"10.0.0.2"}) || cfg.SmartThings.WoLMAC != "B4-2E-99-45-B4-F5" {
		t.Errorf("applyTo smartthings = %+v", cfg.SmartThings)
	}
	// An emptied list must travel as [] so the service clears it.
	cleared := stFormState{}.applyTo(base)
	if cleared.SmartThings.AllowedHubs == nil {
		t.Error("an empty allow list was sent as null (the service reads that as 'keep')")
	}
}

func TestShareUserNameGatedBySessionExposure(t *testing.T) {
	base := stBaseConfig()
	// The user toggle keeps its position while exposure is off, but what is
	// saved — and what counts as a change — is the gated value.
	s := shareFormState{ExposeSession: false, ExposeSessionUser: true}
	if cfg := s.applyTo(base); cfg.SmartThings.ExposeSessionUser {
		t.Error("expose_session_user was saved while expose_session is off")
	}
	if s.dirty(base) {
		t.Error("a user-name toggle left on behind a disabled master marked the tab dirty")
	}
	s.ExposeSession = true
	if !s.dirty(base) {
		t.Error("turning session exposure on is not dirty")
	}
}

// The sharing tab writes the session switches, now playing and the watch
// list; the hub list next to them and media.enabled stay as they were.
func TestShareApplyToTouchesOnlyItsFields(t *testing.T) {
	base := stBaseConfig()
	base.Media.Enabled = true
	base.SmartThings.WoLMAC = "B4-2E-99-45-B4-F5"
	s := shareFormState{ExposeSession: true, NowPlaying: true}
	cfg := s.applyTo(base)
	if !slices.Equal(cfg.SmartThings.AllowedHubs, base.SmartThings.AllowedHubs) || cfg.SmartThings.WoLMAC != base.SmartThings.WoLMAC {
		t.Errorf("applyTo changed the SmartThings tab's fields: %+v", cfg.SmartThings)
	}
	if !cfg.Media.Enabled || !cfg.Media.NowPlaying || !cfg.SmartThings.ExposeSession {
		t.Errorf("applyTo media %+v smartthings %+v", cfg.Media, cfg.SmartThings)
	}
	if cfg.Port != base.Port || cfg.Secret != base.Secret || !reflect.DeepEqual(cfg.Telegram, base.Telegram) {
		t.Errorf("applyTo changed fields owned by another tab: %+v", cfg)
	}
	for _, mutate := range []func(*shareFormState){
		func(s *shareFormState) { s.ExposeSession = true },
		func(s *shareFormState) { s.NowPlaying = true },
		func(s *shareFormState) { s.Activity.Enabled = true },
	} {
		changed := shareStateFromConfig(base)
		mutate(&changed)
		if !changed.dirty(base) {
			t.Errorf("an edit went unnoticed: %+v", changed)
		}
		if changed.dirty(changed.applyTo(base)) {
			t.Errorf("still dirty after saving %+v", changed)
		}
	}
}

func TestSTDirty(t *testing.T) {
	base := stBaseConfig()
	s := stStateFromConfig(base)
	if s.dirty(base) {
		t.Fatal("a freshly filled section is dirty")
	}
	// Hub order and whitespace are not changes; the set is.
	s.Hubs = []string{" 192.168.1.20 "}
	if s.dirty(base) {
		t.Error("whitespace around a hub IP counts as a change")
	}
	for _, mutate := range []func(*stFormState){
		func(s *stFormState) { s.Hubs = addHub(s.Hubs, "10.0.0.2") },
		func(s *stFormState) { s.Hubs = removeHub(s.Hubs, "192.168.1.20") },
		func(s *stFormState) { s.WoLMAC = "B4-2E-99-45-B4-F5" },
	} {
		changed := stStateFromConfig(base)
		mutate(&changed)
		if !changed.dirty(base) {
			t.Errorf("an edit went unnoticed: %+v", changed)
		}
		// …and saving it makes the section clean again.
		if changed.dirty(changed.applyTo(base)) {
			t.Errorf("the section stayed dirty after saving %+v", changed)
		}
	}
}

// --- WoL adapter dropdown (#96) ---------------------------------------------

// stWoLInfo is a three-adapter PC as /api/st/hub describes it: automatic
// currently lands on Ethernet, Wi-Fi can do WoL but has it off, and the
// Hyper-V switch cannot do it at all.
func stWoLInfo() STWoLInfo {
	return STWoLInfo{
		Auto: &STWoLSelected{
			Name: "이더넷", MAC: "B4-2E-99-45-B4-F5", IP: "192.168.1.30",
			WoLEnabled: true, WoLCapable: true, Source: "auto",
		},
		Adapters: []STWoLAdapter{
			{Name: "이더넷", MAC: "B4-2E-99-45-B4-F5", IP: "192.168.1.30", WoLEnabled: true, WoLCapable: true, Selected: true},
			{Name: "Wi-Fi", MAC: "11-22-33-44-55-66", IP: "192.168.1.31", WoLCapable: true},
			{Name: "vEthernet (Default Switch)", MAC: "00-15-5D-01-02-03", IP: "172.20.0.1"},
		},
	}
}

func TestSTWoLOptionLabels(t *testing.T) {
	info := stWoLInfo()
	cases := []struct {
		lang Lang
		auto string
		rows []string
	}{
		{LangKo, "자동 (이더넷 · B4-2E-99-45-B4-F5)", []string{
			"이더넷 · B4-2E-99-45-B4-F5 · WoL 켜짐",
			"Wi-Fi · 11-22-33-44-55-66 · WoL 꺼짐",
			"vEthernet (Default Switch) · 00-15-5D-01-02-03 · WoL 미지원",
		}},
		{LangEn, "Automatic (이더넷 · B4-2E-99-45-B4-F5)", []string{
			"이더넷 · B4-2E-99-45-B4-F5 · WoL on",
			"Wi-Fi · 11-22-33-44-55-66 · WoL off",
			"vEthernet (Default Switch) · 00-15-5D-01-02-03 · WoL not supported",
		}},
	}
	for _, tc := range cases {
		if got := stWoLAutoOption(tc.lang, info.Auto); got != tc.auto {
			t.Errorf("%s auto option = %q, want %q", tc.lang, got, tc.auto)
		}
		for i, want := range tc.rows {
			if got := stWoLAdapterOption(tc.lang, info.Adapters[i]); got != want {
				t.Errorf("%s adapter %d = %q, want %q", tc.lang, i, got, want)
			}
		}
		// Nothing to pick from: the entry says so instead of showing an
		// empty pair of brackets.
		if got := stWoLAutoOption(tc.lang, nil); got == "" || strings.Contains(got, "()") {
			t.Errorf("%s auto option with no adapter = %q", tc.lang, got)
		}
	}
}

func TestSTWoLOptions(t *testing.T) {
	info := stWoLInfo()

	labels, macs := stWoLOptions(LangKo, info, "")
	if len(labels) != 4 || len(macs) != 4 {
		t.Fatalf("options = %v / %v, want automatic + three adapters", labels, macs)
	}
	if macs[0] != "" {
		t.Errorf("the first option saves %q, want \"\" (automatic)", macs[0])
	}
	if !slices.Equal(macs[1:], []string{"B4-2E-99-45-B4-F5", "11-22-33-44-55-66", "00-15-5D-01-02-03"}) {
		t.Errorf("option MACs = %v", macs)
	}

	// A pinned MAC that exists adds nothing: it is already on the list.
	if _, macs := stWoLOptions(LangKo, info, "11-22-33-44-55-66"); len(macs) != 4 {
		t.Errorf("a known MAC added an extra option: %v", macs)
	}
	// One that does not exist keeps an entry, so the dropdown always shows
	// what is actually saved.
	labels, macs = stWoLOptions(LangKo, info, "DE-AD-BE-EF-00-01")
	if len(macs) != 5 || macs[4] != "DE-AD-BE-EF-00-01" {
		t.Fatalf("an unknown MAC lost its option: %v", macs)
	}
	if !strings.Contains(labels[4], "DE-AD-BE-EF-00-01") {
		t.Errorf("the unknown-MAC label does not show the MAC: %q", labels[4])
	}
	// An adapter without a MAC cannot be woken and is not offered.
	noMAC := STWoLInfo{Adapters: []STWoLAdapter{{Name: "Nameless"}}}
	if _, macs := stWoLOptions(LangEn, noMAC, ""); len(macs) != 1 {
		t.Errorf("an adapter without a MAC was offered: %v", macs)
	}
}

// The hint under the dropdown describes the adapter the choice resolves
// to — including the service's own fallback to automatic when the pinned
// MAC matches nothing — and names it.
func TestSTWoLPickAndLine(t *testing.T) {
	info := stWoLInfo()

	if got := stWoLPick(info, ""); got == nil || got.Name != "이더넷" || got.Source != "auto" {
		t.Errorf("automatic resolved to %+v, want the auto pick", got)
	}
	if got := stWoLPick(info, "11-22-33-44-55-66"); got == nil || got.Name != "Wi-Fi" || got.Source != "manual" {
		t.Errorf("a pinned MAC resolved to %+v, want Wi-Fi", got)
	}
	if got := stWoLPick(info, "DE-AD-BE-EF-00-01"); got == nil || got.Name != "이더넷" {
		t.Errorf("an unknown MAC resolved to %+v, want the automatic fallback", got)
	}
	if got := stWoLPick(STWoLInfo{}, ""); got != nil {
		t.Errorf("a PC with no adapters resolved to %+v, want nil", got)
	}

	for _, l := range []Lang{LangKo, LangEn} {
		// WoL off: the line has to name the adapter, not adapters in general.
		off := stWoLLine(l, stWoLPick(info, "11-22-33-44-55-66"))
		if !strings.Contains(off, "Wi-Fi") {
			t.Errorf("%s: the 'WoL is off' hint does not name the adapter: %q", l, off)
		}
		on := stWoLLine(l, stWoLPick(info, ""))
		if !strings.Contains(on, "이더넷") || on == off {
			t.Errorf("%s: the ready hint = %q (off hint %q)", l, on, off)
		}
		na := stWoLLine(l, stWoLPick(info, "00-15-5D-01-02-03"))
		if !strings.Contains(na, "vEthernet") || na == off {
			t.Errorf("%s: the unsupported hint = %q", l, na)
		}
		if none := stWoLLine(l, nil); none == "" || strings.Contains(none, "%!") {
			t.Errorf("%s: the no-adapter hint = %q", l, none)
		}
	}
}

// Choosing an adapter saves its MAC; choosing automatic saves "".
func TestSTWoLMACRoundTrip(t *testing.T) {
	base := stBaseConfig()
	s := stStateFromConfig(base)
	s.WoLMAC = "B4-2E-99-45-B4-F5"
	saved := s.applyTo(base)
	if saved.SmartThings.WoLMAC != "B4-2E-99-45-B4-F5" {
		t.Fatalf("wol_mac = %q after saving", saved.SmartThings.WoLMAC)
	}
	back := stStateFromConfig(saved)
	if back.WoLMAC != "B4-2E-99-45-B4-F5" || back.dirty(saved) {
		t.Errorf("reloading the saved config = %+v, dirty=%v", back, back.dirty(saved))
	}
	back.WoLMAC = "" // back to automatic
	if !back.dirty(saved) {
		t.Error("switching back to automatic was not noticed")
	}
	if got := back.applyTo(saved).SmartThings.WoLMAC; got != "" {
		t.Errorf("automatic saved %q, want \"\"", got)
	}
}
