package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig puts content into dir\config.json.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	stored, err := Save(dir, Config{Port: 7777, Secret: "roundtrip"})
	if err != nil {
		t.Fatal(err)
	}
	// What was stored is the full, defaulted document.
	if stored.Telegram.Detail != "full" || stored.Notify == nil || stored.Presets == nil {
		t.Errorf("stored = %+v", stored)
	}
	loaded := Load(dir)
	if loaded.Port != 7777 || loaded.Secret != "roundtrip" {
		t.Errorf("loaded = %+v", loaded)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw["port"].(float64) != 7777 {
		t.Errorf("saved JSON: %s (%v)", data, err)
	}
	if _, err := Save("", Config{}); err == nil {
		t.Error("a save without a folder must fail")
	}
}

func TestWriteTrayFileCarriesNoSecret(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.Port = 5101
	cfg.Secret = "tray-must-not-see-this"
	cfg.Telegram.BotToken = "123:telegram-token"
	cfg.SmartThings.ExposeSession = true
	WriteTrayFile(dir, cfg)
	data, err := os.ReadFile(filepath.Join(dir, TrayFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"tray-must-not-see-this", "telegram-token", "secret", "bot_token"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("tray.json carries %q:\n%s", leak, data)
		}
	}
	if !strings.Contains(string(data), `"port": 5101`) || !strings.Contains(string(data), `"expose_session": true`) {
		t.Errorf("tray.json = %s", data)
	}
}

// tray.json carries the debug switch (#133) for the app and the
// user-action runs.
func TestTrayFileCarriesDebug(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	WriteTrayFile(dir, cfg)
	data, _ := os.ReadFile(filepath.Join(dir, TrayFileName))
	if !strings.Contains(string(data), `"debug": false`) {
		t.Errorf("debug off: tray.json = %s", data)
	}
	cfg.Debug = true
	WriteTrayFile(dir, cfg)
	data, _ = os.ReadFile(filepath.Join(dir, TrayFileName))
	if !strings.Contains(string(data), `"debug": true`) {
		t.Errorf("debug on: tray.json = %s", data)
	}
}

// An older config.json has no debug key: it loads as off, an explicit
// value is kept, and a save writes the key (#133).
func TestDebugKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"port": 5001, "secret": "x"}`)
	if cfg, migrated := LoadMigrated(dir); cfg.Debug || migrated {
		t.Errorf("older config: debug=%v migrated=%v", cfg.Debug, migrated)
	}
	writeConfig(t, dir, `{"port": 5001, "debug": true}`)
	cfg := Load(dir)
	if !cfg.Debug {
		t.Fatal("debug: true was lost on load")
	}
	if _, err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(data), `"debug": true`) || !Load(dir).Debug {
		t.Errorf("saved config.json = %s", data)
	}
	// ForUpdate + a POST body without the key keeps it (an older app).
	upd := cfg.ForUpdate()
	if err := json.Unmarshal([]byte(`{"port": 5001}`), &upd); err != nil || !Normalize(upd, cfg).Debug {
		t.Error("a POST without debug turned it off")
	}
}

// TestLegacyDiscoveryKeyIsIgnoredAndDropped is the #95 migration: a
// config.json still carrying smartthings.discovery loads without an error,
// the value changes nothing, and the next save writes the key away.
func TestLegacyDiscoveryKeyIsIgnoredAndDropped(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfig(t, dir, `{"port": 5001, "smartthings": {"discovery": false, "allowed_hubs": ["192.168.1.20"], "expose_session": true}}`)

	cfg := Load(dir)
	// Everything beside the retired key survives the load…
	if len(cfg.SmartThings.AllowedHubs) != 1 || cfg.SmartThings.AllowedHubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v", cfg.SmartThings.AllowedHubs)
	}
	if !cfg.SmartThings.ExposeSession {
		t.Error("expose_session was lost alongside the retired key")
	}
	// …and discovery: false cannot stop the responder any more, because
	// nothing reads it.
	if !legacyDiscoveryKey([]byte(`{"smartthings":{"discovery":false}}`)) {
		t.Error("legacyDiscoveryKey missed an explicit false")
	}
	for _, doc := range []string{
		`{"port":5001}`,
		`{"smartthings":{"allowed_hubs":[]}}`,
		`not json`,
	} {
		if legacyDiscoveryKey([]byte(doc)) {
			t.Errorf("legacyDiscoveryKey(%s) = true", doc)
		}
	}

	if _, err := Save(dir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if legacyDiscoveryKey(saved) {
		t.Errorf("the save kept smartthings.discovery: %s", saved)
	}
	if !strings.Contains(string(saved), `"allowed_hubs"`) {
		t.Errorf("the save lost allowed_hubs: %s", saved)
	}
}

// LoadMigrated reports a config.json that Load had to bring up to date —
// so the service saves it once — and stops reporting it after that save.
func TestLoadMigratedReportsAnOlderFormOnce(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		want      bool
	}{
		{"watch list without slots", `{"port": 5001, "activity": {"enabled": true, "watch": [{"process": "steam.exe", "label": "Steam"}, {"process": "obs64.exe", "label": "OBS"}]}}`, true},
		{"repeated slot", `{"port": 5001, "activity": {"watch": [{"slot": 1, "process": "steam.exe"}, {"slot": 1, "process": "obs64.exe"}]}}`, true},
		{"retired discovery key", `{"port": 5001, "smartthings": {"discovery": true}}`, true},
		{"current form", `{"port": 5001, "activity": {"watch": [{"slot": 2, "process": "steam.exe", "label": "Steam"}]}}`, false},
		{"unparseable", `{"port": 5001, "activity": {"watch": [{"process": "steam.exe"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.doc)
			cfg, migrated := LoadMigrated(dir)
			if migrated != tc.want {
				t.Fatalf("migrated = %v, want %v", migrated, tc.want)
			}
			if !migrated {
				return
			}
			if _, err := Save(dir, cfg); err != nil {
				t.Fatal(err)
			}
			again, migrated := LoadMigrated(dir)
			if migrated {
				t.Error("still reported as migrated after the save")
			}
			if len(again.Activity.Watch) != len(cfg.Activity.Watch) {
				t.Errorf("watch after the save = %+v, want %+v", again.Activity.Watch, cfg.Activity.Watch)
			}
		})
	}
	// Without a file there is nothing to migrate.
	if _, migrated := LoadMigrated(t.TempDir()); migrated {
		t.Error("a missing config.json reported as migrated")
	}
}
