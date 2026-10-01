package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"

	"golang.org/x/sys/windows"
)

// TestSaveConfigWritesTrayConfig: every save refreshes tray.json with the
// tray's settings and nothing secret (#131).
func TestSaveConfigWritesTrayConfig(t *testing.T) {
	initLogger()
	saved := getConfig()
	t.Cleanup(func() { setConfig(saved) })
	// saveConfig writes into the test run's config folder; later tests expect the
	// files as they were.
	for _, name := range []string{config.FileName, config.TrayFileName} {
		path := filepath.Join(configDir(), name)
		orig, origErr := os.ReadFile(path)
		t.Cleanup(func() {
			if origErr == nil {
				os.WriteFile(path, orig, 0o644)
			} else {
				os.Remove(path)
			}
		})
	}

	cfg := config.Default()
	cfg.Port = 5101
	cfg.Secret = "tray-must-not-see-this"
	cfg.SmartThings.ExposeSession = true
	cfg.Media.Enabled = false
	cfg.Media.NowPlaying = true
	cfg.Telegram.BotToken = "123:telegram-token"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configDir(), config.TrayFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"tray-must-not-see-this", "telegram-token", "secret", "bot_token"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("tray.json carries %q:\n%s", leak, data)
		}
	}
	// The shape the tray decodes (gui/toast.go localConfig).
	var got struct {
		Port        int `json:"port"`
		SmartThings struct {
			ExposeSession bool `json:"expose_session"`
		} `json:"smartthings"`
		Media struct {
			Enabled    *bool `json:"enabled"`
			NowPlaying bool  `json:"now_playing"`
		} `json:"media"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Port != 5101 || !got.SmartThings.ExposeSession || got.Media.Enabled == nil || *got.Media.Enabled || !got.Media.NowPlaying {
		t.Errorf("tray.json = %s", data)
	}
}

func TestPrivateFilesOffForPlainUser(t *testing.T) {
	saved := config.PrivateFilesOn
	t.Cleanup(func() { config.PrivateFilesOn = saved })
	config.PrivateFilesOn = func() bool { return false }

	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)
	if err := config.WritePrivateFile(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if msgs := lockPrivateFiles(dir); msgs != nil {
		t.Errorf("lockPrivateFiles while off: %v", msgs)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Errorf("content %q, %v", data, err)
	}
}

// TestLockPrivateFiles locks real files, which only an elevated run can
// still read and delete afterwards.
func TestLockPrivateFiles(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated test run")
	}
	initLogger()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs := lockPrivateFiles(dir) // state.json is missing: skipped
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], config.FileName+": access restricted") {
		t.Errorf("first lock: %v", msgs)
	}
	if msgs := lockPrivateFiles(dir); len(msgs) != 0 {
		t.Errorf("second lock: %v", msgs)
	}
	if err := config.WritePrivateFile(filepath.Join(dir, stateFileName), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if msgs := lockPrivateFiles(dir); len(msgs) != 0 {
		t.Errorf("a file written private was locked again: %v", msgs)
	}
}
