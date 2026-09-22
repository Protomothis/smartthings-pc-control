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
			Discovery:   true,
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

func TestSTHubLine(t *testing.T) {
	now := time.Date(2026, 9, 17, 23, 0, 0, 0, time.UTC)
	seen := now.Add(-3 * time.Second).Format(time.RFC3339)
	hub := STHub{Connected: true, IP: "192.168.1.20", DriverVersion: "1.0.0", LastSeen: seen}

	ko := (&ui{lang: LangKo}).stHubLine(hub, now)
	if ko != "허브 192.168.1.20 · 드라이버 v1.0.0 · 마지막 확인 3초 전" {
		t.Errorf("ko line = %q", ko)
	}
	en := (&ui{lang: LangEn}).stHubLine(hub, now)
	if en != "Hub 192.168.1.20 · driver v1.0.0 · last seen 3s ago" {
		t.Errorf("en line = %q", en)
	}

	// Not connected (and the "never seen" zero value) get the install hint.
	for _, h := range []STHub{{}, {Connected: false, IP: "192.168.1.20"}, {Connected: true}} {
		if got := (&ui{lang: LangEn}).stHubLine(h, now); got != T(LangEn, "st.hub.none") {
			t.Errorf("stHubLine(%+v) = %q, want the no-hub line", h, got)
		}
	}
	// An unknown driver version must not break the format string.
	got := (&ui{lang: LangEn}).stHubLine(STHub{Connected: true, IP: "10.0.0.2", LastSeen: seen}, now)
	if strings.Contains(got, "%") || !strings.Contains(got, "driver v?") {
		t.Errorf("missing driver version renders as %q", got)
	}
	// An unparseable timestamp falls back instead of printing a zero time.
	got = (&ui{lang: LangKo}).stHubLine(STHub{Connected: true, IP: "10.0.0.2", DriverVersion: "1.0.0"}, now)
	if !strings.Contains(got, T(LangKo, "st.rel.unknown")) {
		t.Errorf("empty last_seen renders as %q", got)
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

func TestSTApplyToTouchesOnlySmartThings(t *testing.T) {
	base := stBaseConfig()
	s := stFormState{Discovery: false, ExposeSession: true, ExposeSessionUser: true, Hubs: []string{"10.0.0.2"}}
	cfg := s.applyTo(base)

	if cfg.Port != base.Port || cfg.Secret != base.Secret ||
		!reflect.DeepEqual(cfg.Telegram, base.Telegram) || !reflect.DeepEqual(cfg.Notify, base.Notify) {
		t.Errorf("applyTo changed fields owned by another tab: %+v", cfg)
	}
	want := SmartThingsConfig{Discovery: false, AllowedHubs: []string{"10.0.0.2"}, ExposeSession: true, ExposeSessionUser: true}
	if cfg.SmartThings.Discovery != want.Discovery || cfg.SmartThings.ExposeSession != want.ExposeSession ||
		cfg.SmartThings.ExposeSessionUser != want.ExposeSessionUser ||
		!slices.Equal(cfg.SmartThings.AllowedHubs, want.AllowedHubs) {
		t.Errorf("applyTo smartthings = %+v, want %+v", cfg.SmartThings, want)
	}
	// An emptied list must travel as [] so the service clears it.
	cleared := stFormState{}.applyTo(base)
	if cleared.SmartThings.AllowedHubs == nil {
		t.Error("an empty allow list was sent as null (the service reads that as 'keep')")
	}
}

func TestSTUserNameGatedBySessionExposure(t *testing.T) {
	base := stBaseConfig()
	// The user toggle keeps its position while exposure is off, but what is
	// saved — and what counts as a change — is the gated value.
	s := stFormState{Discovery: true, ExposeSession: false, ExposeSessionUser: true, Hubs: base.SmartThings.AllowedHubs}
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
		func(s *stFormState) { s.Discovery = !s.Discovery },
		func(s *stFormState) { s.ExposeSession = true },
		func(s *stFormState) { s.Hubs = addHub(s.Hubs, "10.0.0.2") },
		func(s *stFormState) { s.Hubs = removeHub(s.Hubs, "192.168.1.20") },
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

func TestSTTranslations(t *testing.T) {
	keys := []string{
		"network.wol.section", "st.section", "st.hub.loading", "st.hub.connected", "st.hub.none",
		"st.rel.now", "st.rel.sec", "st.rel.min", "st.rel.hour", "st.rel.day", "st.rel.unknown",
		"st.discovery", "st.discovery.hint", "st.session", "st.session.hint",
		"st.session.user", "st.session.user.hint",
		"st.hubs", "st.hubs.add", "st.hubs.remove", "st.hubs.empty", "st.hubs.hint", "st.secret.hint",
	}
	for _, key := range keys {
		for _, l := range []Lang{LangKo, LangEn} {
			if T(l, key) == key {
				t.Errorf("missing %s translation for %s", l, key)
			}
		}
	}
}
