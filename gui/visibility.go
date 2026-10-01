package gui

import (
	"time"

	"fyne.io/fyne/v2"
)

// Work for someone looking at the window happens only while it is on
// screen (refactor-plan §3.3): the keep-awake row, the battery label and
// the media card are polled while the window is shown, the logs only while
// the logs tab is, and a login autostart that stays in the tray never
// builds its layout (or reads the 26 MB of fonts, theme.go) until the
// window is first opened. What the tray and the service need keeps
// running regardless: the schedule poll (tray countdown, grace toast), the
// reconnect attempts and the heartbeats.

// visibleCheckInterval is how often pollLoop looks whether the window is
// on screen; a window that just opened catches up at the next look.
const visibleCheckInterval = time.Second

// ensureContent builds the window a minimized start left out (rebuild
// builds only the tray then); a no-op once built. This is what measures
// text, and so reads the fonts, for the first time. The forms are filled
// from the baseline initialLoad may have fetched meanwhile. UI goroutine
// only.
func (u *ui) ensureContent() {
	if !u.deferContent {
		return
	}
	u.deferContent = false
	u.rebuild()
}

// showWindow brings the window up from the tray. UI goroutine only.
func (u *ui) showWindow() {
	u.ensureContent()
	u.win.Show()
	u.win.RequestFocus()
}

// checkVisible records whether the window is on screen and, when it has
// just come up, fills it with what is only loaded while it is. Runs off
// the UI goroutine (pollLoop). A window shown some other way than
// showWindow (a second launch restoring it) gets its content here.
func (u *ui) checkVisible() {
	vis := windowOnScreen()
	if u.visible.Swap(vis) == vis || !vis {
		return
	}
	fyne.Do(u.ensureContent)
	if u.connected.Load() {
		u.loadShown()
	}
}

// loadShown loads what is only polled while the window is on screen: the
// command tab's rows, the battery, and the tab in front if it has data of
// its own. Runs off the UI goroutine.
func (u *ui) loadShown() {
	u.loadAwake()
	u.loadBattery()
	u.loadMedia()
	switch int(u.shownTab.Load()) {
	case tabLogs:
		u.loadLogs()
	case tabNetwork:
		u.loadNetwork()
		u.loadSTHub()
	}
}

// onScreen reports what checkVisible last saw.
func (u *ui) onScreen() bool { return u.visible.Load() }

// logsPollWanted reports whether the 3 s logs refresh should run: the logs
// tab is in front of a window on screen, with auto refresh on.
func (u *ui) logsPollWanted() bool {
	return u.connected.Load() && u.onScreen() && int(u.shownTab.Load()) == tabLogs && u.logsAutoOn.Load()
}
