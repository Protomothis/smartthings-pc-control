package ui

// 프리셋: the actions SmartThings routines and Telegram /run start by slot
// number (#109). A form (forms.go): the
// rows are read from their widgets at the top of body; fill, dirty and
// apply work on those values, and the save asks about routine slots
// changing hands on its own.
//
// Each preset is one row of the card: its slot, name and target. A tap
// opens it in place (one at a time) to edit the name, kind, slot, path and
// arguments, browse for the file, test-run it as typed, or delete it.
// While the service refuses this process the local session (C5) nothing
// can be changed, and a banner says why.

import (
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

type presetsPage struct {
	rows []*presetEdit
	add  widget.Clickable
	// warnings are the service's findings of the last save (C6), shown
	// under the rows they are about.
	warnings []gui.PresetWarning
	// focus is a new row whose name field takes the focus next frame.
	focus *presetEdit
	// arrive is the row the home card opened (OpenSlot): the page scrolls
	// it to the top once the rows above it have been measured, and it
	// glows briefly so the eye lands on it.
	arrive *presetEdit
}

// presetEdit is one row of the editor.
type presetEdit struct {
	name, path, args widget.Editor
	slot             int
	typ              string
	open             bool

	head              widget.Clickable
	types             chipGroup
	slots             numberGrid
	browse, test, del widget.Clickable

	// status is the last test run's result, in statusTone.
	status     string
	statusTone tone
	testing    bool

	// link caches PresetLinkNote for linkFor (it reads the registry).
	linkFor, linkKey, linkApp string

	// h is the height of the row in the last frame (for the scroll of
	// OpenSlot); glowUntil ends the arrival highlight.
	h         int
	glowUntil time.Time
}

func newPresetsPage() screen { return &presetsPage{} }

func newPresetEdit(r gui.PresetRow) *presetEdit {
	e := &presetEdit{}
	for _, ed := range []*widget.Editor{&e.name, &e.path, &e.args} {
		ed.SingleLine = true
	}
	e.set(r)
	return e
}

func (p *presetsPage) title(u *UI) string { return u.t("tab.presets") }

// row is e as typed.
func (e *presetEdit) row() gui.PresetRow {
	return gui.PresetRow{Slot: e.slot, Name: e.name.Text(), Type: e.typ, Path: e.path.Text(), Args: e.args.Text()}
}

// set shows r, leaving fields that already say so alone (the caret stays).
func (e *presetEdit) set(r gui.PresetRow) {
	setText(&e.name, r.Name)
	setText(&e.path, r.Path)
	setText(&e.args, r.Args)
	e.slot, e.typ = r.Slot, r.Type
}

// state is every row as typed.
func (p *presetsPage) state() []gui.PresetRow {
	out := make([]gui.PresetRow, len(p.rows))
	for i, e := range p.rows {
		out[i] = e.row()
	}
	return out
}

// fill shows cfg's presets in slot order. A row whose slot was shown
// already keeps its widgets (open, caret, test result while unchanged).
func (p *presetsPage) fill(_ *UI, cfg gui.Config) {
	old := map[int]*presetEdit{}
	for _, e := range p.rows {
		old[e.slot] = e
	}
	rs := gui.PresetRows(cfg)
	rows := make([]*presetEdit, len(rs))
	for i, r := range rs {
		e, ok := old[r.Slot]
		switch {
		case !ok:
			e = newPresetEdit(r)
		case e.row() != r:
			e.set(r)
			e.status = ""
		}
		delete(old, r.Slot)
		rows[i] = e
	}
	p.rows = rows
}

// dirty: nothing is unsaved while the editor is locked, as a save would
// keep the stored presets anyway (gui.WireConfig).
func (p *presetsPage) dirty(u *UI, base gui.Config) bool {
	return !u.editorsLocked() && gui.PresetsDirty(p.state(), base)
}

func (p *presetsPage) apply(u *UI, cfg *gui.Config) error {
	return gui.ApplyPresets(u.language(), p.state(), cfg)
}

func (p *presetsPage) saved(_ *UI, reply gui.SaveReply) { p.warnings = reply.Warnings }

// --- editing -------------------------------------------------------------------

// addRow adds a program row in the lowest free slot, opened, its name
// focused; nil when all slots are taken or the editor is locked.
func (p *presetsPage) addRow(u *UI) *presetEdit {
	slot := gui.PresetFreeSlot(p.state())
	if slot == 0 || u.editorsLocked() {
		return nil
	}
	e := newPresetEdit(gui.PresetRow{Slot: slot, Type: "program"})
	p.rows = append(p.rows, e)
	p.openRow(e)
	p.focus = e
	return e
}

// StartNew opens the page with a new row ready to fill in: the home
// 프리셋 card's [+ 추가].
func (p *presetsPage) StartNew(u *UI) {
	u.open(pagePresets)
	p.addRow(u)
}

// OpenSlot opens the page on the preset in slot, opened, scrolled to and
// briefly highlighted: a tap on that preset in the home card. A slot that
// is not there (the config changed meanwhile) just opens the page.
func (p *presetsPage) OpenSlot(u *UI, slot int) {
	u.open(pagePresets)
	for _, e := range p.rows {
		if e.slot == slot {
			p.openRow(e)
			p.arrive = e
			e.glowUntil = time.Time{} // starts when it is on screen (scrollToArrival)
			return
		}
	}
}

// arrivalGlow is how long an arrived-at row stays highlighted.
const arrivalGlow = 1600 * time.Millisecond

// openRow opens e and closes the others.
func (p *presetsPage) openRow(e *presetEdit) {
	for _, x := range p.rows {
		x.open = x == e
	}
}

func (p *presetsPage) deleteRow(e *presetEdit) {
	p.rows = slices.DeleteFunc(p.rows, func(x *presetEdit) bool { return x == e })
}

// setType changes e's kind. A URL takes no arguments: they are cleared
// rather than kept hidden and still saved or checked.
func (e *presetEdit) setType(typ string) {
	e.typ = typ
	if typ == "url" {
		e.args.SetText("")
	}
}

// slotTaken reports whether a row other than e uses slot.
func (p *presetsPage) slotTaken(e *presetEdit, slot int) bool {
	return slices.ContainsFunc(p.rows, func(x *presetEdit) bool { return x != e && x.slot == slot })
}

// pickPath browses for e's program or script.
func (p *presetsPage) pickPath(u *UI, e *presetEdit) {
	filters := []fileFilter{{u.t("presets.type.program"), "*.exe;*.com"}}
	if e.typ == "script" {
		filters = []fileFilter{{u.t("presets.type.script"), "*.ps1;*.bat;*.cmd"}}
	}
	dir := ""
	if cur := strings.TrimSpace(e.path.Text()); cur != "" {
		dir = filepath.Dir(cur)
	}
	u.pickFile(u.t("presets.browse"), dir, filters, func(path string, err error) {
		if err == nil && path != "" {
			e.path.SetText(path)
		}
	})
}

// runTest runs e as typed via /api/presets/test, the result under it.
func (p *presetsPage) runTest(u *UI, e *presetEdit) {
	r := e.row()
	if strings.TrimSpace(r.Name) == "" {
		r.Name = "test"
	}
	if k := gui.PresetRowProblem(r); k != "" {
		e.status, e.statusTone = fmt.Sprintf(u.t(k), r.Slot), toneBad
		return
	}
	pr, _ := gui.PresetOf(r)
	e.status, e.statusTone, e.testing = u.t("presets.testing"), toneOff, true
	l := u.language()
	go func() {
		ws, err := withLocalSession(u, func() ([]gui.PresetWarning, error) { return u.api.TestPreset(pr) })
		u.onWindow(func() {
			e.testing = false
			switch {
			case err != nil:
				e.status, e.statusTone = gui.ActionErrorText(l, err), toneBad
			case len(ws) > 0:
				// Every warning is about this one preset, whatever slot the
				// service put in it.
				parts := []string{u.t("presets.started")}
				for _, w := range ws {
					parts = append(parts, gui.PresetWarningText(l, w))
				}
				e.status, e.statusTone = strings.Join(parts, "\n"), toneWarn
			default:
				e.status, e.statusTone = u.t("presets.started"), toneOK
			}
		})
	}()
}

// problem is what is wrong with row i as typed, in the user's words; ""
// when it looks fine.
func (p *presetsPage) problem(u *UI, i int, dup []bool) string {
	r := p.rows[i].row()
	if k := gui.PresetRowProblem(r); k != "" {
		return fmt.Sprintf(u.t(k), r.Slot)
	}
	if dup[i] {
		return fmt.Sprintf(u.t("presets.err.namedup"), r.Slot)
	}
	return ""
}

// linkNote is e's app-link line (#137) and whether it is a warning.
func (e *presetEdit) linkNote(u *UI) (string, bool) {
	if path := e.typ + "\x00" + e.path.Text(); path != e.linkFor {
		e.linkFor = path
		e.linkKey, e.linkApp = gui.PresetLinkNote(e.row())
	}
	switch e.linkKey {
	case "":
		return "", false
	case "presets.link.unknown":
		return u.t("presets.link.unknown"), true
	case "presets.link.app":
		return fmt.Sprintf(u.t("presets.link.app"), e.linkApp), false
	}
	return u.t("presets.link.registered"), false
}

// events handles this frame's clicks before anything is laid out.
func (p *presetsPage) events(gtx C, u *UI, locked bool) {
	if p.add.Clicked(gtx) && !locked {
		p.addRow(u)
	}
	types := gui.PresetTypes()
	var gone *presetEdit
	for _, e := range p.rows {
		for _, ed := range []*widget.Editor{&e.name, &e.path, &e.args} {
			ed.ReadOnly = locked
		}
		if e.head.Clicked(gtx) {
			if e.open {
				e.open = false
			} else {
				p.openRow(e)
			}
		}
		if i := e.types.Clicked(gtx, len(types)); i >= 0 && !locked {
			e.setType(types[i])
		}
		if s := e.slots.Clicked(gtx, gui.PresetMaxSlots); s > 0 && !locked && !p.slotTaken(e, s) {
			e.slot = s
		}
		if e.browse.Clicked(gtx) && !locked {
			p.pickPath(u, e)
		}
		if e.test.Clicked(gtx) && !locked && !e.testing {
			p.runTest(u, e)
		}
		if e.del.Clicked(gtx) && !locked {
			gone = e
		}
	}
	if gone != nil {
		p.deleteRow(gone)
	}
	if p.focus != nil {
		gtx.Execute(key.FocusCmd{Tag: &p.focus.name})
		p.focus = nil
	}
}

// --- layout -------------------------------------------------------------------

func (p *presetsPage) body(gtx C, u *UI, _ snapshot) D {
	locked := u.editorsLocked()
	p.events(gtx, u, locked)
	th := u.th
	dup := gui.PresetDuplicateNames(p.state())
	full := len(p.rows) >= gui.PresetMaxSlots

	blocks := []layout.Widget{}
	bannerH := 0
	if locked {
		blocks = append(blocks, func(gtx C) D {
			d := banner(gtx, th, noticeWarn, u.lockedNote())
			bannerH = d.Size.Y
			return d
		})
	}
	header := fmt.Sprintf(u.t("ui.presets.count"), len(p.rows), gui.PresetMaxSlots)
	defer p.scrollToArrival(gtx, u, header, &bannerH)
	blocks = append(blocks,
		func(gtx C) D {
			return column(gtx,
				layout.Rigid(section(th, header, func(gtx C) D {
					children := []layout.FlexChild{}
					if len(p.rows) == 0 {
						children = append(children, layout.Rigid(titled(th, u.t("home.presets.empty"), u.t("ui.presets.empty.hint"))))
					} else {
						ws := make([]layout.Widget, len(p.rows))
						for i, e := range p.rows {
							ws[i] = func(gtx C) D {
								d := p.layoutRow(gtx, u, e, p.problem(u, i, dup), locked)
								e.h = d.Size.Y
								return d
							}
						}
						children = append(children, layout.Rigid(func(gtx C) D { return rows(gtx, ws...) }))
					}
					if !locked {
						children = append(children, vspace(gapM), layout.Rigid(func(gtx C) D {
							if full {
								return label(th, textS, u.t("ui.presets.full"), colSub, font.Normal).Layout(gtx)
							}
							return smallButton(th, &p.add, "+ "+u.t("presets.add"), colAccent).Layout(gtx)
						}))
					}
					return column(gtx, children...)
				})),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 4, Right: 4, Top: 8}.Layout(gtx, label(th, textS, u.t("ui.presets.hint"), colSub, font.Normal).Layout)
				}),
			)
		},
	)
	return sections(gtx, blocks...)
}

// scrollToArrival scrolls the page so the arrived-at row (OpenSlot) is at
// the top, once every row above it has a height. The page is one block in
// the scrolling column, so the offset is added up from the pieces body
// lays out: the inset, the banner, the section header, the card inset and
// the rows above with their dividers.
func (p *presetsPage) scrollToArrival(gtx C, u *UI, header string, bannerH *int) {
	e := p.arrive
	if e == nil {
		return
	}
	y := gtx.Dp(pageInset.Top)
	if *bannerH > 0 {
		y += *bannerH + gtx.Dp(gapL)
	}
	m := op.Record(gtx.Ops) // measured, not drawn
	hd := label(u.th, textM, header, colSub, font.SemiBold).Layout(gtx)
	m.Stop()
	y += hd.Size.Y + gtx.Dp(8) + gtx.Dp(gapL)
	for _, x := range p.rows {
		if x == e {
			u.pageList.Position = layout.Position{Offset: max(y-gtx.Dp(gapS), 0)}
			e.glowUntil = gtx.Now.Add(arrivalGlow)
			gtx.Execute(op.InvalidateCmd{})
			p.arrive = nil
			return
		}
		if x.h == 0 {
			gtx.Execute(op.InvalidateCmd{}) // measured next frame
			return
		}
		y += x.h + 2*gtx.Dp(gapS) + max(gtx.Dp(1), 1)
	}
	p.arrive = nil // the row went away
}

// layoutRow draws one preset: its summary, and the editor under it while
// open.
func (p *presetsPage) layoutRow(gtx C, u *UI, e *presetEdit, problem string, locked bool) D {
	th := u.th
	warned := gui.PresetSlotWarnings(u.language(), p.warnings, e.slot)
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return e.head.Layout(gtx, func(gtx C) D {
				pointer.CursorPointer.Add(gtx.Ops)
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return arrivalGlowBehind(gtx, e.glowUntil, func(gtx C) D {
					return layout.Inset{Top: 2, Bottom: 2}.Layout(gtx, func(gtx C) D {
						return row(gtx,
							layout.Rigid(func(gtx C) D { return slotBadge(gtx, th, e.slot) }),
							hspace(gapS),
							layout.Flexed(1, func(gtx C) D { return p.summary(gtx, u, e) }),
							layout.Rigid(func(gtx C) D {
								if problem == "" && warned == "" {
									return D{}
								}
								return layout.Inset{Left: 8}.Layout(gtx, statusPill(th, u.t("ui.presets.check"), toneWarn))
							}),
							hspace(4),
							layout.Rigid(func(gtx C) D {
								name := "expand"
								if e.open {
									name = "collapse"
								}
								return drawIcon(gtx, name, 20, colFaint)
							}),
						)
					})
				})
			})
		}),
		layout.Rigid(func(gtx C) D {
			if !e.open {
				return D{}
			}
			return layout.Inset{Top: gapM, Bottom: 4}.Layout(gtx, func(gtx C) D {
				return p.layoutEditor(gtx, u, e, problem, warned, locked)
			})
		}),
	)
}

// summary is a row's name over its kind and target.
func (p *presetsPage) summary(gtx C, u *UI, e *presetEdit) D {
	th := u.th
	name, nameCol := strings.TrimSpace(e.name.Text()), colText
	if name == "" {
		name, nameCol = u.t("ui.presets.unnamed"), colFaint
	}
	sub := ""
	if e.typ != "" {
		sub = u.t("presets.type." + e.typ)
	}
	if path := strings.TrimSpace(e.path.Text()); path != "" {
		if e.typ != "url" {
			path = filepath.Base(path)
		}
		sub += " · " + path
	}
	return column(gtx,
		layout.Rigid(line(th, textL, name, nameCol, font.Medium).Layout),
		vspace(2),
		layout.Rigid(line(th, textS, sub, colSub, font.Normal).Layout),
	)
}

// layoutEditor is an open row's fields and actions.
func (p *presetsPage) layoutEditor(gtx C, u *UI, e *presetEdit, problem, warned string, locked bool) D {
	th := u.th
	types := gui.PresetTypes()
	typeLabels := make([]string, len(types))
	for i, t := range types {
		typeLabels[i] = u.t("presets.type." + t)
	}
	url := e.typ == "url"
	children := []layout.FlexChild{
		fieldLabel(th, u.t("ui.presets.name")),
		layout.Rigid(field(th, &e.name, u.t("presets.name.placeholder"))),
		vspace(gapM),
		fieldLabel(th, u.t("ui.presets.kind")),
		layout.Rigid(func(gtx C) D { return e.types.Layout(gtx, th, typeLabels, slices.Index(types, e.typ)) }),
		vspace(gapM),
		fieldLabel(th, u.t("presets.slot")),
		layout.Rigid(func(gtx C) D {
			return e.slots.Layout(gtx, th, gui.PresetMaxSlots, e.slot, func(s int) bool { return p.slotTaken(e, s) })
		}),
		layout.Rigid(func(gtx C) D {
			if locked {
				return D{}
			}
			return layout.Inset{Top: 6}.Layout(gtx, label(th, textS, u.t("ui.presets.slot.hint"), colSub, font.Normal).Layout)
		}),
		vspace(gapM),
		fieldLabel(th, u.t("presets.path")),
		layout.Rigid(func(gtx C) D {
			path := field(th, &e.path, gui.PresetPathPlaceholder(u.language(), e.typ))
			if url || locked {
				return path(gtx)
			}
			return row(gtx,
				layout.Flexed(1, path),
				hspace(8),
				layout.Rigid(func(gtx C) D { return fieldButton(th, &e.browse, u.t("presets.browse")).Layout(gtx) }),
			)
		}),
	}
	if note, warn := e.linkNote(u); note != "" {
		col := colSub
		if warn {
			col = colWarn
		}
		children = append(children, vspace(6), layout.Rigid(label(th, textS, note, col, font.Normal).Layout))
	}
	if !url {
		children = append(children,
			vspace(gapM),
			fieldLabel(th, u.t("presets.args")),
			layout.Rigid(field(th, &e.args, u.t("presets.args.placeholder"))),
		)
	}
	for _, b := range []struct {
		text string
		tone tone
	}{{problem, toneWarn}, {warned, toneWarn}, {e.status, e.statusTone}} {
		if b.text == "" {
			continue
		}
		children = append(children, vspace(gapS), layout.Rigid(func(gtx C) D { return banner(gtx, th, b.tone, b.text) }))
	}
	if !locked {
		children = append(children, vspace(gapM), layout.Rigid(func(gtx C) D {
			testTxt := u.t("presets.test")
			if e.testing {
				testTxt = u.t("presets.testing")
			}
			return row(gtx,
				layout.Rigid(func(gtx C) D {
					b := smallButton(th, &e.test, testTxt, colAccent)
					b.Background = colAccentDim
					return b.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx C) D { return D{Size: gtx.Constraints.Min} }),
				layout.Rigid(smallButton(th, &e.del, u.t("ui.presets.delete"), colDanger).Layout),
			)
		}))
	}
	return column(gtx, children...)
}

// arrivalGlowBehind draws w over an accent tint that fades out by until:
// where a jump landed (OpenSlot), so the eye finds it.
func arrivalGlowBehind(gtx C, until time.Time, w layout.Widget) D {
	left := until.Sub(gtx.Now)
	if left <= 0 {
		return w(gtx)
	}
	c := colAccentDim
	c.A = uint8(float32(c.A) * min(float32(left)/float32(arrivalGlow/2), 1))
	gtx.Execute(op.InvalidateCmd{})
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, gtx.Dp(radiusM)).Op(gtx.Ops))
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(w),
	)
}
