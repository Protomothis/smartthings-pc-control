package ui

// 업데이트: the running version against the newest release, the release
// notes since this version, and the update itself with its progress — on
// one page (the Fyne app had dialogs). The shell
// checks GitHub at start and daily (shell.go: snapshot.updateCheck, the
// badge on the home update row); this page starts from that, checks again
// when it knows nothing yet or on [지금 확인], and drives the update
// through an updater, so the installer of #166 can bring its own path.

import (
	"context"
	"fmt"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// updater is how the page checks, plans and applies an update. selfUpdater
// is today's path (signed manifest, verified download, elevated swap);
// tests use fakes and never download anything.
type updater interface {
	// check asks for the newest release. Network: off the window goroutine.
	check() (gui.UpdateCheck, error)
	// prepare decides what can be done about chk for this version: apply
	// in place, or only open the release page. Network too.
	prepare(chk gui.UpdateCheck, version string) gui.UpdatePlan
	// apply downloads, verifies and hands over to whatever installs the
	// update; nil means the app should quit now. progress is called from
	// apply's goroutine.
	apply(ctx context.Context, plan gui.UpdatePlan, progress func(gui.UpdateProgress)) error
}

// selfUpdater is the exe replacing itself (gui/export_update.go).
type selfUpdater struct{}

func (selfUpdater) check() (gui.UpdateCheck, error) { return gui.CheckLatestRelease() }

func (selfUpdater) prepare(chk gui.UpdateCheck, version string) gui.UpdatePlan {
	return gui.PrepareUpdate(chk, version)
}

func (selfUpdater) apply(ctx context.Context, plan gui.UpdatePlan, progress func(gui.UpdateProgress)) error {
	return gui.ApplySelfUpdate(ctx, plan, progress)
}

// recheckAfter is how old the page's knowledge may be before opening it
// checks again by itself.
const recheckAfter = 10 * time.Minute

type updatePage struct {
	upd updater

	checkBtn, primary, skip, page, more, cancel widget.Clickable
	auto                                        widget.Bool
	autoSet                                     bool

	// Window goroutine only.
	checking  bool
	checkErr  error
	latest    gui.UpdateCheck // the newest release known, own check or the shell's
	checkedAt time.Time       // of the page's own check
	notesTag  string          // the tag p.notes were read for
	notes     []noteRelease
	moreNotes bool // more releases than MaxNotes

	planTag  string // the tag p.plan is for; "" while none
	planning bool
	plan     gui.UpdatePlan

	running  bool
	prog     gui.UpdateProgress
	stopping context.CancelFunc
}

func newUpdatePage() screen { return &updatePage{upd: selfUpdater{}} }

func (p *updatePage) title(u *UI) string { return u.t("update.manual") }

// shown takes the shell's answer and checks again when the page knows
// nothing recent.
func (p *updatePage) shown(u *UI, s snapshot) {
	p.adopt(s)
	if !p.checking && !p.running && (!p.latest.Valid() || (!p.checkedAt.IsZero() && time.Since(p.checkedAt) > recheckAfter)) {
		p.startCheck(u)
	}
}

// adopt takes the shell's check when it knows a newer release than the
// page.
func (p *updatePage) adopt(s snapshot) {
	c := s.updateCheck
	if !c.Valid() {
		return
	}
	if !p.latest.Valid() || gui.IsNewerRelease(p.latest.Tag(), c.Tag()) {
		p.latest = c
	}
}

// startCheck asks for the newest release; the answer also updates the home
// badge. Window goroutine.
func (p *updatePage) startCheck(u *UI) {
	p.checking, p.checkErr = true, nil
	upd := p.upd
	go func() {
		chk, err := upd.check()
		if err == nil {
			tag := chk.Tag()
			u.update(func(s *snapshot) {
				if gui.IsNewerRelease(u.version, tag) {
					s.updateTag, s.updateCheck = tag, chk
				} else {
					s.updateTag = ""
				}
			})
		}
		u.onWindow(func() {
			p.checking = false
			if err != nil {
				p.checkErr = err
				return
			}
			p.latest, p.checkedAt = chk, time.Now()
		})
	}()
}

// offered reports whether the newest release is something to install: a
// newer version, or any release for a dev build (which may only open the
// release page).
func (p *updatePage) offered(u *UI) bool {
	if !p.latest.Valid() {
		return false
	}
	return !gui.CanSelfUpdate(u.version) || gui.IsNewerRelease(u.version, p.latest.Tag())
}

// syncPlan reads the release's manifest once per offered tag. Window
// goroutine.
func (p *updatePage) syncPlan(u *UI) {
	if !p.offered(u) || p.planning {
		return
	}
	tag := p.latest.Tag()
	if p.planTag == tag {
		return
	}
	p.planning = true
	chk, upd, version := p.latest, p.upd, u.version
	go func() {
		plan := upd.prepare(chk, version)
		u.onWindow(func() {
			p.planning = false
			p.plan, p.planTag = plan, tag
		})
	}()
}

// syncNotes reads the release notes again when the newest release changed.
func (p *updatePage) syncNotes(u *UI) {
	if !p.latest.Valid() || p.notesTag == p.latest.Tag() {
		return
	}
	rels := gui.UpdateNotes(p.latest, u.version)
	p.notes = noteReleases(rels, gui.MaxNotes)
	p.moreNotes = len(rels) > gui.MaxNotes
	p.notesTag = p.latest.Tag()
}

// skipped reports whether the newest release is the one the user skips.
func (p *updatePage) skipped(u *UI) bool {
	return p.latest.Valid() && u.prefs.String(gui.PrefUpdateSkipped, "") == p.latest.Tag()
}

// askUpdate confirms, then starts the update.
func (p *updatePage) askUpdate(u *UI) {
	u.ask(askSpec{
		title: u.t("ui.update.ask.title"),
		body:  u.t("update.applying"),
		ok:    u.t("update.now"),
		onOK:  func() { p.startUpdate(u) },
	})
}

// startUpdate runs the update with its progress on the page. On success
// the installer waits for this process: the app quits. Window goroutine.
func (p *updatePage) startUpdate(u *UI) {
	if p.running || !p.plan.CanApply() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.running, p.stopping, p.prog = true, cancel, gui.UpdateProgress{Total: -1}
	u.clearNotice(p)
	plan, upd := p.plan, p.upd
	go func() {
		err := upd.apply(ctx, plan, func(pr gui.UpdateProgress) {
			u.onWindow(func() { p.prog = pr })
		})
		cancelled := ctx.Err() != nil
		cancel()
		u.onWindow(func() {
			p.running, p.stopping = false, nil
			switch {
			case err == nil:
				if u.quit != nil {
					go u.quit()
				}
			case !cancelled: // a cancel is not worth a banner
				u.setNotice(p, noticeError, gui.UpdateFailedText(u.language(), err))
			}
		})
	}()
}

// hero is the page's state at a glance.
func (p *updatePage) hero(u *UI) hero {
	cur := u.version
	if cur == "" {
		cur = "dev"
	}
	h := hero{icon: "update", tone: toneOff, sub: fmt.Sprintf(u.t("ui.update.current"), cur)}
	switch {
	case p.running:
		h.tone, h.headline = toneAccent, u.t("ui.update.hero.running")
		h.sub = fmt.Sprintf(u.t("ui.update.to"), cur, p.latest.Tag())
	case p.checking && !p.latest.Valid():
		h.headline = u.t("ui.update.hero.checking")
	case p.checkErr != nil:
		h.icon, h.tone, h.headline = "error", toneBad, u.t("ui.update.hero.failed")
	case !p.latest.Valid():
		h.headline = u.t("ui.update.hero.unknown")
	case !gui.CanSelfUpdate(u.version):
		h.headline = u.t("ui.update.hero.dev")
		h.sub = fmt.Sprintf(u.t("ui.update.latest"), p.latest.Tag())
	case gui.IsNewerRelease(u.version, p.latest.Tag()):
		h.tone, h.headline = toneAccent, fmt.Sprintf(u.t("ui.update.hero.new"), p.latest.Tag())
		if p.skipped(u) {
			h.sub += " · " + u.t("ui.update.skipped.short")
		}
	default:
		h.icon, h.tone, h.headline = "ok", toneOK, u.t("ui.update.hero.uptodate")
		h.sub = fmt.Sprintf(u.t("ui.update.using"), cur)
	}
	return h
}

func (p *updatePage) body(gtx C, u *UI, s snapshot) D {
	if !p.autoSet {
		p.auto.Value, p.autoSet = u.prefs.Bool(prefCheckUpdates, true), true
	}
	if p.auto.Update(gtx) {
		u.prefs.Set(prefCheckUpdates, p.auto.Value)
	}
	p.adopt(s)
	if p.checkBtn.Clicked(gtx) && !p.checking && !p.running {
		p.startCheck(u)
	}
	p.syncPlan(u)
	p.syncNotes(u)
	planned := p.planTag != "" && p.latest.Valid() && p.planTag == p.latest.Tag() && !p.planning
	if p.primary.Clicked(gtx) && planned && !p.running {
		if p.plan.CanApply() {
			p.askUpdate(u)
		} else {
			_ = openURL(p.plan.Page)
		}
	}
	if p.page.Clicked(gtx) && p.latest.Valid() {
		_ = openURL(p.latest.Page())
	}
	if p.more.Clicked(gtx) {
		_ = openURL(release.Page)
	}
	if p.skip.Clicked(gtx) && p.latest.Valid() {
		if p.skipped(u) {
			u.prefs.Set(gui.PrefUpdateSkipped, "")
		} else {
			u.prefs.Set(gui.PrefUpdateSkipped, p.latest.Tag())
		}
	}
	if p.cancel.Clicked(gtx) && p.stopping != nil {
		p.stopping()
	}

	th := u.th
	blocks := []layout.Widget{
		func(gtx C) D { return heroCard(gtx, th, p.hero(u), p.checkButton(u)) },
	}
	if p.checkErr != nil && !p.checking {
		msg := u.t("update.checkfailed") + p.checkErr.Error()
		blocks = append(blocks, func(gtx C) D { return banner(gtx, th, noticeError, msg) })
	}
	switch {
	case p.running:
		blocks = append(blocks, section(th, "", func(gtx C) D { return p.progressCard(gtx, u) }))
	case p.offered(u):
		blocks = append(blocks, section(th, "", func(gtx C) D { return p.offerCard(gtx, u, planned) }))
	}
	if p.latest.Valid() {
		blocks = append(blocks, section(th, u.t("ui.update.notes"), func(gtx C) D { return p.notesCard(gtx, u) }))
	}
	blocks = append(blocks, section(th, u.t("ui.update.settings"), func(gtx C) D {
		return toggleRow(gtx, th, &p.auto, u.t("update.check"), u.t("ui.update.auto.hint"))
	}))
	return sections(gtx, blocks...)
}

// checkButton is the hero's [지금 확인]; it says it is checking meanwhile
// and is gone during an update.
func (p *updatePage) checkButton(u *UI) layout.Widget {
	if p.running {
		return nil
	}
	return func(gtx C) D {
		if p.checking {
			return smallButton(u.th, &p.checkBtn, u.t("ui.update.checkingbtn"), colSub).Layout(gtx)
		}
		return smallButton(u.th, &p.checkBtn, u.t("ui.update.checknow"), colAccent).Layout(gtx)
	}
}

// offerCard is what can be done about the newest release: update now, or
// why not and its download page; skip it; open its page.
func (p *updatePage) offerCard(gtx C, u *UI, planned bool) D {
	th := u.th
	children := []layout.FlexChild{}
	add := func(w layout.Widget) {
		if len(children) > 0 {
			children = append(children, vspace(gapS))
		}
		children = append(children, layout.Rigid(w))
	}
	switch {
	case !planned:
		add(func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "refresh", 16, colSub) }),
				hspace(6),
				layout.Flexed(1, label(th, textM, u.t("ui.update.preparing"), colSub, font.Normal).Layout),
			)
		})
	case p.plan.CanApply():
		add(fill(primaryButton(th, &p.primary, u.t("update.now")).Layout))
	default:
		if p.plan.Block == gui.UpdateDevBuild {
			add(func(gtx C) D { return banner(gtx, th, noticeInfo, u.t("ui.update.dev.note")) })
			add(fill(secondaryButton(th, &p.primary, u.t("update.releasepage")).Layout))
		} else {
			add(func(gtx C) D { return banner(gtx, th, noticeWarn, p.plan.Note(u.language())) })
			add(fill(secondaryButton(th, &p.primary, u.t("update.open")).Layout))
		}
	}
	dev := !gui.CanSelfUpdate(u.version)
	if !dev {
		skipTxt := u.t("update.skip")
		if p.skipped(u) {
			skipTxt = u.t("update.unskip")
		}
		add(func(gtx C) D {
			return wrap(gtx, gapXS, gapXS,
				smallButton(th, &p.skip, skipTxt, colText).Layout,
				func(gtx C) D {
					if planned && !p.plan.CanApply() {
						return D{} // the primary button opens the page already
					}
					return smallButton(th, &p.page, u.t("update.releasepage"), colAccent).Layout(gtx)
				},
			)
		})
		if p.skipped(u) {
			add(label(th, textS, u.t("update.skipped"), colSub, font.Normal).Layout)
		}
	}
	return column(gtx, children...)
}

// progressCard is the running update: what it does, how far, and [취소]
// until the installer takes over.
func (p *updatePage) progressCard(gtx C, u *UI) D {
	th := u.th
	frac := p.prog.Fraction()
	children := []layout.FlexChild{
		layout.Rigid(label(th, textM, p.prog.Text(u.language()), colText, font.Medium).Layout),
		vspace(gapS),
		layout.Rigid(func(gtx C) D {
			if frac < 0 {
				return usageBar(gtx, 0, colAccent)
			}
			return usageBar(gtx, frac, colAccent)
		}),
	}
	if p.prog.Stage != gui.StageApplying {
		children = append(children, vspace(gapM),
			layout.Rigid(fill(greyButton(th, &p.cancel, u.t("update.cancel")).Layout)))
	}
	return column(gtx, children...)
}

// notesCard is the release notes, newest first, and a link to the rest.
func (p *updatePage) notesCard(gtx C, u *UI) D {
	th := u.th
	ws := make([]layout.Widget, 0, len(p.notes)+1)
	for _, r := range p.notes {
		ws = append(ws, func(gtx C) D { return layoutNotes(gtx, th, r, u.t("update.nonotes")) })
	}
	if p.moreNotes {
		ws = append(ws, smallButton(th, &p.more, u.t("update.morenotes"), colAccent).Layout)
	}
	return rows(gtx, ws...)
}
