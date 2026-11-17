package ui

import (
	"image"
	"math"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// A wheel is one column of a time picker, like the Windows clock app's: the
// values stacked vertically, the chosen one on the middle row (the picker
// draws the band behind it). The mouse wheel moves it a value per notch, a
// drag pulls it along and lets go on the nearest value, a click on a row
// above or below moves to it, and the arrow keys step it while it has the
// focus (a click gives it).

// wheelRowH is one row's height; wheelRows rows show, the middle one
// chosen.
const (
	wheelRowH unit.Dp = 34
	wheelRows int     = 3
)

// wheelSnap is how quickly a let-go wheel settles on its value: the
// offset left after one second is e^-wheelSnap of what it was.
const wheelSnap = 18.0

type wheel struct {
	off      float32 // px the column is pulled past the chosen row; + towards the next values
	dragging bool
	pid      pointer.ID
	pressY   float32
	lastY    float32
	moved    bool      // the press travelled beyond a click
	last     time.Time // the previous settling frame
}

// wheelHeight is the height of a wheel's column.
func wheelHeight(gtx C) int { return gtx.Dp(wheelRowH) * wheelRows }

// Layout draws n values of text(i) with sel chosen, width wide, and returns
// the choice after this frame's input. loop: past the last value comes the
// first again (hours of a day); otherwise the ends stop it.
func (w *wheel) Layout(gtx C, th *material.Theme, width unit.Dp, n, sel int, loop bool, txt func(int) string) (D, int) {
	rowH := float32(gtx.Dp(wheelRowH))
	size := image.Pt(gtx.Dp(width), wheelHeight(gtx))
	slop := float32(gtx.Dp(4))

	step := func(d int) {
		next := sel + d
		if loop {
			next = ((next % n) + n) % n
		} else {
			next = max(0, min(next, n-1))
		}
		if moved := d; next != sel {
			if !loop {
				moved = next - sel
			}
			// Start the new value where the old one stood and let it settle.
			w.off -= float32(moved) * rowH
			sel = next
		}
	}

	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: w, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll,
				ScrollY: pointer.ScrollRange{Min: math.MinInt32, Max: math.MaxInt32}},
			key.FocusFilter{Target: w},
			key.Filter{Focus: w, Name: key.NameUpArrow},
			key.Filter{Focus: w, Name: key.NameDownArrow},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			// The arrows work while it has the focus; nothing is drawn for it.
		case key.Event:
			if e.State == key.Press {
				if e.Name == key.NameUpArrow {
					step(-1)
				} else {
					step(1)
				}
			}
		case pointer.Event:
			switch e.Kind {
			case pointer.Scroll:
				// One value per notch, whatever distance the system reports.
				switch {
				case e.Scroll.Y > 0:
					step(1)
				case e.Scroll.Y < 0:
					step(-1)
				}
			case pointer.Press:
				if w.dragging {
					break
				}
				w.dragging, w.pid, w.moved = true, e.PointerID, false
				w.pressY, w.lastY = e.Position.Y, e.Position.Y
				gtx.Execute(key.FocusCmd{Tag: w})
			case pointer.Drag:
				if !w.dragging || e.PointerID != w.pid {
					break
				}
				if !w.moved && abs32(e.Position.Y-w.pressY) > slop {
					w.moved = true
					gtx.Execute(pointer.GrabCmd{Tag: w, ID: e.PointerID})
				}
				if w.moved {
					w.off += w.lastY - e.Position.Y
					sel = w.follow(sel, n, loop, rowH)
				}
				w.lastY = e.Position.Y
			case pointer.Release, pointer.Cancel:
				if !w.dragging || e.PointerID != w.pid {
					break
				}
				w.dragging = false
				if e.Kind == pointer.Release && !w.moved {
					// A click: the row under it becomes the choice.
					row := int(math.Floor(float64(e.Position.Y / rowH)))
					step(row - wheelRows/2)
				}
			}
		}
	}

	// Let go: settle on the chosen row.
	if !w.dragging && w.off != 0 {
		dt := float32(1.0 / 60)
		if !w.last.IsZero() {
			dt = min(float32(gtx.Now.Sub(w.last).Seconds()), 0.1)
		}
		w.off *= float32(math.Exp(-wheelSnap * float64(dt)))
		if abs32(w.off) < 0.5 {
			w.off = 0
		}
	}
	if !w.dragging && w.off != 0 {
		w.last = gtx.Now
		gtx.Execute(op.InvalidateCmd{})
	} else {
		w.last = time.Time{}
	}

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, w)
	pointer.CursorPointer.Add(gtx.Ops)

	mid := float32(size.Y)/2 - rowH/2
	for k := -wheelRows/2 - 1; k <= wheelRows/2+1; k++ {
		i := sel + k
		if loop {
			i = ((i % n) + n) % n
		} else if i < 0 || i >= n {
			continue
		}
		y := mid + float32(k)*rowH - w.off
		if y <= -rowH || y >= float32(size.Y) {
			continue
		}
		// Brighter towards the middle.
		near := 1 - min(abs32(y-mid)/rowH, 1)
		col := mix(colFaint, colText, near)
		weight := font.Normal
		if near > 0.5 {
			weight = font.SemiBold
		}
		t := op.Offset(image.Pt(0, int(math.Round(float64(y))))).Push(gtx.Ops)
		rgtx := gtx
		rgtx.Constraints = layout.Exact(image.Pt(size.X, int(rowH)))
		layout.Center.Layout(rgtx, func(gtx C) D {
			l := line(th, textL, txt(i), col, weight)
			l.Alignment = text.Middle
			return l.Layout(gtx)
		})
		t.Pop()
	}
	return D{Size: size}, sel
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// follow moves the choice along with a drag: a pull of more than half a
// row is the next value, and pulled past an end it gives a little, then
// stops.
func (w *wheel) follow(sel, n int, loop bool, rowH float32) int {
	for w.off > rowH/2 && (loop || sel < n-1) {
		w.off -= rowH
		sel++
	}
	for w.off < -rowH/2 && (loop || sel > 0) {
		w.off += rowH
		sel--
	}
	if loop {
		return ((sel % n) + n) % n
	}
	if sel == n-1 && w.off > 0 || sel == 0 && w.off < 0 {
		w.off = max(-rowH/3, min(w.off, rowH/3))
	}
	return sel
}
