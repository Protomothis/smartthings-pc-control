package ui

import (
	"time"

	"fyne.io/systray"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/internal/health"
	"github.com/Protomothis/smartthings-pc-control/trayexec"
)

// Run is the whole app: the single-instance check, the start-up chores,
// the tray and, unless minimized, the window. It blocks until the app
// quits. Call it from the main goroutine: systray needs the main thread,
// and Gio windows on Windows run their own message loops on other threads,
// so app.Main is not needed.
//
// It is what "gui" (and a double-click) runs, and does what the Fyne
// app's gui.Run did: one app per session (a
// later launch opens this one's window), debug mode from tray.json, the
// toast protocol and shortcut, the autostart entry rewritten for the
// current exe, the heartbeats and the service's user-session jobs
// (trayexec, #164) for as long as the tray runs, the grace toast and the
// daily update check.
func Run(version string, minimized bool) {
	inst := gui.ClaimInstance()
	if !inst.First() {
		// A minimized launch (login autostart, or the service waking the
		// tray app for a grace toast) stays silent: the running app has the
		// tray and shows the toast itself. Any other launch opens its
		// window.
		if !minimized {
			inst.ActivateRunning()
		}
		return
	}
	gui.StartDebugMode(version)
	defer gui.StopDebugMode()
	gui.PrepareStart()

	port := gui.LocalWebUIPort()
	client := gui.NewClient(port)
	u := New(client, version, loadPrefs(defaultPrefsPath()), systray.Quit)
	u.snap.port = port
	// Default on: the grace toast and, since #164, the service's
	// user-session commands only work while the tray app runs. Rewritten
	// every start so it tracks the current exe path.
	if u.prefs.Bool(prefAutostart, true) {
		go func() { _ = gui.SetAutostart(true) }()
	}
	stopActivation := inst.WatchActivation(u.Show)
	defer stopActivation()
	run(u, background{newTrayExec(client, u), &shellLoops{u: u, startup: !minimized}}, minimized)
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

// background runs several Backgrounds as one.
type background []Background

func (b background) Start() {
	for _, x := range b {
		x.Start()
	}
}

func (b background) Stop() {
	for i := len(b) - 1; i >= 0; i-- {
		b[i].Stop()
	}
}

// trayExec runs trayexec on the window's client, sharing its session
// cookie, its port and its local-login gate.
type trayExec struct {
	c    *gui.Client
	u    *UI
	stop func()
}

func newTrayExec(c *gui.Client, u *UI) *trayExec { return &trayExec{c: c, u: u} }

func (t *trayExec) options() trayexec.Options {
	return trayexec.Options{
		Base:    t.c.BaseURL,
		Jar:     t.c.CookieJar(),
		Relogin: t.u.relogin,
		Logf:    func(format string, args ...any) { gui.Logf("trayexec", format, args...) },
	}
}

func (t *trayExec) Start() {
	t.stop = trayexec.Start(t.options())
}

func (t *trayExec) Stop() {
	if t.stop != nil {
		t.stop()
	}
	// After the poll loop has ended, so no poll of this process follows the
	// notice: the user quit, the service should not bring the tray back on
	// its own (#167). An update closing the app sends nothing.
	if t.u.userQuit.Load() {
		if err := trayexec.NotifyQuit(t.options()); err != nil {
			gui.Logf("trayexec", "quit notice to the service: %v", err)
		}
	}
}

// updateEvery is how often the update check runs after the one at start.
const updateEvery = 24 * time.Hour

// shellLoops are the tray's own timers, window or not: the full heartbeat
// every 30s, the media change check every 3s (#117) and the update check.
type shellLoops struct {
	u       *UI
	startup bool // the window opens at start: the first check may say so
	done    chan struct{}
	stopped chan struct{}
}

func (l *shellLoops) Start() {
	l.done = make(chan struct{})
	l.stopped = make(chan struct{})
	go l.loop()
}

func (l *shellLoops) Stop() {
	if l.done == nil {
		return
	}
	close(l.done)
	<-l.stopped
}

func (l *shellLoops) loop() {
	defer close(l.stopped)
	u := l.u
	hb := time.NewTicker(gui.HeartbeatInterval)
	defer hb.Stop()
	media := time.NewTicker(gui.MediaWatchInterval)
	defer media.Stop()
	upd := time.NewTicker(updateEvery)
	defer upd.Stop()
	go u.checkUpdates(l.startup)
	trayLog := func(f string, a ...any) { gui.Logf("tray", f, a...) }
	go health.Watch(l.done, trayHealthLimits, trayLog)
	// CPU that stays high leaves a profile and a goroutine dump (#176).
	go health.WatchCPU(l.done, gui.TrayDiagDir(), trayLog)
	for {
		select {
		case <-l.done:
			return
		case <-hb.C:
			// One at a time, like the media check: a reading stuck in a
			// system call must not leave a goroutine (and the OS thread
			// the COM work locks) behind every 30 seconds.
			if u.hbBusy.CompareAndSwap(false, true) {
				go func() {
					defer u.hbBusy.Store(false)
					u.heartbeat.SendFull(heartbeatClient{u})
				}()
			}
		case <-media.C:
			// Like the heartbeat, not gated on the connection: the change
			// check keeps the service current while the window is closed.
			if u.mediaBusy.CompareAndSwap(false, true) {
				go func() {
					defer u.mediaBusy.Store(false)
					u.heartbeat.SendChanges(heartbeatClient{u})
				}()
			}
		case <-upd.C:
			go u.checkUpdates(false)
		}
	}
}

// trayHealthLimits are well above what the tray app holds normally
// (around 20 threads and 600 handles).
var trayHealthLimits = health.Limits{Goroutines: 300, Threads: 80, Handles: 3000}

// heartbeatClient is the UI's gui.HeartbeatClient.
type heartbeatClient struct{ u *UI }

func (h heartbeatClient) SessionHeartbeat(hb gui.Heartbeat) (bool, error) {
	return h.u.api.SessionHeartbeat(hb)
}
func (h heartbeatClient) Relogin() bool { return h.u.relogin() }
