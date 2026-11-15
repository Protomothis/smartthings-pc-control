package ui

// Responsive layout: the window can be any size from winMinW up. Narrow it
// is one column of cards; wide, the same cards flow into a masonry grid of
// two or three columns inside a centred, capped content width. Sub-pages
// keep a readable form width and centre it.

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// Breakpoints, in dp of window width.
const (
	twoColumnsAt   = 720  // two cards of ~340dp side by side
	threeColumnsAt = 1080 // three of ~340dp
	maxContentW    = 1240 // wider windows centre the grid instead of stretching it
	maxPageW       = 640  // sub-pages: a form wider than this is hard to read
	// pageColumnW is a sub-page column: the form plus its side margins.
	pageColumnW = maxPageW + 2*gapM
)

// columnsFor is the number of grid columns at a window width in dp.
func columnsFor(widthDp float32) int {
	switch {
	case widthDp >= threeColumnsAt:
		return 3
	case widthDp >= twoColumnsAt:
		return 2
	}
	return 1
}

// gridItem is one cell of a masonryGrid; a spanning item takes the whole
// row and closes the columns above it.
type gridItem struct {
	span bool
	w    layout.Widget
}

// masonryPlace assigns each of heights (one run of non-spanning items) to
// a column: the next item goes under the shortest column, the leftmost on
// a tie, so the columns end as level as the cards allow. It returns each
// item's column and top, and the run's total height.
func masonryPlace(heights []int, cols, gap int) (col, top []int, total int) {
	col, top = make([]int, len(heights)), make([]int, len(heights))
	colH := make([]int, cols)
	for i, h := range heights {
		c := 0
		for j := 1; j < cols; j++ {
			if colH[j] < colH[c] {
				c = j
			}
		}
		col[i], top[i] = c, colH[c]
		colH[c] += h + gap
	}
	for _, h := range colH {
		total = max(total, h-gap)
	}
	return col, top, max(total, 0)
}

// masonryGrid lays items out in cols columns with gap between cells. Cards
// keep their own heights (a masonry flow): the home cards differ too much
// in height for equal rows, which would leave big holes under the short
// ones.
func masonryGrid(gtx C, cols int, gap unit.Dp, items []gridItem) D {
	g := gtx.Dp(gap)
	width := gtx.Constraints.Max.X
	cellW := (width - g*(cols-1)) / cols
	y := 0
	place := func(call op.CallOp, x, at int) {
		t := op.Offset(image.Pt(x, at)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
	}
	measure := func(w layout.Widget, cw int) (op.CallOp, int) {
		m := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Min: image.Pt(cw, 0), Max: image.Pt(cw, gtx.Constraints.Max.Y)}
		d := w(cgtx)
		return m.Stop(), d.Size.Y
	}
	for i := 0; i < len(items); {
		if items[i].span {
			call, h := measure(items[i].w, width)
			if y > 0 {
				y += g
			}
			place(call, 0, y)
			y += h
			i++
			continue
		}
		j := i
		for j < len(items) && !items[j].span {
			j++
		}
		calls := make([]op.CallOp, 0, j-i)
		heights := make([]int, 0, j-i)
		for _, it := range items[i:j] {
			call, h := measure(it.w, cellW)
			calls, heights = append(calls, call), append(heights, h)
		}
		col, top, total := masonryPlace(heights, cols, g)
		if y > 0 {
			y += g
		}
		for k, call := range calls {
			place(call, col[k]*(cellW+g), y+top[k])
		}
		y += total
		i = j
	}
	return D{Size: image.Pt(width, y)}
}

// centered caps w at maxDp wide and centres it in the available width.
func centered(gtx C, maxDp unit.Dp, w layout.Widget) D {
	avail := gtx.Constraints.Max.X
	cw := min(avail, gtx.Dp(maxDp))
	off := (avail - cw) / 2
	cgtx := gtx
	cgtx.Constraints.Max.X = cw
	cgtx.Constraints.Min.X = min(cgtx.Constraints.Min.X, cw)
	t := op.Offset(image.Pt(off, 0)).Push(gtx.Ops)
	d := w(cgtx)
	t.Pop()
	return D{Size: image.Pt(max(avail, d.Size.X), d.Size.Y)}
}
