package ui

// Questions are a sheet over the window rather than a dialog: a title, a
// grey body, an optional warning line and two full-width buttons. Any
// screen asks one with u.ask (a delete, a routine slot changing hands);
// the tray's 종료 asks through it too, since #164 mute, media,
// notifications, presets and the screen commands run in the tray app.

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// askSpec is one question.
type askSpec struct {
	title, body string
	// note is an extra line in the warning colour ("저장하지 않은 변경").
	note string
	// ok is the confirm button's text; danger paints it red (the action
	// cannot be taken back). cancel defaults to 취소.
	ok, cancel string
	danger     bool
	// onOK runs on the window goroutine when confirmed, onCancel (may be
	// nil) when cancelled or dismissed by a click beside the sheet.
	onOK, onCancel func()
}

// sheetState is the open question and the sheet's widgets.
type sheetState struct {
	open                     *askSpec
	scrim, sheet, cancel, ok widget.Clickable
}

// ask shows a question, replacing one already open. Window goroutine
// only; from elsewhere wrap it in u.onWindow.
func (u *UI) ask(a askSpec) {
	if a.cancel == "" {
		a.cancel = u.t("login.cancel")
	}
	u.sheetW.open = &a
	u.invalidate()
}

// requestQuit shows the window with the quit question. Safe from any
// goroutine.
func (u *UI) requestQuit() {
	u.quitAsk.Store(true)
	u.Show()
	u.invalidate()
}

// askQuit turns the tray's request into the quit question; it mentions
// unsaved changes on the page in front.
func (u *UI) askQuit() {
	if !u.quitAsk.Swap(false) {
		return
	}
	a := askSpec{title: u.t("quit.title"), body: u.t("quit.body"), ok: u.t("tray.exit"), danger: true,
		onOK: func() { go u.quit() }}
	if u.formDirty(u.nav.top()) {
		a.note = u.t("unsaved.indicator")
	}
	u.ask(a)
}

// layoutSheet draws the open question, if any; call after the screen.
func (u *UI) layoutSheet(gtx C) {
	q := &u.sheetW
	a := q.open
	if a == nil {
		return
	}
	if q.cancel.Clicked(gtx) || q.scrim.Clicked(gtx) {
		q.open = nil
		if a.onCancel != nil {
			a.onCancel()
		}
		return
	}
	if q.ok.Clicked(gtx) {
		q.open = nil
		if a.onOK != nil {
			a.onOK()
		}
		return
	}
	// The scrim takes every click, so nothing under it reacts.
	q.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, color.NRGBA{A: 0x66}, clip.Rect(image.Rectangle{Max: gtx.Constraints.Max}).Op())
		return D{Size: gtx.Constraints.Max}
	})
	gtx.Constraints.Min = gtx.Constraints.Max
	layout.S.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(gapM).Layout(gtx, func(gtx C) D {
			return centered(gtx, pageColumnW, func(gtx C) D {
				// the sheet swallows clicks that miss its buttons
				return q.sheet.Layout(gtx, func(gtx C) D {
					return card(gtx, func(gtx C) D {
						pointer.CursorDefault.Add(gtx.Ops)
						children := []layout.FlexChild{
							layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, a.title, nil) }),
						}
						if a.body != "" {
							children = append(children, vspace(gapXS),
								layout.Rigid(label(u.th, textM, a.body, colSub, font.Normal).Layout))
						}
						if a.note != "" {
							children = append(children, vspace(gapXS),
								layout.Rigid(label(u.th, textM, a.note, colWarn, font.Medium).Layout))
						}
						okBtn := primaryButton(u.th, &q.ok, a.ok)
						if a.danger {
							okBtn = dangerButton(u.th, &q.ok, a.ok)
						}
						children = append(children, vspace(gapM), layout.Rigid(func(gtx C) D {
							return grid(gtx, 8,
								fill(greyButton(u.th, &q.cancel, a.cancel).Layout),
								fill(okBtn.Layout),
							)
						}))
						return column(gtx, children...)
					})
				})
			})
		})
	})
}
