package ui

// Chrome around the screens: the sub-page top bar, the save bar that
// appears only with unsaved changes, and the toast.

import (
	"image"
	"sync"
	"time"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// topBar is a sub-page's header: a back button and the title.
func topBar(gtx C, th *material.Theme, back *widget.Clickable, title string) D {
	return layout.Inset{Top: 8, Bottom: 8, Left: 8, Right: gapM}.Layout(gtx, func(gtx C) D {
		return row(gtx,
			layout.Rigid(func(gtx C) D {
				return back.Layout(gtx, func(gtx C) D {
					pointer.CursorPointer.Add(gtx.Ops)
					s := gtx.Dp(40)
					if back.Hovered() {
						paint.FillShape(gtx.Ops, colGhost, clip.Ellipse(image.Rect(0, 0, s, s)).Op(gtx.Ops))
					}
					gtx.Constraints = layout.Exact(image.Pt(s, s))
					return layout.Center.Layout(gtx, func(gtx C) D { return drawIcon(gtx, "back", 24, colText) })
				})
			}),
			hspace(4),
			layout.Flexed(1, line(th, titleSize, title, colText, font.Bold).Layout),
		)
	})
}

// saveBarState is the bar's two buttons.
type saveBarState struct {
	save, discard widget.Clickable
}

// saveBar is pinned to the bottom of a sub-page while it has unsaved
// changes: what is pending, 되돌리기 and 저장.
func saveBar(gtx C, th *material.Theme, s *saveBarState, msg, discardTxt, saveTxt string) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			sz := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, colCard, clip.Rect(image.Rectangle{Max: sz}).Op())
			paint.FillShape(gtx.Ops, colLine, clip.Rect(image.Rect(0, 0, sz.X, max(gtx.Dp(1), 1))).Op())
			return D{Size: sz}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return centered(gtx, pageColumnW, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: gapS, Bottom: gapS, Left: gapL, Right: gapL}.Layout(gtx, func(gtx C) D {
					return row(gtx,
						layout.Flexed(1, line(th, textM, msg, colSub, font.Medium).Layout),
						hspace(8),
						layout.Rigid(func(gtx C) D {
							b := greyButton(th, &s.discard, discardTxt)
							b.Inset = layout.Inset{Top: 10, Bottom: 10, Left: 14, Right: 14}
							b.TextSize = textM
							return b.Layout(gtx)
						}),
						hspace(8),
						layout.Rigid(func(gtx C) D {
							b := primaryButton(th, &s.save, saveTxt)
							b.Inset = layout.Inset{Top: 10, Bottom: 10, Left: 18, Right: 18}
							b.TextSize = textM
							return b.Layout(gtx)
						}),
					)
				})
			})
		}),
	)
}

// toastShown is how long a toast stays.
const toastShown = 2500 * time.Millisecond

// toaster holds the current toast. show is safe from any goroutine.
type toaster struct {
	mu    sync.Mutex
	text  string
	bad   bool
	until time.Time
}

func (t *toaster) show(text string, bad bool, now time.Time) {
	t.mu.Lock()
	t.text, t.bad, t.until = text, bad, now.Add(toastShown)
	t.mu.Unlock()
}

// current is the toast to draw at now, if any.
func (t *toaster) current(now time.Time) (text string, bad bool, until time.Time, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.text == "" || !now.Before(t.until) {
		return "", false, time.Time{}, false
	}
	return t.text, t.bad, t.until, true
}

// layoutToast draws the toast as a dark pill near the bottom, bottomGap
// above the edge, and asks for a frame when it should go.
func (t *toaster) layout(gtx C, th *material.Theme, bottomGap int) D {
	text, bad, until, ok := t.current(gtx.Now)
	if !ok {
		return D{}
	}
	gtx.Execute(op.InvalidateCmd{At: until})
	bg := rgb(0x333d4b)
	if bad {
		bg = colDanger
	}
	macro := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	cgtx.Constraints.Max.X -= gtx.Dp(48)
	d := layout.Stack{}.Layout(cgtx,
		layout.Expanded(func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radiusM)).Op(gtx.Ops))
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			return layout.Inset{Top: 12, Bottom: 12, Left: 18, Right: 18}.Layout(gtx,
				label(th, textM, text, rgb(0xffffff), font.Medium).Layout)
		}),
	)
	call := macro.Stop()
	x := (gtx.Constraints.Max.X - d.Size.X) / 2
	y := gtx.Constraints.Max.Y - d.Size.Y - bottomGap
	off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	return D{}
}
