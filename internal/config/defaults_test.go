package config

// Defaults, loading and Normalize, moved here from the service package
// with the code (#127).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

func TestAwakeConfigDefaults(t *testing.T) {
	if got := Default().WithDefaults().Awake; got.DefaultMinutes != 60 || got.KeepDisplay {
		t.Errorf("default awake = %+v, want {60 false}", got)
	}
	for _, tc := range []struct{ in, want int }{{0, 0}, {30, 30}, {1440, 1440}, {-5, 60}, {1441, 60}} {
		if got := (AwakeConfig{DefaultMinutes: tc.in}).WithDefaults().DefaultMinutes; got != tc.want {
			t.Errorf("withDefaults(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// A config.json without the key keeps the default; one with 0 keeps 0.
	cfg := Default()
	if err := json.Unmarshal([]byte(`{"port":5001}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.WithDefaults().Awake.DefaultMinutes != 60 {
		t.Errorf("missing awake key: %+v", cfg.Awake)
	}
	cfg = Default()
	json.Unmarshal([]byte(`{"awake":{"default_minutes":0,"keep_display":true}}`), &cfg)
	if a := cfg.WithDefaults().Awake; a.DefaultMinutes != 0 || !a.KeepDisplay {
		t.Errorf("explicit awake = %+v", a)
	}
}

func TestMediaConfigDefaultsOn(t *testing.T) {
	if !Default().Media.Enabled {
		t.Error("media.enabled defaults to off")
	}
	// An older config.json without the key keeps the default; an explicit
	// false is kept.
	for body, want := range map[string]bool{`{"port":5001}`: true, `{"media":{"enabled":false}}`: false, `{"media":{}}`: true} {
		cfg := Default()
		if err := json.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.WithDefaults().Media.Enabled != want {
			t.Errorf("%s: media.enabled = %v, want %v", body, cfg.Media.Enabled, want)
		}
	}
	old := Default().WithDefaults()
	changed := old
	changed.Media.Enabled = false
	if keys := ChangedKeys(old, changed); !reflect.DeepEqual(keys, []string{"media.enabled"}) {
		t.Errorf("changed keys = %v", keys)
	}
}

func TestMediaConfigNowPlayingDefaultOff(t *testing.T) {
	if Default().Media.NowPlaying {
		t.Error("media.now_playing defaults to on")
	}
	old := Default().WithDefaults()
	changed := old
	changed.Media.NowPlaying = true
	if keys := ChangedKeys(old, changed); !reflect.DeepEqual(keys, []string{"media.now_playing"}) {
		t.Errorf("changed keys = %v", keys)
	}
}

func TestNotifyPCDefaults(t *testing.T) {
	cfg := Default().WithDefaults()
	if !cfg.NotifyPC.Enabled {
		t.Errorf("defaults = %+v, want enabled", cfg.NotifyPC)
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"port": 9999, "secret": "test123"}`)

	cfg := Load(dir)
	if cfg.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Port)
	}
	if cfg.Secret != "test123" {
		t.Errorf("expected secret 'test123', got %q", cfg.Secret)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "{invalid json!!!")

	cfg := Load(dir)
	// Should fall back to defaults
	if cfg.Port != 5001 {
		t.Errorf("expected default port 5001 on invalid JSON, got %d", cfg.Port)
	}
}

func TestLoadConfigZeroPort(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"port": 0, "secret": "abc"}`)

	cfg := Load(dir)
	if cfg.Port != 5001 {
		t.Errorf("expected port 5001 when configured as 0, got %d", cfg.Port)
	}
	if cfg.Secret != "abc" {
		t.Errorf("expected secret 'abc', got %q", cfg.Secret)
	}
}

func TestGraceDefaultTrueFromConfig(t *testing.T) {
	// Missing key in an old config.json must keep the default (true).
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"port": 5001, "secret": ""}`), 0644)
	cfg := Default()
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.ShutdownGrace {
		t.Error("shutdown_grace should default to true when missing from config.json")
	}
	if cfg.WebUIRemote {
		t.Error("webui_remote should default to false when missing from config.json")
	}
	if cfg.GraceSeconds != DefaultGraceSeconds {
		t.Errorf("grace_seconds = %d, want default %d when missing from config.json", cfg.GraceSeconds, DefaultGraceSeconds)
	}
}

func TestGraceDurationFallsBackWhenInvalid(t *testing.T) {
	def := time.Duration(DefaultGraceSeconds) * time.Second
	cases := map[int]time.Duration{
		0:                   def, // key missing from config.json
		MinGraceSeconds:     time.Duration(MinGraceSeconds) * time.Second,
		MaxGraceSeconds:     time.Duration(MaxGraceSeconds) * time.Second,
		MaxGraceSeconds + 1: def,
		-5:                  def,
	}
	for sec, want := range cases {
		if got := (Config{GraceSeconds: sec}).GraceDuration(); got != want {
			t.Errorf("graceDuration(%d) = %s, want %s", sec, got, want)
		}
	}
}

func TestNormalizeConfigKeepsGraceWhenOmitted(t *testing.T) {
	current := Config{GraceSeconds: 60}
	// A client that predates grace_seconds sends 0 → keep the live value.
	if got := Normalize(Config{ShutdownGrace: true}, current).GraceSeconds; got != 60 {
		t.Errorf("omitted grace_seconds → %d, want 60 (current)", got)
	}
	// Explicit values pass through untouched (validation happens later).
	if got := Normalize(Config{GraceSeconds: 10}, current).GraceSeconds; got != 10 {
		t.Errorf("explicit grace_seconds → %d, want 10", got)
	}
	// Nothing to inherit → default.
	if got := Normalize(Config{}, Config{}).GraceSeconds; got != DefaultGraceSeconds {
		t.Errorf("no current value → %d, want default %d", got, DefaultGraceSeconds)
	}
}

func TestLoadConfigFillsTelegramAndNotifyDefaults(t *testing.T) {
	// A v0.3.x config.json knows nothing about telegram/notify.
	dir := t.TempDir()
	configPath := writeConfig(t, dir, `{"port": 5001, "secret": "abc", "shutdown_grace": true, "grace_seconds": 300}`)

	cfg := Load(dir)
	tg := cfg.Telegram
	if tg.Enabled || tg.BotToken != "" || tg.ChatID != "" || tg.ControlEnabled {
		t.Errorf("telegram should be off by default, got %+v", tg)
	}
	if tg.Detail != "full" || tg.Lang != "ko" || tg.PCName != "" {
		t.Errorf("detail/lang/pc_name defaults wrong: %+v", tg)
	}
	if tg.AllowedChatIDs == nil || len(tg.AllowedChatIDs) != 0 {
		t.Errorf("allowed_chat_ids should be an empty list, got %#v", tg.AllowedChatIDs)
	}
	wantQH := notify.QuietHours{Enabled: false, Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true}
	if tg.QuietHours != wantQH {
		t.Errorf("quiet_hours = %+v, want %+v", tg.QuietHours, wantQH)
	}
	if !reflect.DeepEqual(cfg.Notify, notify.DefaultConfig()) {
		t.Errorf("notify should be the full default catalogue, got %v", cfg.Notify)
	}
	if !cfg.Notify.Enabled("remote", "received") || cfg.Notify.Enabled("schedule", "created") {
		t.Error("notify defaults not applied")
	}

	// Change a few values, save, reload: everything must survive JSON,
	// including explicit falses that differ from the defaults.
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "123:abc"
	cfg.Telegram.ChatID = "42"
	cfg.Telegram.AllowedChatIDs = []string{"42", "43"}
	cfg.Telegram.Lang = "en"
	cfg.Telegram.QuietHours.Enabled = true
	cfg.Telegram.QuietHours.SecurityBypass = false
	cfg.Notify["remote"]["received"] = false
	cfg.Notify["schedule"]["created"] = true
	if _, err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded := Load(dir)
	// Save stores the token DPAPI-protected (#65); compare the
	// decrypted value and the rest of the struct separately.
	if plain, err := secret.Unprotect(loaded.Telegram.BotToken); err != nil || plain != "123:abc" {
		t.Errorf("bot_token round trip: got %q (%v), want 123:abc", plain, err)
	}
	loaded.Telegram.BotToken = cfg.Telegram.BotToken
	if !reflect.DeepEqual(loaded.Telegram, cfg.Telegram) {
		t.Errorf("telegram round trip:\n got %+v\nwant %+v", loaded.Telegram, cfg.Telegram)
	}
	if !reflect.DeepEqual(loaded.Notify, cfg.Notify) {
		t.Errorf("notify round trip:\n got %v\nwant %v", loaded.Notify, cfg.Notify)
	}
	if loaded.Secret != "abc" || loaded.GraceSeconds != 300 {
		t.Errorf("existing keys damaged: %+v", loaded)
	}

	// The file uses the documented key names.
	data, _ := os.ReadFile(configPath)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"telegram", "notify"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("saved config.json lacks %q", key)
		}
	}
	var rawTG map[string]json.RawMessage
	json.Unmarshal(raw["telegram"], &rawTG)
	for _, key := range []string{"enabled", "bot_token", "chat_id", "control_enabled", "allowed_chat_ids", "detail", "lang", "pc_name", "quiet_hours"} {
		if _, ok := rawTG[key]; !ok {
			t.Errorf("saved telegram object lacks %q", key)
		}
	}
}

func TestLoadConfigMissingFileHasDefaults(t *testing.T) {
	cfg := Load(t.TempDir())
	if cfg.Telegram.Detail != "full" || cfg.Notify == nil {
		t.Errorf("defaults not filled without a config file: %+v", cfg)
	}
}

func TestNormalizeConfigKeepsBotTokenAndOmittedTelegram(t *testing.T) {
	current := Config{GraceSeconds: 60, Telegram: TelegramConfig{
		Enabled: true, BotToken: "keep-me", ChatID: "1", AllowedChatIDs: []string{"1", "2"}, Detail: "simple", Lang: "en",
		QuietHours: notify.QuietHours{Enabled: true, Start: "23:00", End: "06:00", SecurityBypass: true, Digest: true},
	}, Notify: notify.Config{"remote": {"received": false}}.WithDefaults()}

	// Old GUI: the body has no telegram/notify keys at all.
	body := `{"port": 5001, "secret": "s", "webui_remote": false, "shutdown_grace": true, "grace_seconds": 60}`
	newCfg := current.ForUpdate()
	if err := json.Unmarshal([]byte(body), &newCfg); err != nil {
		t.Fatal(err)
	}
	got := Normalize(newCfg, current)
	if !reflect.DeepEqual(got.Telegram, current.Telegram) {
		t.Errorf("omitted telegram changed:\n got %+v\nwant %+v", got.Telegram, current.Telegram)
	}
	if !reflect.DeepEqual(got.Notify, current.Notify) {
		t.Errorf("omitted notify changed: %v", got.Notify)
	}

	// New GUI sends telegram with an empty token (masked value comes with
	// #63): the token is kept, the rest is replaced.
	body = `{"port": 5001, "telegram": {"enabled": false, "bot_token": "", "chat_id": "9", "allowed_chat_ids": [], "detail": "full", "lang": "ko",
		"quiet_hours": {"enabled": false, "start": "22:00", "end": "07:00", "security_bypass": false, "digest": true}},
		"notify": {"remote": {"received": true}}}`
	newCfg = current.ForUpdate()
	if err := json.Unmarshal([]byte(body), &newCfg); err != nil {
		t.Fatal(err)
	}
	got = Normalize(newCfg, current)
	if got.Telegram.BotToken != "keep-me" {
		t.Errorf("empty bot_token replaced the stored one: %q", got.Telegram.BotToken)
	}
	if got.Telegram.Enabled || got.Telegram.ChatID != "9" || got.Telegram.Detail != "full" || got.Telegram.Lang != "ko" {
		t.Errorf("explicit telegram values not applied: %+v", got.Telegram)
	}
	if len(got.Telegram.AllowedChatIDs) != 0 {
		t.Errorf("explicit empty allowed_chat_ids not applied: %v", got.Telegram.AllowedChatIDs)
	}
	if got.Telegram.QuietHours.SecurityBypass || got.Telegram.QuietHours.Enabled {
		t.Errorf("explicit quiet_hours falses not applied: %+v", got.Telegram.QuietHours)
	}
	if !got.Notify.Enabled("remote", "received") || got.Notify["schedule"]["created"] {
		t.Errorf("notify not applied/filled: %v", got.Notify)
	}
	// A new token replaces the old one.
	newCfg = current.ForUpdate()
	json.Unmarshal([]byte(`{"telegram": {"bot_token": "new"}}`), &newCfg)
	if got := Normalize(newCfg, current); got.Telegram.BotToken != "new" || got.Telegram.ChatID != "1" {
		t.Errorf("new token / untouched chat_id: %+v", got.Telegram)
	}
	// Nothing above may have written into current's map or slice.
	if current.Notify["remote"]["received"] || len(current.Telegram.AllowedChatIDs) != 2 {
		t.Error("normalizeConfig aliased the live config")
	}
}

func TestConfigChangedKeys(t *testing.T) {
	old := Default().WithDefaults()
	same := old
	if keys := ChangedKeys(old, same); len(keys) != 0 {
		t.Errorf("identical configs reported %v", keys)
	}
	changed := old
	changed.Secret = "s3cr3t-value"
	changed.ShutdownGrace = !old.ShutdownGrace // not security-relevant
	changed.Telegram.BotToken = "9999:ZZZZ"
	changed.Telegram.QuietHours.Enabled = true
	changed.Telegram.AllowedChatIDs = []string{"777"}
	got := strings.Join(ChangedKeys(old, changed), ",")
	want := "secret,telegram.bot_token,telegram.allowed_chat_ids,telegram.quiet_hours"
	if got != want {
		t.Errorf("changed keys = %s, want %s", got, want)
	}
	for _, v := range []string{"s3cr3t", "ZZZZ", "777"} {
		if strings.Contains(got, v) {
			t.Errorf("changed keys leaked the value %q", v)
		}
	}
}

func TestSmartThingsConfigDefaults(t *testing.T) {
	// A config.json that predates v1.1.0 keeps the smartthings settings off.
	dir := t.TempDir()
	writeConfig(t, dir, `{"port": 5001, "secret": "abc"}`)
	cfg := Load(dir)
	if cfg.SmartThings.AllowedHubs == nil || len(cfg.SmartThings.AllowedHubs) != 0 {
		t.Errorf("allowed_hubs = %v, want []", cfg.SmartThings.AllowedHubs)
	}
	if cfg.SmartThings.ExposeSession || cfg.SmartThings.ExposeSessionUser {
		t.Error("session exposure must default to off")
	}

	writeConfig(t, dir, `{"port": 5001, "smartthings": {"allowed_hubs": ["192.168.1.20"]}}`)
	cfg = Load(dir)
	if len(cfg.SmartThings.AllowedHubs) != 1 || cfg.SmartThings.AllowedHubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v", cfg.SmartThings.AllowedHubs)
	}
}

func TestNormalizeConfigKeepsSmartThingsHubsWhenOmitted(t *testing.T) {
	current := Default().WithDefaults()
	current.SmartThings.AllowedHubs = []string{"192.168.1.20"}
	current.SmartThings.ExposeSession = true

	posted := current.ForUpdate()
	if err := json.Unmarshal([]byte(`{"port":5001}`), &posted); err != nil {
		t.Fatal(err)
	}
	got := Normalize(posted, current)
	if len(got.SmartThings.AllowedHubs) != 1 || got.SmartThings.AllowedHubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v, want the live list kept", got.SmartThings.AllowedHubs)
	}
	if !got.SmartThings.ExposeSession {
		t.Error("expose_session was reset by a body that omitted it")
	}
}

// TestConfigChangedKeysCoversAllowedHubs guards the security event: the
// allow list is what decides who may drive this PC now that discovery has
// no switch (#95), so a change to it must be listed.
func TestConfigChangedKeysCoversAllowedHubs(t *testing.T) {
	old := Default().WithDefaults()
	updated := old
	updated.SmartThings.AllowedHubs = []string{"192.168.1.20"}
	keys := ChangedKeys(old, updated)
	if len(keys) != 1 || keys[0] != "smartthings.allowed_hubs" {
		t.Errorf("keys = %v", keys)
	}
	// The retired key can no longer produce an event of its own.
	if slices.Contains(ChangedKeys(old, old), "smartthings.discovery") {
		t.Error("smartthings.discovery is still a tracked key")
	}
}
