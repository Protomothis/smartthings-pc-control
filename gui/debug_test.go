package gui

import (
	"slices"
	"testing"
)

// recordDebugMode replaces setDebugMode for one test and returns what the
// app asked for, in order.
func recordDebugMode(t *testing.T) *[]bool {
	t.Helper()
	var calls []bool
	saved := setDebugMode
	setDebugMode = func(on bool) { calls = append(calls, on) }
	t.Cleanup(func() { setDebugMode = saved })
	return &calls
}

// The crash folder sits next to gui.log, or in temp without LOCALAPPDATA.
func TestCrashDirIn(t *testing.T) {
	if got, want := crashDirIn(`C:\L\SmartThings PC Control`, `C:\T`), `C:\L\SmartThings PC Control\crash`; got != want {
		t.Errorf("crashDirIn = %q, want %q", got, want)
	}
	if got, want := crashDirIn("", `C:\T`), `C:\T\SmartThings PC Control\crash`; got != want {
		t.Errorf("crashDirIn without a data folder = %q, want %q", got, want)
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

// The developer section's switch end to end (#133): it fills from the
// service's config, makes the settings tab dirty, saves through the one
// save path (posting false too, never omitting it), and the adopted
// config hands the new value to the app's crash record.
func TestDebugToggleSavesThroughTheSettingsTab(t *testing.T) {
	calls := recordDebugMode(t)
	u, svc := newFakeServiceUI(t)
	if u.debugCheck.Checked {
		t.Fatal("the switch is on for a config without debug")
	}
	if !slices.Equal(*calls, []bool{false}) {
		t.Errorf("after the load setDebugMode got %v, want [false]", *calls)
	}

	u.debugCheck.SetChecked(true)
	if d := dirtyIndices(u.forms); !slices.Equal(d, []int{tabSettings}) {
		t.Fatalf("dirty = %v, want the settings tab", d)
	}
	u.saveForms([]*formTab{u.forms.tabAt(tabSettings)}, true, nil)
	if sent := svc.lastPost(t); !sent.Debug || sent.Secret != "s3cret" || sent.Port != 5001 {
		t.Errorf("posted debug %v secret %q port %d", sent.Debug, sent.Secret, sent.Port)
	}
	if !svc.cfg.Debug || !u.forms.base.Debug || len(dirtyIndices(u.forms)) != 0 {
		t.Errorf("after the save: stored %v, baseline %v, dirty %v", svc.cfg.Debug, u.forms.base.Debug, dirtyIndices(u.forms))
	}
	if last := (*calls)[len(*calls)-1]; !last {
		t.Errorf("setDebugMode calls %v, want the last one on", *calls)
	}

	u.debugCheck.SetChecked(false)
	u.saveForms([]*formTab{u.forms.tabAt(tabSettings)}, true, nil)
	if svc.cfg.Debug {
		t.Error("switching debug off did not reach the service")
	}
	if last := (*calls)[len(*calls)-1]; last {
		t.Errorf("setDebugMode calls %v, want the last one off", *calls)
	}

	// Turned on elsewhere (the WebUI API): a refresh fills the switch.
	svc.mu.Lock()
	svc.cfg.Debug = true
	svc.mu.Unlock()
	u.initialLoad()
	if !u.debugCheck.Checked || !(*calls)[len(*calls)-1] {
		t.Errorf("after a refresh: switch %v, calls %v", u.debugCheck.Checked, *calls)
	}
}
