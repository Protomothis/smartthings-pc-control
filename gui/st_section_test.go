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

func TestSTMachineIDLine(t *testing.T) {
	const id = "58bff996-6d9a-4a2f-8e1c-0123456789ab"
	if got := (&ui{lang: LangKo}).stMachineIDLine(id); got != "이 PC의 ID 58bff996" {
		t.Errorf("ko line = %q", got)
	}
	if got := (&ui{lang: LangEn}).stMachineIDLine(id); got != "This PC's ID 58bff996" {
		t.Errorf("en line = %q", got)
	}
	// Before the service answers there is no id to show — and no "%!s".
	for _, l := range []Lang{LangKo, LangEn} {
		got := (&ui{lang: l}).stMachineIDLine("")
		if got != T(l, "st.machineid.unknown") || strings.Contains(got, "%") {
			t.Errorf("%s line for an unknown id = %q", l, got)
		}
	}
}

func TestSTSearchLine(t *testing.T) {
	now := time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC)
	at := now.Add(-12 * time.Second).Format(time.RFC3339)
	search := &STLastSearch{IP: "192.168.1.105", At: at}

	cases := []struct {
		name   string
		state  STSSDPState
		ko, en string
	}{
		{
			name:  "everything in order",
			state: STSSDPState{Running: true, FirewallRule: true, LastSearch: search},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 OK · 마지막 검색 요청 192.168.1.105, 12초 전",
			en:    "Discovery responder on · Firewall rule OK · Last search from 192.168.1.105, 12s ago",
		},
		{
			name:  "no search has arrived yet",
			state: STSSDPState{Running: true, FirewallRule: true},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 OK · 검색 요청 없음",
			en:    "Discovery responder on · Firewall rule OK · No search yet",
		},
		{
			name:  "the firewall rule is missing",
			state: STSSDPState{Running: true},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 없음 (서비스를 다시 시작하면 만듭니다) · 검색 요청 없음",
			en:    "Discovery responder on · No firewall rule (restart the service to create it) · No search yet",
		},
		{
			// A responder with no socket answers nothing, so the rest of
			// the line would only distract.
			name:  "the responder could not start",
			state: STSSDPState{Running: false, FirewallRule: true, LastSearch: search},
			ko:    "검색 응답기 꺼짐 (소켓 열기 실패, 로그 확인)",
			en:    "Discovery responder off (no socket could be opened, check the log)",
		},
		{
			// An older service that does not send last_search yet, and a
			// malformed one: neither may print a bare "%!s(MISSING)".
			name:  "a last_search with no address",
			state: STSSDPState{Running: true, FirewallRule: true, LastSearch: &STLastSearch{At: at}},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 OK · 검색 요청 없음",
			en:    "Discovery responder on · Firewall rule OK · No search yet",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (&ui{lang: LangKo}).stSearchLine(c.state, now); got != c.ko {
				t.Errorf("ko = %q\nwant %q", got, c.ko)
			}
			if got := (&ui{lang: LangEn}).stSearchLine(c.state, now); got != c.en {
				t.Errorf("en = %q\nwant %q", got, c.en)
			}
		})
	}

	// An unparseable timestamp falls back instead of printing a zero time.
	got := (&ui{lang: LangKo}).stSearchLine(STSSDPState{
		Running: true, FirewallRule: true, LastSearch: &STLastSearch{IP: "10.0.0.2"},
	}, now)
	if !strings.Contains(got, T(LangKo, "st.rel.unknown")) || strings.Contains(got, "%") {
		t.Errorf("a last_search with no time renders as %q", got)
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
	s := stFormState{ExposeSession: true, ExposeSessionUser: true, Hubs: []string{"10.0.0.2"}}
	cfg := s.applyTo(base)

	if cfg.Port != base.Port || cfg.Secret != base.Secret ||
		!reflect.DeepEqual(cfg.Telegram, base.Telegram) || !reflect.DeepEqual(cfg.Notify, base.Notify) {
		t.Errorf("applyTo changed fields owned by another tab: %+v", cfg)
	}
	want := SmartThingsConfig{AllowedHubs: []string{"10.0.0.2"}, ExposeSession: true, ExposeSessionUser: true}
	if cfg.SmartThings.ExposeSession != want.ExposeSession ||
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
	s := stFormState{ExposeSession: false, ExposeSessionUser: true, Hubs: base.SmartThings.AllowedHubs}
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
		"st.machineid", "st.machineid.unknown", "st.machineid.copy", "st.machineid.copied",
		"st.search.loading", "st.search.on", "st.search.off", "st.search.fw.ok", "st.search.fw.missing",
		"st.search.last", "st.search.none", "st.search.hint",
		"st.session", "st.session.hint",
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
