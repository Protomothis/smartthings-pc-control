package config

// Defaults, loading and Normalize, moved here from the service package
// with the code (#127).

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

var update = flag.Bool("update", false, "rewrite testdata/config.default.json")

// TestDefaultConfigGolden pins the configuration a fresh install runs with
// (testdata/config.default.json), and checks that every way of arriving
// without a value — no config folder, no file, a broken file, an older
// config.json that predates a key, an empty object for a block — lands on
// exactly it. A new setting changes the golden; regenerate with
//
//	go test ./internal/config -run TestDefaultConfigGolden -update
func TestDefaultConfigGolden(t *testing.T) {
	golden := filepath.Join("testdata", "config.default.json")
	encode := func(c Config) string {
		t.Helper()
		raw, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	got := encode(Load(t.TempDir()))
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("the defaults changed (a new setting? run with -update):\n%s", got)
	}

	if s := encode(Default().WithDefaults()); s != got {
		t.Errorf("Default().WithDefaults() differs from Load of a missing file:\n%s", s)
	}
	if s := encode(Load("")); s != got {
		t.Errorf("no config folder:\n%s", s)
	}
	for _, body := range []string{
		"{invalid json!!!",
		`{}`,
		`{"port": 0}`,
		// Every block a release added, absent or empty in an older file.
		`{"port": 5001, "secret": ""}`,
		`{"telegram": {}, "notify": {}, "smartthings": {}, "awake": {}, "activity": {}, "media": {}, "notify_pc": {}}`,
	} {
		dir := t.TempDir()
		writeConfig(t, dir, body)
		if s := encode(Load(dir)); s != got {
			t.Errorf("%s: loaded as\n%s", body, s)
		}
	}
}

// What a config.json says explicitly is kept, including the values that
// equal a type's zero value but not the default.
func TestLoadKeepsExplicitValues(t *testing.T) {
	for body, check := range map[string]func(Config) bool{
		`{"port": 9999, "secret": "test123"}`:                             func(c Config) bool { return c.Port == 9999 && c.Secret == "test123" },
		`{"port": 0, "secret": "abc"}`:                                    func(c Config) bool { return c.Port == DefaultPort && c.Secret == "abc" },
		`{"shutdown_grace": false, "grace_seconds": 30}`:                  func(c Config) bool { return !c.ShutdownGrace && c.GraceSeconds == 30 },
		`{"media": {"enabled": false}}`:                                   func(c Config) bool { return !c.Media.Enabled },
		`{"notify_pc": {"enabled": false}}`:                               func(c Config) bool { return !c.NotifyPC.Enabled },
		`{"awake": {"default_minutes": 0, "keep_display": true}}`:         func(c Config) bool { return c.Awake.DefaultMinutes == 0 && c.Awake.KeepDisplay },
		`{"smartthings": {"allowed_hubs": ["192.168.1.20"]}}`:             func(c Config) bool { return slices.Equal(c.SmartThings.AllowedHubs, []string{"192.168.1.20"}) },
		`{"smartthings": {"expose_session": true}, "webui_remote": true}`: func(c Config) bool { return c.SmartThings.ExposeSession && c.WebUIRemote },
	} {
		dir := t.TempDir()
		writeConfig(t, dir, body)
		if cfg := Load(dir); !check(cfg) {
			t.Errorf("%s: loaded as %+v", body, cfg)
		}
	}
}

// Out-of-range values fall back to the default instead of failing the load.
func TestOutOfRangeFallsBackToDefault(t *testing.T) {
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
	for _, tc := range []struct{ in, want int }{{0, 0}, {30, 30}, {1440, 1440}, {-5, 60}, {1441, 60}} {
		if got := (AwakeConfig{DefaultMinutes: tc.in}).WithDefaults().DefaultMinutes; got != tc.want {
			t.Errorf("awake.default_minutes %d → %d, want %d", tc.in, got, tc.want)
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
