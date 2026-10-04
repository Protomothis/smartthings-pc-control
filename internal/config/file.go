package config

// The files (#131). config.json — the WebUI/API secret and the Telegram
// bot token among others — carries a DACL of its own, SYSTEM and
// Administrators only, written that way on every save
// (secureacl.WritePrivateFile); state.json shares the rule. tray.json,
// next to them and readable by Users, holds the few non-secret settings
// the tray needs before or without a session (WriteTrayFile).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

// FileName is the settings file in the config folder (next to the exe).
const FileName = "config.json"

// TrayFileName is the user-readable subset of config.json for the tray
// app (gui/toast.go readLocalConfig) and the user-action runs
// (useraction/crashdump.go).
const TrayFileName = "tray.json"

// Load reads dir\config.json over Default, so every missing key keeps its
// default, and repairs what a hand edit may have broken: an invalid port,
// invalid presets or watch entries (dropped with a log line each), and a
// bot token this machine cannot decrypt. A missing or unreadable file, or
// dir "", is the default configuration.
func Load(dir string) Config {
	cfg, _ := LoadMigrated(dir)
	return cfg
}

// LoadMigrated is Load, also reporting whether config.json is in an older
// form that Load had to bring up to date in memory — a watch list without
// slots (#123), the retired smartthings.discovery key (#95) — so the
// service can save the result once instead of redoing (and logging) the
// migration at every start. A file that does not parse is never reported:
// saving over it would throw the user's settings away.
func LoadMigrated(dir string) (Config, bool) {
	cfg := Default()
	if dir == "" {
		return cfg.WithDefaults(), false
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		// No config file, use defaults
		return cfg.WithDefaults(), false
	}

	// Decoding over Default keeps the default for every missing key
	// (shutdown_grace, telegram.quiet_hours.security_bypass, ...).
	parsed := true
	if err := json.Unmarshal(data, &cfg); err != nil {
		logx.Printf("WARNING: config.json 파싱 실패 (기본값 사용): %v", err)
		fmt.Fprintf(os.Stderr, "WARNING: config.json parse error (using defaults): %v\n", err)
		cfg = Default()
		parsed = false
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		logx.Printf("WARNING: invalid port %d, using default 5001", cfg.Port)
		cfg.Port = DefaultPort
	}
	// A config.json written before #95 still carries smartthings.discovery.
	// The decoder ignores it (the field is gone) and the next save drops
	// it; say so once so a user who turned it off is not left wondering.
	noteLegacyDiscoveryKey(data)
	// A hand-edited preset that breaks the rules is ignored, not fatal: the
	// other presets and every other setting still load (#109).
	cfg.Presets = DropInvalidPresets(cfg.Presets)
	// A DPAPI-protected token that this machine cannot decrypt (config.json
	// copied from another PC) is unusable: blank it so the GUI shows "not
	// set" and the user re-enters it. The load itself still succeeds.
	if secret.IsProtected(cfg.Telegram.BotToken) {
		if _, err := secret.Unprotect(cfg.Telegram.BotToken); err != nil {
			logx.Printf("WARNING: telegram.bot_token cannot be decrypted on this machine (config copied from another PC?); token cleared, enter it again: %v", err)
			cfg.Telegram.BotToken = ""
		}
	}
	// A hand-edited watch list keeps its valid entries; the rest are
	// dropped with a log line each rather than failing the whole load.
	activity, reslotted := sanitizeActivity(cfg.Activity)
	cfg.Activity = activity
	migrated := parsed && (reslotted || legacyDiscoveryKey(data))
	return cfg.WithDefaults(), migrated
}

// legacyDiscoveryKey reports whether raw config.json data still carries
// smartthings.discovery, the toggle #95 removed. Parsing is deliberately
// separate from the Config decode: the key no longer has a field, so the
// only way to notice it is to look at the document.
func legacyDiscoveryKey(data []byte) bool {
	var doc struct {
		SmartThings struct {
			Discovery *bool `json:"discovery"`
		} `json:"smartthings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return false
	}
	return doc.SmartThings.Discovery != nil
}

// legacyDiscoveryOnce keeps the migration notice to one line per service
// run: Load also runs from the installer and the CLI.
var legacyDiscoveryOnce sync.Once

// noteLegacyDiscoveryKey logs the migration notice when data still has the
// retired key. The value is ignored either way — the responder is always on.
func noteLegacyDiscoveryKey(data []byte) {
	if !legacyDiscoveryKey(data) {
		return
	}
	legacyDiscoveryOnce.Do(func() {
		logx.Printf("SSDP 검색은 항상 켜져 있습니다 (discovery 설정은 더 이상 쓰지 않습니다)")
	})
}

// Save writes cfg to dir\config.json and returns what was stored: the
// full catalogue and every default (so config.json documents every key),
// with the bot token DPAPI-protected (#65). The file is private (#131).
func Save(dir string, cfg Config) (Config, error) {
	// Always persist the full catalogue and defaults so config.json
	// documents every key.
	cfg = cfg.WithDefaults()
	// Issue #65: never write the bot token in plaintext. A protection
	// failure is logged and the save proceeds with plaintext rather than
	// losing the user's other changes; the next save retries.
	if tok := cfg.Telegram.BotToken; tok != "" && !secret.IsProtected(tok) {
		if enc, err := secret.Protect(tok); err != nil {
			logx.Printf("WARNING: telegram.bot_token could not be DPAPI-protected, saving as plaintext: %v", err)
		} else {
			cfg.Telegram.BotToken = enc
		}
	}
	if dir == "" {
		return cfg, errors.New("config folder unknown")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return cfg, err
	}
	// SYSTEM and Administrators only (#131): it holds the secret.
	if err := WritePrivateFile(filepath.Join(dir, FileName), data); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// PrivateFilesOn reports whether this process restricts the private files:
// the service (LocalSystem) and the elevated installer do. A console run
// or a test as a plain user would lock itself out of its own config, so
// it writes them as before. A var for the tests.
var PrivateFilesOn = sync.OnceValue(func() bool {
	tok := windows.GetCurrentProcessToken()
	if tok.IsElevated() {
		return true
	}
	u, err := tok.GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
})

// WritePrivateFile writes config.json or state.json. A file that was saved
// but could not be restricted (a FAT drive, say) is logged, not failed:
// losing the user's settings would be worse.
func WritePrivateFile(path string, data []byte) error {
	if !PrivateFilesOn() {
		return os.WriteFile(path, data, 0o644)
	}
	err := secureacl.WritePrivateFile(path, data)
	if errors.Is(err, secureacl.ErrNotPrivate) {
		logx.Printf("WARNING: %s saved, but other users may still read it: %v", filepath.Base(path), err)
		return nil
	}
	return err
}

// trayConfig is tray.json: the non-secret settings the tray reads without a
// session. The JSON shape is config.json's, so the tray decodes both alike.
type trayConfig struct {
	Port        int `json:"port"`
	SmartThings struct {
		ExposeSession bool `json:"expose_session"`
	} `json:"smartthings"`
	Media struct {
		Enabled    bool `json:"enabled"`
		NowPlaying bool `json:"now_playing"`
	} `json:"media"`
	// Debug lets the app and the user-action runs record crashes from
	// their first instant, before any session (#133).
	Debug bool `json:"debug"`
}

// trayConfigFrom picks the tray's settings out of cfg.
func trayConfigFrom(cfg Config) trayConfig {
	var t trayConfig
	t.Port = cfg.Port
	t.SmartThings.ExposeSession = cfg.SmartThings.ExposeSession
	t.Media.Enabled = cfg.Media.Enabled
	t.Media.NowPlaying = cfg.Media.NowPlaying
	t.Debug = cfg.Debug
	return t
}

// WriteTrayFile rewrites tray.json in dir for cfg. Unlike config.json it
// inherits the folder's "Users: read". Best effort: a failure is logged,
// and the tray then falls back to the default port and its switches' off
// state until the next save or start.
func WriteTrayFile(dir string, cfg Config) {
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(trayConfigFrom(cfg), "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, TrayFileName), data, 0o644); err != nil {
		logx.Printf("WARNING: %s not written: %v", TrayFileName, err)
	}
}
