package gui

import (
	"testing"
)

// The save coordinator is tested on the pure tab models, wired up the way
// the widgets wire them: Fill loads the model from a config, Dirty and
// ApplyTo are the model's own. Indices only tell the drafts apart.

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
