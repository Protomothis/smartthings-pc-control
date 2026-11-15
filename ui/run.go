package ui

import (
	"fyne.io/systray"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// Run starts the tray and, unless minimized, the window; it blocks until
// the app quits. Call it from the main goroutine: systray needs the main
// thread, and Gio windows on Windows run their own message loops on other
// threads, so app.Main is not needed. bg (nil for none) starts with the
// tray and stops when the app quits (#164).
//
// Not yet here (later #165 phases, still only in gui.Run): the single
// instance lock, the idle and media heartbeats, the grace toast, the
// update check and the autostart refresh.
func Run(version string, minimized bool, bg Background) {
	port := gui.LocalWebUIPort()
	u := New(gui.NewClient(port), version, loadPrefs(defaultPrefsPath()), systray.Quit)
	u.snap.port = port
	stop := make(chan struct{})
	systray.Run(func() {
		u.trayReady()
		go u.pollLoop(stop)
		if bg != nil {
			bg.Start()
		}
		if !minimized {
			u.Show()
		}
	}, func() {
		close(stop)
		if bg != nil {
			bg.Stop()
		}
	})
}
