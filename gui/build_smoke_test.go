package gui

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// TestRebuildDoesNotPanic builds the whole window once per language with
// Fyne's in-memory test app. Widget constructors fire callbacks while the
// tabs are being assembled (Select.SetSelectedIndex calls OnChanged), so a
// field used by a callback before it is assigned panics here instead of on
// the user's desktop — v1.2.0-rc1's tray app died that way in buildAwakeRow.
func TestRebuildDoesNotPanic(t *testing.T) {
	syncBackground = true
	defer func() { syncBackground = false }()
	// Building the settings tab must not touch the login Run entry: it did,
	// and pointed it at the test binary.
	writes := 0
	setAutostart = func(bool) error { writes++; return nil }
	defer func() { setAutostart = SetAutostart }()
	defer func() {
		if writes != 0 {
			t.Errorf("building the window wrote the autostart entry %d times", writes)
		}
	}()
	for _, lang := range []Lang{LangKo, LangEn} {
		t.Run(string(lang), func(t *testing.T) {
			a := test.NewTempApp(t)
			u := &ui{
				app:     a,
				client:  NewClient(1), // never contacted: nothing polls in this test
				version: "test",
				quit:    make(chan struct{}),
				lang:    lang,
			}
			u.win = a.NewWindow(windowTitle)
			defer u.win.Close()
			u.rebuild()
			// A second rebuild is what a language switch does.
			u.rebuild()
		})
	}
}
