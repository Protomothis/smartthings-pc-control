package ui

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

func TestColumnsFor(t *testing.T) {
	for _, c := range []struct {
		w    float32
		want int
	}{{winMinW, 1}, {winW, 1}, {719, 1}, {720, 2}, {1079, 2}, {1080, 3}, {4000, 3}} {
		if got := columnsFor(c.w); got != c.want {
			t.Errorf("columnsFor(%v) = %d, want %d", c.w, got, c.want)
		}
	}
}

func TestMasonryPlace(t *testing.T) {
	// Each card goes under the shortest column, leftmost on a tie.
	col, top, total := masonryPlace([]int{100, 50, 30, 40}, 2, 10)
	if !slices.Equal(col, []int{0, 1, 1, 1}) || !slices.Equal(top, []int{0, 0, 60, 100}) || total != 140 {
		t.Fatalf("col %v top %v total %d", col, top, total)
	}
	col, top, total = masonryPlace([]int{10, 20, 30}, 3, 5)
	if !slices.Equal(col, []int{0, 1, 2}) || !slices.Equal(top, []int{0, 0, 0}) || total != 30 {
		t.Fatalf("one row: col %v top %v total %d", col, top, total)
	}
	if _, _, total := masonryPlace(nil, 2, 10); total != 0 {
		t.Fatalf("empty run is %d tall", total)
	}
	col, _, total = masonryPlace([]int{10, 10}, 1, 4)
	if !slices.Equal(col, []int{0, 0}) || total != 24 {
		t.Fatalf("one column: col %v total %d", col, total)
	}
}

// fixed is a widget of a set height that fills its width.
func fixed(h int) layout.Widget {
	return func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, h)} }
}

func TestMasonryGridSpans(t *testing.T) {
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(210, 10000)), Now: time.Now()}
	gtx.Constraints.Min.Y = 0
	// a spanning header, two cells side by side, a spanning footer
	d := masonryGrid(gtx, 2, 10, []gridItem{
		{span: true, w: fixed(20)}, {w: fixed(50)}, {w: fixed(70)}, {span: true, w: fixed(5)},
	})
	if want := 20 + 10 + 70 + 10 + 5; d.Size.Y != want || d.Size.X != 210 {
		t.Fatalf("size %v, want 210x%d", d.Size, want)
	}
}

// The home screen keeps today's stack in one column and folds the
// connections and the log into cards in the grid.
func TestHomeItems(t *testing.T) {
	u := sampleUI(gui.LangKo, false)
	one, two := u.homeItems(u.current(), 1), u.homeItems(u.current(), 2)
	for _, it := range one {
		if !it.span {
			t.Fatal("one column has a cell")
		}
	}
	if len(one) != 11 || len(two) != 7 || !two[0].span || two[1].span {
		t.Fatalf("one column %d items, grid %d", len(one), len(two))
	}
}
