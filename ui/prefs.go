package ui

// The app's own preferences (language, theme, autostart, update check).
// They stay in the file the Fyne app keeps them in, so switching to this UI
// carries every choice over and nothing has to be migrated:
// %APPDATA%\fyne\com.protomothis.smartthings-pc-control\preferences.json,
// one flat JSON object.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const appID = "com.protomothis.smartthings-pc-control"

// Preference keys. lang, autostart and check_updates are the Fyne app's.
const (
	prefLang         = "lang"
	prefTheme        = "theme" // "" (system), "light" or "dark"
	prefAutostart    = "autostart"
	prefCheckUpdates = "check_updates"
)

type prefs struct {
	path string
	mu   sync.Mutex
	vals map[string]any
}

// defaultPrefsPath is where the Fyne app keeps its preferences.
func defaultPrefsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Roaming", "fyne", appID, "preferences.json")
}

// loadPrefs reads path; a missing or broken file is an empty set.
func loadPrefs(path string) *prefs {
	p := &prefs{path: path, vals: map[string]any{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &p.vals)
	}
	return p
}

func (p *prefs) String(key, fallback string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.vals[key].(string); ok {
		return s
	}
	return fallback
}

func (p *prefs) Bool(key string, fallback bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.vals[key].(bool); ok {
		return b
	}
	return fallback
}

// Set stores v and writes the file; an unwritable file only loses the
// choice for the next start.
func (p *prefs) Set(key string, v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vals[key] = v
	if p.path == "" {
		return // tests
	}
	data, err := json.Marshal(p.vals)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p.path), 0o755)
	tmp := p.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, p.path)
	}
}
