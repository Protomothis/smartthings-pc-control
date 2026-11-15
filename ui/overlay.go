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
// changes: what is pending, 되돌리기 and 저장. busy (a save in flight)
// dims both buttons; the caller ignores their clicks meanwhile.
func saveBar(gtx C, th *material.Theme, s *saveBarState, msg, discardTxt, saveTxt string, busy bool) D {
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
							if busy {
								b.Color = colFaint
							}
							return b.Layout(gtx)
						}),
						hspace(8),
						layout.Rigid(func(gtx C) D {
							b := primaryButton(th, &s.save, saveTxt)
							b.Inset = layout.Inset{Top: 10, Bottom: 10, Left: 18, Right: 18}
							b.TextSize = textM
							if busy {
								b.Background = mix(colAccent, colCard, 0.5)
							}
							return b.Layout(gtx)
						}),
					)
				})
			})
		}),
	)
}

// toastShown is how long a toast stays; one with an action stays longer,
// long enough to read and reach for the button.
const (
	toastShown       = 2500 * time.Millisecond
	toastActionShown = 7 * time.Second
)

// toaster holds the current toast. show and showAction are safe from any
// goroutine; layout runs on the window goroutine.
type toaster struct {
	mu     sync.Mutex
	text   string
	bad    bool
	until  time.Time
	action string // the button's text, "" for none
	do     func() // runs on the window goroutine when the button is pressed

	btn widget.Clickable // window goroutine only
}

func (t *toaster) show(text string, bad bool, now time.Time) {
	t.mu.Lock()
	t.text, t.bad, t.until, t.action, t.do = text, bad, now.Add(toastShown), "", nil
	t.mu.Unlock()
}

// showAction is a toast with one button ([지금 재시작]); pressing it runs
// do and dismisses the toast.
func (t *toaster) showAction(text, action string, do func(), now time.Time) {
	t.mu.Lock()
	t.text, t.bad, t.until, t.action, t.do = text, false, now.Add(toastActionShown), action, do
	t.mu.Unlock()
}

// toastNow is the toast to draw.
type toastNow struct {
	text, action string
	bad          bool
	until        time.Time
	do           func()
}

// current is the toast to draw at now, if any.
func (t *toaster) current(now time.Time) (toastNow, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.text == "" || !now.Before(t.until) {
		return toastNow{}, false
	}
	return toastNow{text: t.text, action: t.action, bad: t.bad, until: t.until, do: t.do}, true
}

// dismiss ends the toast now.
func (t *toaster) dismiss() {
	t.mu.Lock()
	t.text = ""
	t.mu.Unlock()
}

// layout draws the toast as a dark pill near the bottom, bottomGap above
// the edge, and asks for a frame when it should go.
func (t *toaster) layout(gtx C, th *material.Theme, bottomGap int) D {
	cur, ok := t.current(gtx.Now)
	if !ok {
		return D{}
	}
	if cur.do != nil && t.btn.Clicked(gtx) {
		t.dismiss()
		cur.do()
		return D{}
	}
	gtx.Execute(op.InvalidateCmd{At: cur.until})
	bg := rgb(0x333d4b)
	if cur.bad {
		bg = colDanger
	}
	macro := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	cgtx.Constraints.Max.X = min(cgtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(pageColumnW))
	d := layout.Stack{}.Layout(cgtx,
		layout.Expanded(func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radiusM)).Op(gtx.Ops))
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx C) D {
			return layout.Inset{Top: 12, Bottom: 12, Left: 18, Right: 18}.Layout(gtx, func(gtx C) D {
				if cur.action == "" {
					return label(th, textM, cur.text, rgb(0xffffff), font.Medium).Layout(gtx)
				}
				return row(gtx,
					layout.Flexed(1, label(th, textM, cur.text, rgb(0xffffff), font.Medium).Layout),
					hspace(gapS),
					layout.Rigid(func(gtx C) D {
						b := smallButton(th, &t.btn, cur.action, rgb(0xffffff))
						b.Background = colAccent
						return b.Layout(gtx)
					}),
				)
			})
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
