package gui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// countingTheme is the app theme with a font loader that only counts.
func countingTheme(loads *int) *koreanTheme {
	th := newKoreanTheme()
	th.load = func() (fyne.Resource, fyne.Resource) {
		*loads++
		return nil, nil // the default theme's fonts stand in
	}
	return th
}

// The fonts are read on the first measurement, once, and not for the
// monospace log view.
func TestThemeLoadsFontsLazily(t *testing.T) {
	var loads int
	th := countingTheme(&loads)
	if loads != 0 {
		t.Fatal("constructing the theme read the fonts")
	}
	th.Font(fyne.TextStyle{Monospace: true})
	if loads != 0 {
		t.Error("the monospace font read Malgun Gothic")
	}
	th.Font(fyne.TextStyle{})
	th.Font(fyne.TextStyle{Bold: true})
	if loads != 1 {
		t.Errorf("fonts read %d times, want once", loads)
	}
}

// A minimized start builds the whole window without measuring any text:
// the content goes in only when the window is first shown.
func TestDeferredContentMeasuresNothing(t *testing.T) {
	syncBackground = true
	defer func() { syncBackground = false }()
	a := test.NewTempApp(t)
	var loads int
	a.Settings().SetTheme(countingTheme(&loads))
	u := &ui{app: a, client: NewClient(1), version: "test", quit: make(chan struct{}), lang: LangEn, deferContent: true}
	u.win = a.NewWindow(windowTitle)
	defer u.win.Close()
	placeholder := u.win.Content()

	u.rebuild()
	// What the pollers do before anyone opens the window.
	u.setStatus("status")
	u.setConn(connOK)
	u.applyConnected(true)
	u.adoptConfig(storedConfig(), nil)
	u.showScheduleDetail("detail", true, true)
	u.renderLogs()
	if loads != 0 {
		t.Errorf("the hidden window read the fonts %d times", loads)
	}
	if u.win.Content() != placeholder || u.tabs != nil {
		t.Fatal("the window was built before it was shown")
	}

	u.ensureContent()
	if u.win.Content() == placeholder || u.tabs == nil || u.deferContent {
		t.Fatal("ensureContent did not build the window")
	}
	if loads != 1 {
		t.Errorf("fonts read %d times once shown, want 1", loads)
	}
	// The baseline fetched while hidden fills the forms.
	if u.portEntry.Text != "5001" || u.forms.tabAt(tabPresets) == nil || len(u.presets.rows) != 1 {
		t.Errorf("forms not filled from the baseline: port %q", u.portEntry.Text)
	}
}

// The logs are polled only while their tab is in front of a window on
// screen, with auto refresh on.
func TestLogsPollWanted(t *testing.T) {
	u := &ui{}
	u.connected.Store(true)
	u.visible.Store(true)
	u.shownTab.Store(tabLogs)
	u.logsAutoOn.Store(true)
	if !u.logsPollWanted() {
		t.Fatal("logs tab in front, shown, auto on: want polling")
	}
	for name, off := range map[string]func(){
		"hidden":       func() { u.visible.Store(false) },
		"other tab":    func() { u.shownTab.Store(tabSettings) },
		"auto off":     func() { u.logsAutoOn.Store(false) },
		"disconnected": func() { u.connected.Store(false) },
	} {
		off()
		if u.logsPollWanted() {
			t.Errorf("%s: still polling", name)
		}
		u.connected.Store(true)
		u.visible.Store(true)
		u.shownTab.Store(tabLogs)
		u.logsAutoOn.Store(true)
	}
}
