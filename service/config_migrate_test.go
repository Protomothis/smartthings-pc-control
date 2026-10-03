package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

// A config.json from before watch slots is migrated and written back at the
// first start, so the next start has nothing to migrate (and logs nothing).
func TestLoadConfigAtStartPersistsTheMigrationOnce(t *testing.T) {
	path := filepath.Join(configDir(), config.FileName)
	orig, origErr := os.ReadFile(path)
	t.Cleanup(func() {
		if origErr != nil {
			os.Remove(path)
			return
		}
		os.WriteFile(path, orig, 0o644)
	})
	old := `{"port": 5001, "activity": {"enabled": true, "watch": [{"process": "steam.exe", "label": "Steam"}, {"process": "obs64.exe", "label": "OBS"}]}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfigAtStart()
	if w := cfg.Activity.Watch; len(w) != 2 || w[0].Slot != 1 || w[0].Process != "steam.exe" || w[1].Slot != 2 {
		t.Fatalf("watch = %+v, want steam.exe in slot 1 and obs64.exe in slot 2", w)
	}
	if _, migrated := config.LoadMigrated(configDir()); migrated {
		t.Error("config.json still needs the migration after the first start")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The second start reads the migrated file and writes nothing.
	cfg = loadConfigAtStart()
	if w := cfg.Activity.Watch; len(w) != 2 || w[1].Slot != 2 {
		t.Errorf("watch at the second start = %+v", w)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(saved) {
		t.Error("the second start rewrote config.json")
	}
}
