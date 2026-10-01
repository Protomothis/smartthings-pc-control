package service

// The live configuration. The config.json types, defaults, rules and the
// file itself are internal/config; this file holds the copy every request
// reads, and does what a save sets off.

import (
	"sync"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

// The config.json types under their old names, so the service code that
// reads them stays short.
type (
	Config            = config.Config
	TelegramConfig    = config.TelegramConfig
	SmartThingsConfig = config.SmartThingsConfig
	AwakeConfig       = config.AwakeConfig
	ActivityConfig    = config.ActivityConfig
	ActivityWatch     = config.ActivityWatch
	MediaConfig       = config.MediaConfig
	NotifyPCConfig    = config.NotifyPCConfig
	Preset            = config.Preset
)

// configDir is the folder holding config.json, tray.json and state.json:
// the exe's own. A var so the tests keep those files in a folder of their
// own instead of next to the test binary.
var configDir = installDir

// Global config with RWMutex for hot-reload support
var (
	currentConfig Config
	configMu      sync.RWMutex
)

// getConfig returns the current configuration (thread-safe).
func getConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return currentConfig
}

// setConfig updates the current configuration in memory (thread-safe).
func setConfig(cfg Config) {
	configMu.Lock()
	defer configMu.Unlock()
	currentConfig = cfg
}

// loadConfig reads config.json (config.Load); it does not make the result
// live.
func loadConfig() Config {
	return config.Load(configDir())
}

// saveConfig stores cfg (config.Save), makes the stored copy live and
// lets the parts that follow the settings catch up.
func saveConfig(cfg Config) error {
	dir := configDir()
	stored, err := config.Save(dir, cfg)
	if err != nil {
		return err
	}
	// Update in-memory config
	setConfig(stored)
	// The tray's non-secret copy follows every save (#131).
	config.WriteTrayFile(dir, stored)
	// Telegram control follows the saved settings without a restart
	// (no-op unless the service has started it, see telegram_control.go).
	reconcileTelegramControl()
	// The activity scanner looks again right away instead of on its next
	// tick (a no-op while it is not running).
	kickActivityScan()
	return nil
}
