package gui

import (
	"errors"
	"strings"

	"fyne.io/fyne/v2/dialog"
)

// The save coordinator (refactor-plan §3.3). Every tab that edits part of
// the service config is a formTab; one baseline (the config as last
// fetched) and one save path serve them all:
//
//   - Save merges the saving tabs' ApplyTo over the baseline, POSTs it,
//     re-reads the config and adopts that as the new baseline.
//   - Adopting a config (after a save, a reconnect or a language switch)
//     refills only the tabs that have nothing unsaved; a tab with edits
//     keeps them and is compared against the new baseline instead.
//   - The "•" markers and Save buttons of every tab are recomputed after
//     each change, since all of them compare against the same baseline.
//
// Each tab's ApplyTo writes only its own fields, so the secret, the masked
// bot token and everything else another tab owns travel back unchanged.

// formTab is one tab's share of the config form.
type formTab struct {
	// index is the tab's position in u.tabs (the "•" marker); bar its
	// footer. Both are unset in the model-only tests.
	index int
	bar   *saveBar

	// Fill writes cfg into the tab.
	Fill func(cfg Config)
	// Dirty reports whether saving the tab would change base.
	Dirty func(base Config) bool
	// ApplyTo writes the tab's fields over cfg and checks them; the error
	// is worded for the user. It writes what it can even when it fails, so
	// a language switch can carry the edits over (forms.drafts).
	ApplyTo func(cfg *Config) error
}

// forms is the coordinator's state. It outlives a rebuild (language
// change); only tabs is replaced.
type forms struct {
	// base is the config as last fetched or saved; nil until the first
	// load, and nothing is dirty before it.
	base *Config
	tabs []*formTab
	// filling is set while tabs are being filled, so the change callbacks
	// that SetText/SetChecked fire do not recompute anything half-way.
	filling bool
	// saving is set while a save is in flight: a second one would merge
	// over the stale baseline and could undo the first.
	saving bool
}

// register adds a tab (called by the tab builders during rebuild).
func (f *forms) register(t *formTab) *formTab {
	f.tabs = append(f.tabs, t)
	return t
}

// tabAt is the form tab at a u.tabs index, or nil.
func (f *forms) tabAt(index int) *formTab {
	for _, t := range f.tabs {
		if t.index == index {
			return t
		}
	}
	return nil
}

// isDirty reports whether t differs from the baseline.
func (f *forms) isDirty(t *formTab) bool {
	return f.base != nil && !f.filling && t.Dirty(*f.base)
}

// dirtyTabs are the tabs with unsaved edits, in registration order.
func (f *forms) dirtyTabs() []*formTab {
	var out []*formTab
	for _, t := range f.tabs {
		if f.isDirty(t) {
			out = append(out, t)
		}
	}
	return out
}

// merge is the config a save of the given tabs posts: the baseline with
// each tab's fields written over it. The first problem stops it.
func (f *forms) merge(saving []*formTab) (Config, error) {
	cfg := *f.base
	for _, t := range saving {
		if err := t.ApplyTo(&cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// fill writes cfg into t with the change callbacks muted.
func (f *forms) fill(t *formTab, cfg Config) {
	was := f.filling
	f.filling = true
	t.Fill(cfg)
	f.filling = was
}

// adopt makes fresh the baseline. ref is what the tabs were last filled
// with or saved as: a tab that still matches it has nothing unsaved and
// is refilled from fresh; a tab that does not keeps its edits. A nil ref
// (the first load) fills every tab.
func (f *forms) adopt(fresh Config, ref *Config) {
	keep := make([]bool, len(f.tabs))
	if ref != nil {
		for i, t := range f.tabs {
			keep[i] = t.Dirty(*ref)
		}
	}
	f.base = &fresh
	for i, t := range f.tabs {
		if !keep[i] {
			f.fill(t, fresh)
		}
	}
}

// drafts are the unsaved edits as configs, by tab index, for carrying
// them across a rebuild. Edits ApplyTo cannot express (a port that is not
// a number) fall back to the baseline value.
func (f *forms) drafts() map[int]Config {
	out := map[int]Config{}
	for _, t := range f.dirtyTabs() {
		cfg := *f.base
		_ = t.ApplyTo(&cfg) // best effort, see ApplyTo
		out[t.index] = cfg
	}
	return out
}

// restore fills the freshly built tabs: drafts where there were edits, the
// baseline elsewhere. A no-op before the first load.
func (f *forms) restore(drafts map[int]Config) {
	if f.base == nil {
		return
	}
	for _, t := range f.tabs {
		if d, ok := drafts[t.index]; ok {
			f.fill(t, d)
		} else {
			f.fill(t, *f.base)
		}
	}
}

// savedFallback is the baseline to adopt when the re-read after a
// successful save fails: what was sent, with the bot token masked the way
// the service would show it (a kept token keeps its old mask).
func savedFallback(sent Config, oldMasked string) Config {
	fresh := sent
	fresh.Telegram.BotToken = maskedAfterSave(sent.Telegram.BotToken, oldMasked)
	fresh.Telegram.BotTokenSet = fresh.Telegram.BotToken != ""
	return fresh
}

// --- UI side -----------------------------------------------------------------

// refreshDirty recomputes every form tab's Save button and "•" marker.
// UI goroutine only; a no-op while tabs are being filled.
func (u *ui) refreshDirty() {
	f := u.forms
	if f == nil || f.filling {
		return
	}
	for _, t := range f.tabs {
		d := f.isDirty(t)
		if t.bar != nil {
			t.bar.setDirty(d)
		}
		u.markTab(t.index, d)
	}
}

// adoptConfig makes fresh the baseline (see forms.adopt) and refreshes
// what shows the saved config outside the forms. UI goroutine only.
func (u *ui) adoptConfig(fresh Config, ref *Config) {
	u.forms.adopt(withGraceFallback(fresh), ref)
	u.onConfig(*u.forms.base)
	u.refreshDirty()
}

// onConfig updates the parts of the window that show the saved config but
// are not forms: the command tab's preset buttons and the SmartThings
// section's "no secret" hint. The app's crash record follows the saved
// debug switch too (#133, debug.go).
func (u *ui) onConfig(cfg Config) {
	u.fillPresetButtons(cfg.Presets)
	u.setSTSecretHint(cfg.Secret == "")
	setDebugMode(cfg.Debug)
}

// saveResult is what the background part of a save hands back.
type saveResult struct {
	msg string
	// warnings are the service's preset findings (C6).
	warnings []PresetWarning
	fresh    Config
	// reloginErr is set when the secret changed and logging in with the
	// new one failed.
	reloginErr error
}

// saveForms is the one save path: merge the tabs over the baseline, ask
// when a routine's slot would change owner, POST, log in again if the
// secret changed, re-read and adopt the result. Every Save button is busy
// meanwhile. quiet skips the "Saved" dialog (the unsaved-changes prompt).
// onDone (may be nil) learns whether it worked. UI goroutine only.
func (u *ui) saveForms(tabs []*formTab, quiet bool, onDone func(ok bool)) {
	f := u.forms
	done := func(ok bool) {
		if onDone != nil {
			onDone(ok)
		}
	}
	if f.base == nil || f.saving || len(tabs) == 0 {
		done(false)
		return
	}
	sent, err := f.merge(tabs)
	if err != nil {
		dialog.ShowError(err, u.win)
		done(false)
		return
	}
	if changes := configSlotChanges(*f.base, sent); len(changes) > 0 {
		u.confirmSlotChanges(changes, func(ok bool) {
			if !ok {
				done(false)
				return
			}
			u.postForms(sent, quiet, done)
		})
		return
	}
	u.postForms(sent, quiet, done)
}

// wireConfig is what a save posts for sent. Without the local session
// (C5) the presets came masked and the service refuses any change to them
// or to the watch list, so both travel as null: "keep the stored ones".
func wireConfig(sent Config, locked bool) Config {
	if locked {
		sent.Presets = nil
		sent.Activity.Watch = nil
	}
	return sent
}

// saveError is a failed save in the user's words.
func (u *ui) saveError(err error) error {
	if errors.Is(err, errLocalOnly) {
		return errors.New(u.t("localonly.note"))
	}
	return err
}

// postForms is the second half of saveForms: the request and adopting the
// result. UI goroutine only.
func (u *ui) postForms(sent Config, quiet bool, done func(ok bool)) {
	f := u.forms
	if f.base == nil || f.saving {
		done(false)
		return
	}
	wire := wireConfig(sent, u.editorsLocked())
	oldSecret, oldToken := f.base.Secret, f.base.Telegram.BotToken
	busy := func(on bool) {
		f.saving = on
		for _, t := range f.tabs {
			if t.bar != nil {
				t.bar.setBusy(on)
			}
		}
	}
	runAsync(busy, func() (saveResult, error) {
		// A session lost meanwhile (a service restart) is asked for again
		// once before the save fails (C5).
		reply, err := withLocalSession(u, func() (SaveReply, error) { return u.client.SaveConfig(wire) })
		if err != nil {
			return saveResult{}, err
		}
		res := saveResult{msg: reply.Message, warnings: reply.Warnings}
		// A new secret (notably the first one) leaves this client without
		// a valid session; get one now rather than letting the re-read or
		// the next poll hit a 401 and pop the login dialog (#98). The local
		// session first: a secret session would mask the presets (C5).
		if shouldReloginAfterSave(oldSecret, sent.Secret) {
			if u.trust() != trustLocal || u.client.LocalLogin() != nil {
				res.reloginErr = u.client.Login(sent.Secret)
			}
		}
		if res.fresh, err = u.client.GetConfig(); err != nil {
			res.fresh = savedFallback(sent, oldToken)
		}
		return res, nil
	}, func(res saveResult, err error) {
		if err != nil {
			dialog.ShowError(u.saveError(err), u.win)
			done(false)
			return
		}
		u.adoptConfig(res.fresh, &sent)
		u.showPresetWarnings(res.warnings)
		if !quiet {
			msg := u.savedMessage(res.msg)
			if lines := presetWarningLines(u.lang, res.warnings); len(lines) > 0 {
				msg = strings.TrimSpace(msg + "\n\n" + strings.Join(lines, "\n\n"))
			}
			dialog.ShowInformation(u.t("settings.saved"), msg, u.win)
		}
		if res.reloginErr != nil {
			u.promptLogin(func() { go u.initialLoad() })
		}
		done(true)
	})
}

// savedMessage localises the service's English save confirmation: it only
// ever says "Settings saved." or adds that port/remote-access changes need
// a service restart.
func (u *ui) savedMessage(serviceMsg string) string {
	if strings.Contains(serviceMsg, "Restart service") {
		return u.t("settings.saved.restart")
	}
	return u.t("settings.saved.body")
}

// saveTab is the Save button of one tab.
func (u *ui) saveTab(t *formTab) { u.saveForms([]*formTab{t}, false, nil) }
