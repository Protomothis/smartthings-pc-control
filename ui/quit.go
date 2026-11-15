package ui

// Quitting from the tray: since #164 mute, media, notifications, presets
// and the screen commands run in the tray app, so the app says so before
// it ends. The question is a sheet over the window (opened for it) rather
// than a dialog; it also mentions unsaved changes on the page in front.

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

// quitState is the sheet's widgets.
type quitState struct {
	scrim, sheet, cancel, ok widget.Clickable
}

// requestQuit shows the window with the quit sheet. Safe from any
// goroutine.
func (u *UI) requestQuit() {
	u.quitAsk.Store(true)
	u.Show()
	u.invalidate()
}

// layoutQuit draws the sheet while a quit is asked; call after the screen.
func (u *UI) layoutQuit(gtx C, s snapshot) {
	if !u.quitAsk.Load() {
		return
	}
	q := &u.quitW
	if q.cancel.Clicked(gtx) || q.scrim.Clicked(gtx) {
		u.quitAsk.Store(false)
		return
	}
	if q.ok.Clicked(gtx) {
		u.quitAsk.Store(false)
		go u.quit()
		return
	}
	unsaved := false
	if ed, ok := u.nav.top().(editor); ok && ed.dirty(s) {
		unsaved = true
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
							layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("quit.title"), nil) }),
							vspace(gapXS),
							layout.Rigid(label(u.th, textM, u.t("quit.body"), colSub, font.Normal).Layout),
						}
						if unsaved {
							children = append(children, vspace(gapXS),
								layout.Rigid(label(u.th, textM, u.t("unsaved.indicator"), colWarn, font.Medium).Layout))
						}
						children = append(children, vspace(gapM), layout.Rigid(func(gtx C) D {
							return grid(gtx, 8,
								fill(greyButton(u.th, &q.cancel, u.t("login.cancel")).Layout),
								fill(dangerButton(u.th, &q.ok, u.t("tray.exit")).Layout),
							)
						}))
						return column(gtx, children...)
					})
				})
			})
		})
	})
}
