package ui

// The poller keeps the snapshot current, and actions send the screens'
// requests off the window goroutine. Both write the snapshot under u.mu and
// ask for a frame.

import (
	"reflect"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// Poll periods, in poller ticks of one second.
const (
	connEvery    = 5  // a reconnect attempt while not connected
	schedEvery   = 2  // the tray tooltip shows the countdown, so also hidden
	mediaEvery   = 3  // on screen only, like the rest below
	awakeEvery   = 10 // the remaining time is shown in minutes
	hubEvery     = 10
	logsEvery    = 3
	cfgEvery     = 10 // others (the WebUI, Telegram) may change it; the forms follow
	batteryEvery = 60 // the service reads the battery once a minute
)

// pollLoop runs until stop closes.
func (u *UI) pollLoop(stop <-chan struct{}) {
	u.connect()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if u.current().conn != connOK {
			if n%connEvery == 0 {
				u.connect()
			}
			continue
		}
		if n%schedEvery == 0 {
			u.loadSchedule()
		}
		if !u.onScreen() {
			continue
		}
		if n%mediaEvery == 0 {
			u.loadMedia()
		}
		if n%awakeEvery == 0 {
			u.loadAwake()
		}
		if n%hubEvery == 0 {
			u.loadHub()
		}
		if n%batteryEvery == 0 {
			u.loadBattery()
		}
		if n%cfgEvery == 0 {
			u.loadConfig()
		}
		if n%logsEvery == 0 && u.wantLogs.Load() {
			u.loadLogs()
		}
	}
}

// current is a copy of the snapshot.
func (u *UI) current() snapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.snap
}

// update changes the snapshot and redraws.
func (u *UI) update(fn func(s *snapshot)) {
	u.mu.Lock()
	fn(&u.snap)
	u.mu.Unlock()
	u.invalidate()
}

// connect logs in on this PC (the service vouches for the app, #131), reads
// the config and, when that works, everything else once. The local session
// comes first, secret or not (C5): a config read without it has the preset
// paths masked.
func (u *UI) connect() {
	if trustState(u.trust.Load()) != trustLocal {
		u.relogin() // refused or unreachable: GetConfig says which
	}
	cfg, err := u.api.GetConfig()
	if err != nil && !gui.IsUnauthorized(err) {
		// The service may have come back on another port (#121).
		if p := gui.LocalWebUIPort(); p != u.current().port {
			u.api.SetPort(p)
			u.update(func(s *snapshot) { s.port = p })
			u.relogin()
			cfg, err = u.api.GetConfig()
		}
	}
	switch {
	case gui.IsUnauthorized(err):
		u.update(func(s *snapshot) { s.conn = connLogin })
		return
	case err != nil:
		u.forgetSession()
		u.update(func(s *snapshot) { s.conn = connLost; s.battery = gui.Battery{} })
		return
	}
	u.setConfig(cfg)
	u.update(func(s *snapshot) { s.conn = connOK })
	u.loadSchedule()
	u.loadAwake()
	u.loadBattery()
	u.loadMedia()
	u.loadHub()
}

// lost marks the service gone after a network error.
func (u *UI) lost(err error) {
	if err == nil {
		return
	}
	if gui.IsUnauthorized(err) {
		u.update(func(s *snapshot) { s.conn = connLogin })
		return
	}
	u.forgetSession()
	u.update(func(s *snapshot) { s.conn = connLost })
}

// forgetSession is for the service going away: its restart drops the
// session, so the next connect asks again. A refusal is kept.
func (u *UI) forgetSession() {
	u.trust.CompareAndSwap(int32(trustLocal), int32(trustUnknown))
}

// setConfig records a config read from the service: the forms adopt it at
// the next frame (forms.go), and the app's crash record follows its debug
// switch (#133). An unchanged config changes nothing, so the 10s poll does
// not refill the forms under the user's caret. Safe from any goroutine.
func (u *UI) setConfig(cfg gui.Config) {
	u.update(func(s *snapshot) {
		if s.cfgSet && reflect.DeepEqual(s.cfg, cfg) {
			return
		}
		s.cfg, s.cfgSet = cfg, true
		s.cfgGen++
	})
	gui.FollowDebugMode(cfg.Debug)
}

func (u *UI) loadSchedule() {
	sc, err := u.api.GetSchedule()
	if err != nil {
		u.lost(err)
		return
	}
	u.update(func(s *snapshot) { s.sched, s.schedAt = sc, time.Now() })
	u.announceSchedule(sc)
	u.refreshTray()
}

func (u *UI) loadAwake() {
	a, err := u.api.GetAwake()
	if err != nil && !gui.IsAwakeUnsupported(err) {
		u.lost(err)
		return
	}
	u.update(func(s *snapshot) { s.awake, s.awakeErr, s.awakeSet = a, err, true })
}

func (u *UI) loadBattery() {
	b, err := u.api.GetBattery()
	if err != nil {
		b = gui.Battery{}
	}
	u.update(func(s *snapshot) { s.battery = b })
}

func (u *UI) loadMedia() {
	m, err := u.api.GetMedia()
	u.update(func(s *snapshot) { s.media, s.mediaErr, s.mediaSet = m, err, true })
}

func (u *UI) loadHub() {
	h, err := u.api.GetSTHub()
	if err == nil {
		u.update(func(s *snapshot) { s.hub, s.hubSet = h, true })
	}
	if tg, err := u.api.TelegramState(); err == nil {
		u.update(func(s *snapshot) { s.tg = tg })
	}
}

func (u *UI) loadLogs() {
	lines, err := u.api.Logs()
	u.update(func(s *snapshot) { s.logs, s.logsErr, s.logsSet = lines, err, true })
}

func (u *UI) loadConfig() {
	cfg, err := u.api.GetConfig()
	if err != nil {
		u.lost(err)
		return
	}
	u.setConfig(cfg)
}

// act runs fn off the window goroutine and toasts its outcome: ok on
// success, the error otherwise. then (optional) runs afterwards either way.
func (u *UI) act(fn func() error, ok string, then func()) {
	go func() {
		err := fn()
		switch {
		case err != nil:
			u.toast.show(err.Error(), true, time.Now())
		case ok != "":
			u.toast.show(ok, false, time.Now())
		}
		if then != nil {
			then()
		}
		u.invalidate()
	}()
}
