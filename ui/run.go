package ui

import (
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/trayexec"
)

// Run starts the tray and, unless minimized, the window; it blocks until
// the app quits. Call it from the main goroutine: systray needs the main
// thread, and Gio windows on Windows run their own message loops on other
// threads, so app.Main is not needed. The service's user-session jobs
// (trayexec, #164) run as long as the tray does.
//
// Not yet here (later #165 phases, still only in gui.Run): the single
// instance lock, the idle and media heartbeats, the grace toast, the
// update check and the autostart refresh.
func Run(version string, minimized bool) {
	port := gui.LocalWebUIPort()
	client := gui.NewClient(port)
	u := New(client, version, loadPrefs(defaultPrefsPath()), systray.Quit)
	u.snap.port = port
	run(u, newTrayExec(client), minimized)
}

// run drives the tray with bg started alongside it.
func run(u *UI, bg Background, minimized bool) {
	stop := make(chan struct{})
	systray.Run(func() {
		u.trayReady()
		go u.pollLoop(stop)
		bg.Start()
		if !minimized {
			u.Show()
		}
	}, func() {
		close(stop)
		bg.Stop()
	})
}

// reloginEvery holds back the executor's local re-login after an attempt,
// like the Fyne app's gate (gui/locallogin.go): a refusal will not change
// soon, and each one is a log line on the service.
const reloginEvery = 2 * time.Minute

// trayExec runs trayexec on the window's client, sharing its session
// cookie and port.
type trayExec struct {
	c    *gui.Client
	stop func()

	mu   sync.Mutex
	last time.Time
}

func newTrayExec(c *gui.Client) *trayExec { return &trayExec{c: c} }

func (t *trayExec) Start() {
	t.stop = trayexec.Start(trayexec.Options{Base: t.c.BaseURL, Jar: t.c.CookieJar(), Relogin: t.relogin})
}

func (t *trayExec) Stop() {
	if t.stop != nil {
		t.stop()
	}
}

func (t *trayExec) relogin() bool {
	t.mu.Lock()
	if time.Since(t.last) < reloginEvery {
		t.mu.Unlock()
		return false
	}
	t.last = time.Now()
	t.mu.Unlock()
	if t.c.LocalLogin() != nil {
		return false
	}
	t.mu.Lock()
	t.last = time.Time{} // only a refusal holds the next attempt back
	t.mu.Unlock()
	return true
}
