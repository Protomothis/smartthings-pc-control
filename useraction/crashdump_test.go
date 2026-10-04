package useraction

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/crashdump"
)

func TestCrashDirIn(t *testing.T) {
	if got, want := CrashDirIn(`C:\Users\u\AppData\Local`), `C:\Users\u\AppData\Local\SmartThings PC Control\crash`; got != want {
		t.Errorf("CrashDirIn = %q, want %q", got, want)
	}
	if got := CrashDirIn(""); got != "" {
		t.Errorf("CrashDirIn without LOCALAPPDATA = %q", got)
	}
}

// tray.json's switch: a missing file, an older one without the key and
// debug: false are all off.
func TestTrayDebug(t *testing.T) {
	dir := t.TempDir()
	if trayDebug(dir) {
		t.Error("no tray.json reads as on")
	}
	for doc, want := range map[string]bool{
		`{"port": 5001}`:                  false,
		`{"port": 5001, "debug": false}`:  false,
		`{"port": 5001, "debug": true}`:   true,
		`{"port": 5001, "debug": "true"}`: false,
		`not json`:                        false,
	} {
		if err := os.WriteFile(filepath.Join(dir, trayFileName), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := trayDebug(dir); got != want {
			t.Errorf("%s: trayDebug = %v, want %v", doc, got, want)
		}
	}
}

func TestEnvValue(t *testing.T) {
	env := []string{"=C:=C:\\", "PATH=C:\\Windows", "LocalAppData=C:\\Users\\u\\AppData\\Local"}
	if got := envValue(env, "LOCALAPPDATA"); got != `C:\Users\u\AppData\Local` {
		t.Errorf("LOCALAPPDATA = %q", got)
	}
	if got := envValue(env, "TEMP"); got != "" {
		t.Errorf("TEMP = %q", got)
	}
}

// Without tray.json saying debug (the test binary has none beside it) a
// run records nothing and the stop function is harmless.
func TestStartCrashDumpOffWithoutTrayDebug(t *testing.T) {
	stop := StartCrashDump("test")
	if crashdump.Active() != "" {
		crashdump.Disable()
		t.Fatal("recording without debug in tray.json")
	}
	stop()
}
