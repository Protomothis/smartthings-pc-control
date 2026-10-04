package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/crashdump"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
)

// crashFiles lists the service's crash files in crashDir.
func crashFiles(t *testing.T) []string {
	t.Helper()
	out, err := filepath.Glob(filepath.Join(crashDir(), crashPrefix+"-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Debug mode (#133) follows the config: on at start, off and on again
// with a save (no restart), each change logged with the folder, and a
// clean stop leaves no file behind.
func TestDebugModeFollowsTheConfig(t *testing.T) {
	saved := getConfig()
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
	t.Cleanup(func() {
		stopDebugMode()
		setConfig(saved)
		os.RemoveAll(crashDir())
	})
	var buf bytes.Buffer
	t.Cleanup(logx.Capture(&buf))

	// Outside the running service (the installer) a save changes nothing.
	cfg := config.Default()
	cfg.Debug = true
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if crashdump.Active() != "" || len(crashFiles(t)) != 0 {
		t.Fatal("a save outside the service turned recording on")
	}

	// A crash record of an earlier run is announced once at start.
	if err := os.MkdirAll(crashDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(crashDir(), "service-20261001-120000-4.txt")
	os.WriteFile(old, []byte("SmartThings PC Control crash record (service)\r\n--- crash output follows ---\r\npanic: boom\r\n"), 0o644)

	startDebugMode(cfg)
	path := crashdump.Active()
	if path == "" || filepath.Dir(path) != crashDir() {
		t.Fatalf("active crash file = %q, want one in %s", path, crashDir())
	}
	log := buf.String()
	if !strings.Contains(log, "[debug] debug mode on") || !strings.Contains(log, crashDir()) ||
		!strings.Contains(log, "[debug] previous crash recorded: "+old) {
		t.Errorf("start logged:\n%s", log)
	}

	// The same setting again is quiet.
	buf.Reset()
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 || crashdump.Active() != path {
		t.Errorf("an unchanged save logged %q / moved to %q", buf.String(), crashdump.Active())
	}

	cfg.Debug = false
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if crashdump.Active() != "" {
		t.Error("still recording after debug was saved off")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the header-only file stayed after debug was turned off")
	}
	if !strings.Contains(buf.String(), "[debug] debug mode off") {
		t.Errorf("off logged:\n%s", buf.String())
	}

	cfg.Debug = true
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if crashdump.Active() == "" {
		t.Fatal("not recording after debug was saved on again")
	}
	if strings.Count(buf.String(), "previous crash recorded") != 0 {
		t.Error("the earlier record was announced twice")
	}

	buf.Reset()
	stopDebugMode()
	if crashdump.Active() != "" || len(crashFiles(t)) != 1 { // the old record only
		t.Errorf("after the stop: active %q, files %v", crashdump.Active(), crashFiles(t))
	}
	if buf.Len() != 0 {
		t.Errorf("a clean stop logged %q", buf.String())
	}
}
