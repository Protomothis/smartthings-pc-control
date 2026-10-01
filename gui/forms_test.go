package gui

import (
	"slices"
	"testing"
)

// The save coordinator is tested on the pure tab models, wired up the way
// the widgets wire them: Fill loads the model from a config, Dirty and
// ApplyTo are the model's own. Indices only tell the drafts apart.

type modelTabs struct {
	settings settingsFormState
	notify   notifyFormState
	st       stFormState
	share    shareFormState
	presets  presetsFormState
	f        *forms
	tabs     struct{ settings, notify, st, share, presets *formTab }
}

func newModelTabs() *modelTabs {
	m := &modelTabs{f: &forms{}}
	m.tabs.settings = m.f.register(&formTab{
		index:   tabSettings,
		Fill:    func(c Config) { m.settings = settingsStateFromConfig(c) },
		Dirty:   func(b Config) bool { return m.settings.dirty(b) },
		ApplyTo: func(c *Config) error { return m.settings.applyTo(c, LangEn) },
	})
	m.tabs.notify = m.f.register(&formTab{
		index: tabTelegram,
		Fill:  func(c Config) { m.notify = notifyStateFromConfig(c) },
		Dirty: func(b Config) bool { return m.notify.dirty(b) },
		ApplyTo: func(c *Config) error {
			*c = m.notify.applyTo(*c, LangEn)
			return nil
		},
	})
	m.tabs.st = m.f.register(&formTab{
		index: tabSmartThings,
		Fill:  func(c Config) { m.st = stStateFromConfig(c) },
		Dirty: func(b Config) bool { return m.st.dirty(b) },
		ApplyTo: func(c *Config) error {
			*c = m.st.applyTo(*c)
			return nil
		},
	})
	m.tabs.share = m.f.register(&formTab{
		index: tabShare,
		Fill:  func(c Config) { m.share = shareStateFromConfig(c) },
		Dirty: func(b Config) bool { return m.share.dirty(b) },
		ApplyTo: func(c *Config) error {
			*c = m.share.applyTo(*c)
			return nil
		},
	})
	m.tabs.presets = m.f.register(&formTab{
		index: tabPresets,
		Fill:  func(c Config) { m.presets = presetsStateFromConfig(c) },
		Dirty: func(b Config) bool { return m.presets.dirty(b) },
		ApplyTo: func(c *Config) error {
			next, err := m.presets.applyTo(*c)
			*c = next
			return err
		},
	})
	return m
}

// storedConfig is what GET /api/config returns: the secret in plain text
// (loopback), the bot token masked.
func storedConfig() Config {
	return Config{
		Port:          5001,
		Secret:        "s3cret",
		ShutdownGrace: true,
		GraceSeconds:  300,
		Telegram: TelegramConfig{
			Enabled: true, BotToken: "****6789", BotTokenSet: true, ChatID: "42",
			Detail: "full", QuietHours: QuietHours{Start: "22:00", End: "07:00"},
		},
		SmartThings: SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}, ExposeSession: true},
		Presets:     []Preset{{Slot: 1, Name: "Steam", Type: "program", Path: `C:\Steam\steam.exe`}},
	}
}

func dirtyIndices(f *forms) []int {
	var out []int
	for _, t := range f.dirtyTabs() {
		out = append(out, t.index)
	}
	return out
}

// The past bug class: a save from one tab wiped what another tab owns —
// the secret, or the bot token (sent back masked, which the service reads
// as "keep"). Every combination of tabs must carry both through.
func TestMergeKeepsSecretAndMaskedToken(t *testing.T) {
	m := newModelTabs()
	m.f.adopt(storedConfig(), nil)

	m.settings.Port = "5005"
	m.notify.ChatID = "99"
	m.st.Hubs = append(m.st.Hubs, "192.168.1.21")
	m.presets.Rows[0].Name = "Games"
	// The sharing tab edits the same smartthings and media objects as the
	// SmartThings and settings tabs, field by field.
	m.share.ExposeSession = false
	m.share.NowPlaying = true
	m.settings.Media = true

	all := []*formTab{m.tabs.settings, m.tabs.notify, m.tabs.st, m.tabs.share, m.tabs.presets}
	for _, saving := range [][]*formTab{
		all, {m.tabs.notify}, {m.tabs.st}, {m.tabs.share}, {m.tabs.presets}, {m.tabs.settings},
		{m.tabs.notify, m.tabs.presets}, {m.tabs.st, m.tabs.share}, {m.tabs.share, m.tabs.settings},
	} {
		cfg, err := m.f.merge(saving)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if cfg.Secret != "s3cret" {
			t.Errorf("secret = %q, want it kept", cfg.Secret)
		}
		if cfg.Telegram.BotToken != "****6789" {
			t.Errorf("bot token = %q, want the masked value sent back (keep)", cfg.Telegram.BotToken)
		}
		// Only the saving tabs' edits are in the merge.
		want := map[*formTab]bool{}
		for _, s := range saving {
			want[s] = true
		}
		if got := cfg.Port == 5005; got != want[m.tabs.settings] {
			t.Errorf("port 5005 in merge = %v, want %v", got, want[m.tabs.settings])
		}
		if got := cfg.Telegram.ChatID == "99"; got != want[m.tabs.notify] {
			t.Errorf("chat id 99 in merge = %v, want %v", got, want[m.tabs.notify])
		}
		if got := len(cfg.SmartThings.AllowedHubs) == 2; got != want[m.tabs.st] {
			t.Errorf("new hub in merge = %v, want %v", got, want[m.tabs.st])
		}
		if got := cfg.Presets[0].Name == "Games"; got != want[m.tabs.presets] {
			t.Errorf("preset rename in merge = %v, want %v", got, want[m.tabs.presets])
		}
		if got := !cfg.SmartThings.ExposeSession && cfg.Media.NowPlaying; got != want[m.tabs.share] {
			t.Errorf("sharing edits in merge = %v, want %v", got, want[m.tabs.share])
		}
		if got := cfg.Media.Enabled; got != want[m.tabs.settings] {
			t.Errorf("media.enabled in merge = %v, want %v", got, want[m.tabs.settings])
		}
	}

	// An emptied token entry also means "keep".
	m.notify.Token = ""
	cfg, _ := m.f.merge([]*formTab{m.tabs.notify})
	if cfg.Telegram.BotToken != "****6789" {
		t.Errorf("emptied token entry sent %q, want the masked value", cfg.Telegram.BotToken)
	}
	// The baseline itself is never written into.
	if m.f.base.Port != 5001 || m.f.base.Telegram.ChatID != "42" || len(m.f.base.SmartThings.AllowedHubs) != 1 || m.f.base.Presets[0].Name != "Steam" {
		t.Errorf("merge changed the baseline: %+v", *m.f.base)
	}
}

// A check failing in a saving tab stops the merge with its message.
func TestMergeStopsOnAProblem(t *testing.T) {
	m := newModelTabs()
	m.f.adopt(storedConfig(), nil)
	m.settings.Port = "abc"
	if _, err := m.f.merge([]*formTab{m.tabs.settings}); err == nil {
		t.Error("a bad port merged")
	}
	m.settings.Port = "5001"
	m.settings.Remote, m.settings.Secret = true, ""
	if _, err := m.f.merge([]*formTab{m.tabs.settings}); err == nil {
		t.Error("browser access without a secret merged")
	}
}

// After a save every tab is compared against the new baseline: the saved
// one is clean (refilled from what the service stored), another tab's
// unsaved edits survive and stay dirty.
func TestAdoptAfterSaveKeepsOtherTabsEdits(t *testing.T) {
	m := newModelTabs()
	m.f.adopt(storedConfig(), nil)
	if d := dirtyIndices(m.f); d != nil {
		t.Fatalf("dirty right after the first load: %v", d)
	}

	m.settings.Port = "5005"
	m.notify.ChatID = "99"
	m.notify.Token = "123456:NEWTOKEN"
	if d := dirtyIndices(m.f); !slices.Equal(d, []int{tabSettings, tabTelegram}) {
		t.Fatalf("dirty = %v, want settings and notify", d)
	}

	// Save the settings tab only.
	sent, err := m.f.merge([]*formTab{m.tabs.settings})
	if err != nil {
		t.Fatal(err)
	}
	fresh := sent // the service stores it; the token comes back masked
	m.f.adopt(fresh, &sent)

	if m.f.base.Port != 5005 {
		t.Errorf("baseline port = %d, want the saved 5005", m.f.base.Port)
	}
	if d := dirtyIndices(m.f); !slices.Equal(d, []int{tabTelegram}) {
		t.Errorf("dirty after saving settings = %v, want only notify", d)
	}
	if m.notify.ChatID != "99" || m.notify.Token != "123456:NEWTOKEN" {
		t.Errorf("notify edits lost: chat %q token %q", m.notify.ChatID, m.notify.Token)
	}

	// Now save notify; the service masks the new token.
	sent, _ = m.f.merge([]*formTab{m.tabs.notify})
	if sent.Secret != "s3cret" || sent.Port != 5005 {
		t.Errorf("second save lost the first: secret %q port %d", sent.Secret, sent.Port)
	}
	fresh = sent
	fresh.Telegram.BotToken = maskToken("123456:NEWTOKEN")
	m.f.adopt(fresh, &sent)
	if d := dirtyIndices(m.f); d != nil {
		t.Errorf("dirty after saving everything = %v", d)
	}
	if m.notify.Token != "****OKEN" {
		t.Errorf("token entry = %q, want the new mask", m.notify.Token)
	}
}

// A refresh (reconnect, or the language switch's reload) picks up what
// changed in the service for clean tabs only.
func TestAdoptOnRefreshKeepsUnsavedEdits(t *testing.T) {
	m := newModelTabs()
	m.f.adopt(storedConfig(), nil)
	m.share.ExposeSession = false // unsaved

	remote := storedConfig() // changed elsewhere (WebUI, Telegram)
	remote.Presets = append(remote.Presets, Preset{Slot: 2, Name: "Docs", Type: "url", Path: "https://example.com"})
	remote.SmartThings.AllowedHubs = []string{"10.0.0.1"}
	remote.Media.NowPlaying = true
	m.f.adopt(remote, m.f.base)

	if len(m.presets.Rows) != 2 {
		t.Errorf("clean presets tab did not follow the service: %d rows", len(m.presets.Rows))
	}
	if !slices.Equal(m.st.Hubs, []string{"10.0.0.1"}) {
		t.Errorf("clean SmartThings tab did not follow the service: %v", m.st.Hubs)
	}
	if m.share.ExposeSession {
		t.Error("unsaved sharing edit was overwritten by the refresh")
	}
	if m.share.NowPlaying {
		t.Error("now playing changed: a dirty tab keeps all of its widgets")
	}
	if d := dirtyIndices(m.f); !slices.Equal(d, []int{tabShare}) {
		t.Errorf("dirty after refresh = %v, want only the sharing tab", d)
	}
}

// A rebuild (language switch) carries the edits over as drafts.
func TestDraftsSurviveARebuild(t *testing.T) {
	m := newModelTabs()
	m.f.adopt(storedConfig(), nil)
	m.settings.Secret = "n3w"
	m.notify.Token = "123456:NEWTOKEN"
	m.presets.Rows[0].Args = `"unbalanced` // cannot be expressed: falls back
	drafts := m.f.drafts()

	// The rebuild: fresh, empty widgets under the same baseline.
	base := m.f.base
	m2 := newModelTabs()
	m2.f.base = base
	m2.f.restore(drafts)

	if m2.settings.Secret != "n3w" || m2.notify.Token != "123456:NEWTOKEN" {
		t.Errorf("drafts lost: secret %q token %q", m2.settings.Secret, m2.notify.Token)
	}
	if m2.notify.ChatID != "42" || m2.settings.Port != "5001" {
		t.Errorf("untouched fields changed: chat %q port %q", m2.notify.ChatID, m2.settings.Port)
	}
	if d := dirtyIndices(m2.f); !slices.Equal(d, []int{tabSettings, tabTelegram}) {
		t.Errorf("dirty after the rebuild = %v, want settings and notify", d)
	}
}

// When the re-read after a save fails, the sent config becomes the
// baseline with the token masked the way the service would.
func TestSavedFallback(t *testing.T) {
	sent := storedConfig()
	if got := savedFallback(sent, "****6789").Telegram; got.BotToken != "****6789" || !got.BotTokenSet {
		t.Errorf("kept token: %+v", got)
	}
	sent.Telegram.BotToken = "123456:NEWTOKEN"
	if got := savedFallback(sent, "****6789").Telegram.BotToken; got != "****OKEN" {
		t.Errorf("new token masked as %q", got)
	}
	sent.Telegram.BotToken = "-"
	if got := savedFallback(sent, "****6789").Telegram; got.BotToken != "" || got.BotTokenSet {
		t.Errorf("cleared token: %+v", got)
	}
}

func TestSettingsStateRoundTrip(t *testing.T) {
	cfg := storedConfig()
	cfg.Media = MediaConfig{Enabled: true, NowPlaying: true}
	cfg.NotifyPC.Enabled = true
	s := settingsStateFromConfig(cfg)
	if s.dirty(cfg) {
		t.Fatalf("fresh state is dirty: %+v", s)
	}
	out := cfg
	if err := s.applyTo(&out, LangEn); err != nil {
		t.Fatal(err)
	}
	if out.Port != cfg.Port || out.Secret != cfg.Secret || out.GraceSeconds != 300 || !out.ShutdownGrace || !out.Media.NowPlaying || !out.NotifyPC.Enabled {
		t.Errorf("round trip changed the config: %+v", out)
	}

	// Grace off keeps the period for later; an older service's "on, no
	// period" reads as the fallback and is not a change.
	s.GraceOn = false
	_ = s.applyTo(&out, LangEn)
	if out.ShutdownGrace || out.GraceSeconds != 300 {
		t.Errorf("grace off: %v / %d", out.ShutdownGrace, out.GraceSeconds)
	}
	old := Config{ShutdownGrace: true}
	if s := settingsStateFromConfig(old); s.GraceSec != fallbackGraceSeconds || s.dirty(withGraceFallback(old)) {
		t.Errorf("grace without a period: %+v", s)
	}
}
