package ui

// The app shell's state that is not a screen: the local trusted session
// (#131, C5), work handed to the window goroutine, the grace toast, the
// update check and the service restart.

import (
	"fmt"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// trustState is what the app knows of its local trusted session: the
// session /api/local-login hands out. Only that session may change the
// presets and the watch list, test a preset or list the running programs;
// without it the service masks the preset paths in GET /api/config.
type trustState int32

const (
	trustUnknown trustState = iota // not asked yet, or the service restarted since
	trustLocal                     // the service vouched for this process
	trustRefused                   // it would not (a standard user): editors locked
)

// loginGate holds back the automatic local login after a refusal, like
// the Fyne app's gate (gui/locallogin.go): a refusal will not change soon,
// and each one is a line in the service log. The window's connect, the
// heartbeats and trayexec share it.
type loginGate struct {
	mu   sync.Mutex
	last time.Time
}

// relogin asks the service to vouch for this process again unless a
// refusal is still fresh, and records the answer. Safe from any goroutine.
func (u *UI) relogin() bool {
	g := &u.gate
	g.mu.Lock()
	if !g.last.IsZero() && time.Since(g.last) < gui.LocalLoginRetryAfter {
		g.mu.Unlock()
		return false
	}
	g.last = time.Now()
	g.mu.Unlock()
	err := u.api.LocalLogin()
	switch {
	case err == nil:
		g.mu.Lock()
		g.last = time.Time{} // only a refusal holds the next attempt back
		g.mu.Unlock()
		u.trust.Store(int32(trustLocal))
		return true
	case gui.IsLocalLoginRefused(err):
		u.trust.Store(int32(trustRefused))
	}
	return false
}

// editorsLocked reports whether the service refused the local session, so
// it would refuse a save that changes the presets or the watch list. The
// pages that edit those show why and keep their save off; the save path
// sends both as "keep" (gui.WireConfig).
//
// A config that still came masked after readConfig tried to log in again
// locks them too: its presets have no paths, and saving them as shown
// would empty every one (the service refuses that anyway).
// lockedNote says why the editors are locked.
func (u *UI) lockedNote() string {
	if trustState(u.trust.Load()) != trustRefused && u.masked.Load() {
		return u.t("localonly.relogin")
	}
	return u.t("localonly.note")
}

func (u *UI) editorsLocked() bool {
	return trustState(u.trust.Load()) == trustRefused || u.masked.Load()
}

// withLocalSession runs call and, when the service wants the local session
// again (it restarted, the secret changed), obtains it once more and
// retries. Off the window goroutine.
func withLocalSession[T any](u *UI, call func() (T, error)) (T, error) {
	v, err := call()
	if gui.NeedsLocalSession(err) && u.relogin() {
		v, err = call()
	}
	return v, err
}

// onWindow runs fn on the window goroutine before the next frame: how a
// background call hands a result to widget state (forms, page fields).
// Safe from any goroutine. With no window open, fn waits for the next one.
func (u *UI) onWindow(fn func()) {
	u.mu.Lock()
	u.tasks = append(u.tasks, fn)
	u.mu.Unlock()
	u.invalidate()
}

// runTasks drains onWindow's queue. Window goroutine only.
func (u *UI) runTasks() {
	u.mu.Lock()
	tasks := u.tasks
	u.tasks = nil
	u.mu.Unlock()
	for _, fn := range tasks {
		fn()
	}
}

// announceSchedule raises the grace toast for a schedule not yet
// announced (a new one, or one that changed hands). Runs on the poller.
func (u *UI) announceSchedule(s gui.Schedule) {
	l := u.language()
	if n, ok := u.grace.Next(l, s); ok {
		go gui.ShowGraceNotice(l, n)
	}
}

// checkUpdates asks GitHub for the newest release. A newer one shows a
// badge on the update row and, once per version, a notification; a
// version the user chose to skip stays silent (#135). startup is the
// check at start with the window opening: it notifies even when this
// version was notified before, like the Fyne app's start-up dialog.
func (u *UI) checkUpdates(startup bool) {
	if !u.prefs.Bool(prefCheckUpdates, true) {
		return
	}
	chk, err := gui.CheckLatestRelease()
	if err != nil {
		return
	}
	tag := chk.Tag()
	if !gui.IsNewerRelease(u.version, tag) {
		u.update(func(s *snapshot) { s.updateTag = "" })
		return
	}
	u.update(func(s *snapshot) { s.updateTag, s.updateCheck = tag, chk })
	if u.prefs.String(gui.PrefUpdateSkipped, "") == tag {
		return
	}
	if !startup && u.prefs.String(gui.PrefUpdateNotified, "") == tag {
		return
	}
	u.prefs.Set(gui.PrefUpdateNotified, tag)
	l := u.language()
	_ = gui.Notify(gui.T(l, "update.title"), fmt.Sprintf(gui.T(l, "update.body"), tag, u.version))
}

// restartService asks the service to restart itself; the poller picks it
// up again. The save toast's [지금 재시작] (#139) and later the service
// page use it.
func (u *UI) restartService() {
	u.act(u.api.RestartService, u.t("settings.restarting"), func() {
		u.update(func(s *snapshot) { s.conn = connPending })
	})
}
