package ui

// 공유: what this PC may tell SmartThings (and Telegram) about the people
// using it (#128) — the session's lock and idle state and who is signed in,
// what is playing (#117) and which watched programs run (#110, #123). All
// of it is opt-in. The model is gui.ShareForm (gui/export_sharing.go).
//
// The watch list: up to five entries, each in a numbered slot (the
// priority, and the routine condition "감시 N" on the PC device). A row's
// slot pill opens the free slots as chips under it. [실행 중인 프로그램에서
// 고르기] asks the service for the running .exe names (GET /api/processes,
// the local trusted session only — C5) and shows them in a panel inside
// the card; those names stay in that panel, only a picked one becomes an
// entry. Without the local session (u.editorsLocked) the list is read-only
// under a banner, and the save sends it as "keep" (gui.WireConfig); the
// detection switch stays usable. A save that empties or moves a slot asks
// first (the form framework's slot question).

import (
	"fmt"
	"slices"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"golang.org/x/exp/shiny/materialdesign/icons"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

func init() {
	iconData["apps"] = icons.NavigationApps
	iconData["person"] = icons.SocialPerson
}

// pickerShown caps the picker's rows; the search narrows the rest.
const pickerShown = 40

// slotPillW is the slot pill's width, so the fields of every row line up.
const slotPillW unit.Dp = 80

type sharePage struct {
	// form is the page as plain values, read from the widgets at the top
	// of body; fill, dirty and apply work on it only.
	form gui.ShareForm

	session, user, nowPlaying, activity widget.Bool

	// watch is the edited list in slot order; the editors hold its texts
	// (read back each frame) and are re-synced when rows move.
	watch     []gui.ActivityWatch
	procEd    []widget.Editor
	labelEd   []widget.Editor
	slotBtn   []widget.Clickable
	slotOpen  int // the row whose slot chips are open, -1 for none
	slotChips chipGroup
	list      listEditor
	pickBtn   widget.Clickable
	webui     widget.Clickable

	picker picker
}

// picker is the running-program panel.
type picker struct {
	open, loading bool
	names         []string // what the service listed; kept only while open
	search        widget.Editor
	close         widget.Clickable
	rows          []widget.Clickable
}

func newSharingPage() screen { return &sharePage{slotOpen: -1} }

func (p *sharePage) title(u *UI) string { return u.t("tab.share") }

// shareStatus is the sharing state for the hero card and the home row.
func (u *UI) shareStatus(s snapshot) (hero, string) {
	n := s.shareCount()
	if n == 0 {
		return hero{icon: "share", tone: toneOff, headline: u.t("home.share.none"), sub: u.t("ui.sharing.hero.sub")}, u.t("ui.conn.off")
	}
	return hero{icon: "share", tone: toneAccent, headline: fmt.Sprintf(u.t("home.share.some"), n), sub: u.t("ui.sharing.hero.sub")},
		fmt.Sprintf(u.t("ui.sharing.count"), n)
}

// --- form ----------------------------------------------------------------------

// fill puts cfg into the widgets.
func (p *sharePage) fill(_ *UI, cfg gui.Config) {
	p.form = gui.NewShareForm(cfg)
	p.session.Value = p.form.ExposeSession
	p.user.Value = p.form.ExposeSessionUser
	p.nowPlaying.Value = p.form.NowPlaying
	p.activity.Value = p.form.Activity.Enabled
	p.watch = gui.SortActivity(p.form.Activity.Watch)
	p.slotOpen = -1
	p.syncEditors()
	p.read()
}

// syncEditors sizes the row widgets to p.watch and writes its texts into
// them: after fill and whenever rows are added, removed or reordered.
func (p *sharePage) syncEditors() {
	n := len(p.watch)
	for len(p.procEd) < n {
		p.procEd = append(p.procEd, widget.Editor{SingleLine: true})
		p.labelEd = append(p.labelEd, widget.Editor{SingleLine: true})
	}
	if len(p.slotBtn) < n {
		p.slotBtn = append(p.slotBtn, make([]widget.Clickable, n-len(p.slotBtn))...)
	}
	for i, w := range p.watch {
		if p.procEd[i].Text() != w.Process {
			p.procEd[i].SetText(w.Process)
		}
		if p.labelEd[i].Text() != w.Label {
			p.labelEd[i].SetText(w.Label)
		}
	}
}

// readEditors copies the typed texts into p.watch.
func (p *sharePage) readEditors() {
	for i := range p.watch {
		if i < len(p.procEd) {
			p.watch[i].Process = p.procEd[i].Text()
			p.watch[i].Label = p.labelEd[i].Text()
		}
	}
}

// read copies the widgets into the form.
func (p *sharePage) read() {
	p.readEditors()
	p.form.ExposeSession = p.session.Value
	p.form.ExposeSessionUser = p.user.Value
	p.form.NowPlaying = p.nowPlaying.Value
	p.form.Activity = gui.ActivityConfig{Enabled: p.activity.Value, Watch: slices.Clone(p.watch)}
}

func (p *sharePage) dirty(_ *UI, base gui.Config) bool { return p.form.Dirty(base) }

func (p *sharePage) apply(u *UI, cfg *gui.Config) error { return p.form.ApplyTo(cfg, u.language()) }

// setWatch replaces the list after a structural change.
func (p *sharePage) setWatch(watch []gui.ActivityWatch) {
	p.watch = watch
	p.slotOpen = -1
	p.syncEditors()
}

// --- events --------------------------------------------------------------------

// update handles this frame's clicks, before anything is laid out.
func (p *sharePage) update(gtx C, u *UI, locked bool) {
	p.readEditors()
	p.session.Update(gtx)
	if p.user.Update(gtx) && !p.session.Value {
		p.user.Value = !p.user.Value // greyed while session info is off
	}
	p.nowPlaying.Update(gtx)
	p.activity.Update(gtx)
	if p.webui.Clicked(gtx) {
		u.open(pageService)
	}
	if locked {
		p.slotOpen = -1
		if p.picker.open {
			p.picker.reset()
		}
		p.read()
		return
	}
	if i := p.list.Deleted(gtx, len(p.watch)); i >= 0 {
		p.setWatch(slices.Delete(slices.Clone(p.watch), i, i+1))
	}
	if p.list.Added(gtx) {
		if w, ok := gui.AddActivityRow(p.watch); ok {
			p.setWatch(w)
		}
	}
	for i := range min(len(p.watch), len(p.slotBtn)) {
		if p.slotBtn[i].Clicked(gtx) {
			if p.slotOpen == i {
				p.slotOpen = -1
			} else {
				p.slotOpen = i
			}
		}
	}
	if i := p.slotOpen; i >= 0 && i < len(p.watch) {
		choices := gui.ActivitySlotChoices(p.watch, i)
		if k := p.slotChips.Clicked(gtx, len(choices)); k >= 0 {
			if w, ok := gui.SetActivitySlot(p.watch, i, choices[k]); ok {
				p.setWatch(w)
			} else {
				p.slotOpen = -1 // its own slot: just close
			}
		}
	}
	if p.pickBtn.Clicked(gtx) {
		if p.picker.open {
			p.picker.reset()
		} else {
			p.openPicker(u)
		}
	}
	if p.picker.close.Clicked(gtx) {
		p.picker.reset()
	}
	if p.picker.open && !p.picker.loading {
		shown := p.candidates()
		p.picker.grow(len(shown))
		for k, name := range shown {
			if p.picker.rows[k].Clicked(gtx) {
				if w, ok := gui.AddActivityProcess(p.watch, name); ok {
					p.setWatch(w)
				}
				p.picker.reset()
				break
			}
		}
	}
	p.read()
}

// --- picker --------------------------------------------------------------------

func (pk *picker) reset() {
	pk.open, pk.loading, pk.names = false, false, nil
	pk.search.SetText("")
}

func (pk *picker) grow(n int) {
	if len(pk.rows) < n {
		pk.rows = append(pk.rows, make([]widget.Clickable, n-len(pk.rows))...)
	}
}

// candidates is what the picker offers now: the running names not on the
// list, narrowed by the search.
func (p *sharePage) candidates() []string {
	return gui.PickerCandidates(p.picker.names, p.watch, p.picker.search.Text())
}

// openPicker opens the panel and fetches the running programs off the
// window goroutine.
func (p *sharePage) openPicker(u *UI) {
	u.clearNotice(p)
	p.picker.reset()
	p.picker.open, p.picker.loading = true, true
	go func() {
		names, err := withLocalSession(u, u.api.RunningProcesses)
		u.onWindow(func() { p.gotProcesses(u, names, err) })
	}()
}

// gotProcesses shows the fetched names, or says why there are none.
// Window goroutine.
func (p *sharePage) gotProcesses(u *UI, names []string, err error) {
	if !p.picker.open {
		return // closed meanwhile
	}
	switch {
	case gui.IsLocalOnly(err):
		p.picker.reset()
		u.setNotice(p, noticeError, u.t("localonly.note"))
	case err != nil:
		p.picker.reset()
		u.setNotice(p, noticeError, fmt.Sprintf(u.t("activity.pick.fail"), err))
	default:
		p.picker.loading = false
		p.picker.names = names
	}
}

// --- layout --------------------------------------------------------------------

func (p *sharePage) body(gtx C, u *UI, s snapshot) D {
	locked := u.editorsLocked()
	p.update(gtx, u, locked)
	th := u.th
	hr, _ := u.shareStatus(s)
	return sections(gtx,
		func(gtx C) D { return heroCard(gtx, th, hr, nil) },
		section(th, u.t("share.session"), func(gtx C) D {
			return rows(gtx,
				func(gtx C) D {
					return toggleRowIcon(gtx, th, &p.session, "lock", toneAccent, u.t("st.session"), u.t("ui.sharing.session.hint"))
				},
				func(gtx C) D {
					defer dim(gtx, !p.session.Value)()
					return toggleRowIcon(gtx, th, &p.user, "person", toneOff, u.t("st.session.user"), u.t("ui.sharing.user.hint"))
				},
			)
		}),
		section(th, u.t("share.media"), func(gtx C) D {
			return toggleRowIcon(gtx, th, &p.nowPlaying, "music", toneAccent, u.t("settings.nowplaying"), u.t("ui.sharing.nowplaying.hint"))
		}),
		section(th, u.t("share.activity"), func(gtx C) D {
			return toggleRowIcon(gtx, th, &p.activity, "apps", toneAccent, u.t("activity.toggle"), u.t("ui.sharing.activity.hint"))
		}),
		section(th, u.t("activity.watch"), func(gtx C) D { return p.watchCard(gtx, u, locked) }),
		section(th, u.t("ui.sharing.browser"), func(gtx C) D {
			return listRow(gtx, th, &p.webui, u.t("ui.service.webui"), u.t("ui.service.webui.hint"), u.onOff(s.cfg.WebUIRemote))
		}),
	)
}

// watchCard is the watch list: the priority note, the rows, the add and
// pick buttons, the picker panel and the rules under them.
func (p *sharePage) watchCard(gtx C, u *UI, locked bool) D {
	th := u.th
	children := []layout.FlexChild{}
	if locked {
		children = append(children,
			layout.Rigid(func(gtx C) D { return banner(gtx, th, noticeWarn, u.lockedNote()) }),
			vspace(gapS))
	}
	children = append(children,
		layout.Rigid(label(th, textS, u.t("ui.sharing.watch.priority"), colSub, font.Normal).Layout),
		vspace(gapS))
	if locked {
		children = append(children, layout.Rigid(func(gtx C) D { return p.lockedRows(gtx, u) }))
	} else {
		children = append(children,
			layout.Rigid(func(gtx C) D {
				return p.list.Layout(gtx, th, len(p.watch), u.t("activity.empty"), "", func(gtx C, i int) D { return p.watchRow(gtx, u, i) })
			}),
			vspace(gapS),
			layout.Rigid(func(gtx C) D { return p.buttons(gtx, u) }),
		)
		if p.picker.open {
			children = append(children, vspace(gapS), layout.Rigid(func(gtx C) D { return p.pickerPanel(gtx, u) }))
		}
	}
	children = append(children,
		vspace(gapS),
		layout.Rigid(label(th, textS, u.t("ui.sharing.watch.hint"), colFaint, font.Normal).Layout))
	return column(gtx, children...)
}

// slotPill is a row's slot: "감시 1", tappable to change it.
func (p *sharePage) slotPill(gtx C, u *UI, i int) D {
	gtx.Constraints.Min.X = gtx.Dp(slotPillW)
	gtx.Constraints.Max.X = gtx.Constraints.Min.X
	b := smallButton(u.th, &p.slotBtn[i], fmt.Sprintf(u.t("activity.slot"), p.watch[i].Slot), colAccent)
	b.Background = colAccentDim
	b.Font.Weight = font.SemiBold
	if p.slotOpen == i {
		b.Background, b.Color = colAccent, colOnAccent
	}
	return b.Layout(gtx)
}

// watchRow is one entry: the slot pill, the file name and the label (side
// by side when there is room, else the label under the name), and the
// slot chips while the pill is open.
func (p *sharePage) watchRow(gtx C, u *UI, i int) D {
	th := u.th
	proc := field(th, &p.procEd[i], "steam.exe")
	lab := field(th, &p.labelEd[i], u.t("activity.ph.label"))
	pill := func(gtx C) D { return p.slotPill(gtx, u, i) }
	wide := gtx.Constraints.Max.X >= gtx.Dp(440)
	children := []layout.FlexChild{}
	if wide {
		children = append(children, layout.Rigid(func(gtx C) D {
			return row(gtx, layout.Rigid(pill), hspace(gapXS), layout.Flexed(1, proc), hspace(gapXS), layout.Flexed(1, lab))
		}))
	} else {
		children = append(children,
			layout.Rigid(func(gtx C) D { return row(gtx, layout.Rigid(pill), hspace(gapXS), layout.Flexed(1, proc)) }),
			vspace(gapXS),
			layout.Rigid(func(gtx C) D { return layout.Inset{Left: slotPillW + gapXS}.Layout(gtx, lab) }),
		)
	}
	if p.slotOpen == i {
		children = append(children, vspace(gapXS), layout.Rigid(func(gtx C) D {
			choices := gui.ActivitySlotChoices(p.watch, i)
			labels := make([]string, len(choices))
			sel := -1
			for k, s := range choices {
				labels[k] = fmt.Sprintf(u.t("activity.slot"), s)
				if s == p.watch[i].Slot {
					sel = k
				}
			}
			return layout.Inset{Left: slotPillW + gapXS}.Layout(gtx, func(gtx C) D { return p.slotChips.Layout(gtx, th, labels, sel) })
		}))
	}
	return column(gtx, children...)
}

// lockedRows shows the list read-only: slot, label and file name.
func (p *sharePage) lockedRows(gtx C, u *UI) D {
	th := u.th
	if len(p.watch) == 0 {
		return label(th, textM, u.t("activity.empty"), colSub, font.Normal).Layout(gtx)
	}
	ws := make([]layout.Widget, len(p.watch))
	for i, w := range p.watch {
		ws[i] = func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Dp(slotPillW)
					return statusPill(th, fmt.Sprintf(u.t("activity.slot"), w.Slot), toneOff)(gtx)
				}),
				hspace(gapXS),
				layout.Flexed(1, titled(th, w.Label, w.Process)),
			)
		}
	}
	return rows(gtx, ws...)
}

// buttons are [+ 추가] and [실행 중인 프로그램에서 고르기], or a note when
// every slot is taken.
func (p *sharePage) buttons(gtx C, u *UI) D {
	th := u.th
	if len(p.watch) >= gui.ActivityMaxWatch {
		return label(th, textS, u.t("ui.sharing.watch.full"), colSub, font.Normal).Layout(gtx)
	}
	return wrap(gtx, gapXS, gapXS,
		smallButton(th, &p.list.add, "+ "+u.t("activity.add"), colAccent).Layout,
		func(gtx C) D {
			b := smallButton(th, &p.pickBtn, u.t("activity.pick"), colAccent)
			if p.picker.open {
				b.Background = colAccentDim
			}
			return b.Layout(gtx)
		},
	)
}

// pickerPanel is the running-program list inside the card: a title with
// a close button, the search box, the privacy note and the names.
func (p *sharePage) pickerPanel(gtx C, u *UI) D {
	th := u.th
	pk := &p.picker
	return surface(gtx, colBg, radiusM, func(gtx C) D {
		return layout.UniformInset(gapS).Layout(gtx, func(gtx C) D {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D { return drawIcon(gtx, "search", 18, colSub) }),
						hspace(8),
						layout.Flexed(1, label(th, textL, u.t("activity.pick.title"), colText, font.SemiBold).Layout),
						layout.Rigid(func(gtx C) D { return iconButton(gtx, &pk.close, "close", 30, colSub, colGhost) }),
					)
				}),
				vspace(gapXS),
				layout.Rigid(label(th, textS, u.t("ui.sharing.pick.hint"), colSub, font.Normal).Layout),
				vspace(gapS),
			}
			if pk.loading {
				children = append(children, layout.Rigid(label(th, textM, u.t("ui.sharing.pick.loading"), colSub, font.Normal).Layout))
				return column(gtx, children...)
			}
			children = append(children,
				layout.Rigid(func(gtx C) D {
					return surface(gtx, colCard, radiusM, field(th, &pk.search, u.t("activity.pick.search")))
				}),
				vspace(gapS))
			shown := p.candidates()
			if len(shown) == 0 {
				children = append(children, layout.Rigid(label(th, textM, u.t("activity.pick.none"), colSub, font.Normal).Layout))
				return column(gtx, children...)
			}
			more := 0
			if len(shown) > pickerShown {
				more = len(shown) - pickerShown
				shown = shown[:pickerShown]
			}
			pk.grow(len(shown))
			ws := make([]layout.Widget, len(shown))
			for k, name := range shown {
				ws[k] = func(gtx C) D { return pickRow(gtx, th, &pk.rows[k], name) }
			}
			children = append(children, layout.Rigid(func(gtx C) D { return rows(gtx, ws...) }))
			if more > 0 {
				children = append(children, vspace(gapS),
					layout.Rigid(label(th, textS, fmt.Sprintf(u.t("ui.sharing.pick.more"), more), colFaint, font.Normal).Layout))
			}
			return column(gtx, children...)
		})
	})
}
