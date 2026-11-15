package ui

// The form framework: every sub-page that edits part of the service config
// is a form, and one coordinator serves them all (as the Fyne app's
// coordinator did for its tabs).
//
//   - Baseline. The config as last read (snapshot.cfg, with an old
//     service's missing grace length filled in) is every form's baseline.
//     When a fresh config arrives (the poller reads it while the window is
//     open, and after every save), forms with nothing unsaved are refilled
//     from it; a form with edits keeps them and is compared against the new
//     baseline instead.
//   - Save. A save re-reads the config first. If the service changed
//     meanwhile (the WebUI, Telegram, another app), that becomes the
//     baseline and the form's own fields are written over it, so a save
//     only ever changes what the saving page owns. Then: the routine-slot
//     question when a preset or watch slot changes hands, the POST, a new
//     login when the secret changed, a re-read, and the result adopted.
//   - Outcome. "저장했어요" as a toast, with [지금 재시작] when the change
//     needs a service restart (#139); the service's preset warnings, a
//     failed check or a failed save as a banner at the top of the page.
//
// A form's apply writes only its own fields, so the secret, the masked bot
// token and everything another page owns travel back unchanged.

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// form is a sub-page with fields of the service config. Its methods run on
// the window goroutine. The page keeps its widgets' values in plain fields
// (read from the widgets at the top of body), so dirty and apply never
// touch widget state.
type form interface {
	screen
	// fill shows cfg: called when the page opens with nothing unsaved,
	// when 되돌리기 is pressed, and when a fresh config arrives while the
	// page has nothing unsaved.
	fill(u *UI, cfg gui.Config)
	// dirty reports whether saving the page would change base.
	dirty(u *UI, base gui.Config) bool
	// apply writes the page's own fields over cfg and checks them; the
	// error is shown as is, so word it for the user (a locale text). It
	// must not write into slices or maps cfg already holds: assign new
	// ones (cfg is a copy, but its slices are shared with the baseline).
	apply(u *UI, cfg *gui.Config) error
}

// savedForm is a form that wants to know a save went through (beyond
// being refilled from the result): to clear a typed token, say.
type savedForm interface {
	saved(u *UI, reply gui.SaveReply)
}

// formState is the coordinator. Window goroutine only.
type formState struct {
	// base is the baseline the forms were last filled from; nil until the
	// first config, and nothing is dirty before it.
	base *gui.Config
	// gen is the snapshot's cfgGen that base came from.
	gen uint64
	// saving is set while a save is in flight: a second one would merge
	// over a stale baseline and could undo the first.
	saving bool
}

// forms lists the registered pages that are forms.
func (u *UI) forms() []form {
	var out []form
	for _, id := range pageOrder {
		if f, ok := u.pages[id].(form); ok {
			out = append(out, f)
		}
	}
	return out
}

// baseline is cfg as the forms compare against it.
func baseline(cfg gui.Config) gui.Config { return gui.SettingsBaseline(cfg) }

// cloneConfig is a deep copy of cfg (a JSON round trip keeps nil and
// empty slices apart, which the service tells apart too).
func cloneConfig(cfg gui.Config) gui.Config {
	data, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	var out gui.Config
	if json.Unmarshal(data, &out) != nil {
		return cfg
	}
	return out
}

// syncForms adopts a config the poller brought, once per new one. Called
// at the top of every frame.
func (u *UI) syncForms(s snapshot) {
	fs := &u.formSt
	if !s.cfgSet || s.cfgGen == fs.gen {
		return
	}
	fs.gen = s.cfgGen
	u.adoptConfig(baseline(s.cfg), fs.base)
}

// adoptConfig makes fresh the baseline. ref is what the forms were last
// filled with or saved as: a form that still matches it has nothing
// unsaved and is refilled from fresh; one that does not keeps its edits. A
// nil ref (the first config) fills every form.
func (u *UI) adoptConfig(fresh gui.Config, ref *gui.Config) {
	fs := &u.formSt
	all := u.forms()
	keep := make([]bool, len(all))
	if ref != nil {
		for i, f := range all {
			keep[i] = f.dirty(u, *ref)
		}
	}
	fs.base = &fresh
	for i, f := range all {
		if !keep[i] {
			f.fill(u, fresh)
		}
	}
}

// formDirty reports whether p is a form with unsaved edits.
func (u *UI) formDirty(p screen) bool {
	f, ok := p.(form)
	return ok && u.formSt.base != nil && f.dirty(u, *u.formSt.base)
}

// revertForm puts f back to the baseline (되돌리기, leaving with 뒤로).
func (u *UI) revertForm(f form) {
	if u.formSt.base != nil {
		f.fill(u, *u.formSt.base)
	}
	u.clearNotice(f)
}

// formOpened fills a form that has nothing unsaved as it opens, so it
// shows the config as it is now.
func (u *UI) formOpened(f form) {
	if b := u.formSt.base; b != nil && !f.dirty(u, *b) {
		f.fill(u, *b)
	}
}

// saveForm is the save bar's 저장: check, ask about routine slots, then
// post (postForm). Window goroutine.
func (u *UI) saveForm(f form) {
	fs := &u.formSt
	if fs.base == nil || fs.saving {
		return
	}
	sent := cloneConfig(*fs.base)
	if err := f.apply(u, &sent); err != nil {
		u.setNotice(f, noticeError, err.Error())
		return
	}
	u.clearNotice(f)
	post := func() { u.postForm(f, sent) }
	if q := gui.SlotChangeQuestion(u.language(), *fs.base, sent); q != "" {
		u.ask(askSpec{title: u.t("slotchange.title"), body: q, ok: u.t("settings.save"), onOK: post})
		return
	}
	post()
}

// postForm re-reads the config, merges the form over it when the service
// changed meanwhile, and posts. Window goroutine; the requests run on
// their own.
func (u *UI) postForm(f form, sent gui.Config) {
	fs := &u.formSt
	if fs.base == nil || fs.saving {
		return
	}
	fs.saving = true
	base := *fs.base
	go func() {
		now, err := withLocalSession(u, u.api.GetConfig)
		u.onWindow(func() {
			if err == nil && !reflect.DeepEqual(baseline(now), base) {
				// Changed meanwhile: that is the baseline now (other forms
				// keep their edits), and the form goes over it.
				u.setConfig(now)
				fs.gen = u.current().cfgGen
				u.adoptConfig(baseline(now), &base)
				base = *fs.base
				sent = cloneConfig(base)
				if err := f.apply(u, &sent); err != nil {
					fs.saving = false
					u.setNotice(f, noticeError, err.Error())
					return
				}
			}
			go u.sendConfig(f, base, sent)
		})
	}()
}

// saveOutcome is what the background part of a save hands back.
type saveOutcome struct {
	reply      gui.SaveReply
	fresh      gui.Config
	err        error
	reloginErr error // the secret changed and the new login failed
}

// sendConfig posts sent and re-reads the result. Off the window goroutine.
func (u *UI) sendConfig(f form, base, sent gui.Config) {
	var out saveOutcome
	wire := gui.WireConfig(sent, u.editorsLocked())
	out.reply, out.err = withLocalSession(u, func() (gui.SaveReply, error) { return u.api.SaveConfig(wire) })
	if out.err == nil {
		// A new secret (notably the first one) leaves this client without
		// a valid session: the local one first (a secret session would
		// mask the presets, C5), the secret if that is refused.
		if gui.ShouldReloginAfterSave(base.Secret, sent.Secret) {
			u.trust.Store(int32(trustUnknown))
			if !u.relogin() {
				out.reloginErr = u.api.Login(sent.Secret)
			}
		}
		var err error
		if out.fresh, err = u.api.GetConfig(); err != nil {
			out.fresh = gui.SavedFallback(sent, base.Telegram.BotToken)
		}
	}
	u.onWindow(func() { u.finishSave(f, sent, out) })
}

// finishSave adopts a save's result and says how it went. Window
// goroutine.
func (u *UI) finishSave(f form, sent gui.Config, out saveOutcome) {
	fs := &u.formSt
	fs.saving = false
	now := time.Now()
	if out.err != nil {
		msg := out.err.Error()
		if gui.IsLocalOnly(out.err) {
			msg = u.t("localonly.note")
		}
		u.setNotice(f, noticeError, msg)
		return
	}
	u.setConfig(out.fresh)
	fs.gen = u.current().cfgGen
	u.adoptConfig(baseline(out.fresh), &sent)
	if sf, ok := f.(savedForm); ok {
		sf.saved(u, out.reply)
	}
	if lines := gui.PresetWarningLines(u.language(), out.reply.Warnings); len(lines) > 0 {
		u.setNotice(f, noticeWarn, strings.Join(lines, "\n\n"))
	}
	if gui.ServiceNeedsRestart(out.reply.Message) {
		u.toast.showAction(u.t("settings.saved.restart"), u.t("settings.restart.now"), u.restartService, now)
	} else {
		u.toast.show(u.t("settings.saved.body"), false, now)
	}
	if out.reloginErr != nil {
		u.update(func(s *snapshot) { s.conn = connLogin })
	}
}
