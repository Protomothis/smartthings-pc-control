package main

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndpointArc(t *testing.T) {
	// The large ring: from (33,40) to (63,40), r 19, the long way round
	// below the chord.
	a := endpointArc(33, 40, 63, 40, 19, true, false)
	if math.Abs(a.cx-48) > 1e-9 || math.Abs(a.cy-(40+math.Sqrt(19*19-15*15))) > 1e-9 {
		t.Fatalf("centre (%v, %v)", a.cx, a.cy)
	}
	if a.da > -math.Pi {
		t.Fatalf("sweep %v: want the large arc, counter-clockwise", a.da)
	}
	// The bottom of the ring is on the arc; the top of the circle is not.
	if d := a.dist(48, a.cy+19); d > 1e-9 {
		t.Errorf("bottom is %v off the arc", d)
	}
	if d := a.dist(48, a.cy-19); d < 5 {
		t.Errorf("top is only %v off the arc", d)
	}
	// A small arc bulging up (a wave).
	w := endpointArc(39, 32, 57, 32, 13, false, true)
	if w.cy < 32 || w.dist(48, w.cy-13) > 1e-9 {
		t.Errorf("wave: centre y %v, top off by %v", w.cy, w.dist(48, w.cy-13))
	}
}

func TestMastersRender(t *testing.T) {
	m, err := loadMasters(filepath.Join("..", "..", "..", "assets", "logo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{16, 20, 24, 32, 48} {
		img := m.forSize(n).render(n, 4)
		// The middle of an edge is the blue tile; a corner pixel is clear.
		if c := img.NRGBAAt(n/2, 0); c.B != 0xF6 || c.A < 200 {
			t.Errorf("%d px: top edge %v", n, c)
		}
		if c := img.NRGBAAt(0, 0); c.A > 128 {
			t.Errorf("%d px: corner %v", n, c)
		}
	}
	if _, err := parseSVG(strings.NewReader(`<svg viewBox="0 0 96 96"><path d="M0 0L5 5" stroke="#fff" stroke-linecap="round"/></svg>`)); err == nil {
		t.Error("an L command was accepted")
	}
}
