package gui

import (
	"testing"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// The crash folder sits next to gui.log, or in temp without LOCALAPPDATA.
func TestCrashDirIn(t *testing.T) {
	if got, want := crashDirIn(`C:\L\SmartThings PC Control`, `C:\T`), `C:\L\SmartThings PC Control\crash`; got != want {
		t.Errorf("crashDirIn = %q, want %q", got, want)
	}
	if got, want := crashDirIn("", `C:\T`), `C:\T\SmartThings PC Control\crash`; got != want {
		t.Errorf("crashDirIn without a data folder = %q, want %q", got, want)
	}
	// The user-action runs write into the same folder.
	lad := `C:\Users\u\AppData\Local`
	if got, want := useraction.CrashDirIn(lad), crashDirIn(userDataDirIn(lad), `C:\T`); got != want {
		t.Errorf("useraction.CrashDirIn = %q, the app's is %q", got, want)
	}
}

// Without startDebugMode (the tests, any process but the app's own Run)
// the switch records nothing.
func TestSetDebugModeIsInertUntilStarted(t *testing.T) {
	setDebugMode(true)
	if debugState.on {
		stopDebugMode()
		t.Fatal("debug mode turned on without startDebugMode")
	}
}

// The settings model carries the switch: Fill, Dirty and ApplyTo agree.
func TestSettingsStateDebugRoundTrip(t *testing.T) {
	cfg := storedConfig()
	cfg.Debug = true
	s := settingsStateFromConfig(cfg)
	if !s.Debug || s.dirty(cfg) {
		t.Fatalf("state from a debug config: %+v (dirty %v)", s, s.dirty(cfg))
	}
	s.Debug = false
	if !s.dirty(cfg) {
		t.Error("switching debug off is not a change")
	}
	out := cfg
	if err := s.applyTo(&out, LangEn); err != nil || out.Debug {
		t.Errorf("applyTo: debug %v, err %v", out.Debug, err)
	}
}
