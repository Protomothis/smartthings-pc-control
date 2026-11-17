package ui

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// wheelRig lays one wheel out under a router, so the tests can send it
// real pointer and key events.
type wheelRig struct {
	r    input.Router
	ops  op.Ops
	w    wheel
	n    int
	sel  int
	loop bool
	now  time.Time
	rowH float32
}

func newWheelRig(n, sel int, loop bool) *wheelRig {
	g := &wheelRig{n: n, sel: sel, loop: loop, now: time.Now()}
	usePalette(false)
	g.frame()
	return g
}

// frame lays the wheel out once, a frame's time after the last.
func (g *wheelRig) frame() {
	g.ops.Reset()
	g.now = g.now.Add(16 * time.Millisecond)
	gtx := layout.Context{
		Ops:         &g.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(200, 200)),
		Now:         g.now,
		Source:      g.r.Source(),
	}
	_, g.sel = g.w.Layout(gtx, newTheme(), 92, g.n, g.sel, g.loop, func(i int) string { return "x" })
	g.rowH = float32(gtx.Dp(wheelRowH))
	g.r.Frame(&g.ops)
}

// settle runs frames until the wheel stands still.
func (g *wheelRig) settle() {
	for range 200 {
		g.frame()
		if g.w.off == 0 {
			return
		}
	}
}

func (g *wheelRig) send(evs ...pointer.Event) {
	for _, e := range evs {
		e.Source, e.Buttons = pointer.Mouse, pointer.ButtonPrimary
		g.r.Queue(e)
	}
	g.frame()
}

func (g *wheelRig) mid() float32 { return g.rowH * float32(wheelRows) / 2 }

func TestWheelScroll(t *testing.T) {
	g := newWheelRig(24, 7, true)
	at := f32.Pt(40, g.mid())
	// One value per notch, whatever the distance.
	g.send(pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, 120)})
	if g.sel != 8 {
		t.Fatalf("down a notch: %d, want 8", g.sel)
	}
	g.send(pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, -3)})
	g.settle()
	if g.sel != 7 || g.w.off != 0 {
		t.Fatalf("up a notch: %d (off %v), want 7 settled", g.sel, g.w.off)
	}
	// A looping wheel goes round; one that does not stops at its ends.
	g.sel = 0
	g.send(pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, -1)})
	if g.sel != 23 {
		t.Errorf("hour 0 up: %d, want 23", g.sel)
	}
	h := newWheelRig(169, 0, false)
	h.send(pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, -1)})
	h.settle()
	if h.sel != 0 {
		t.Errorf("0 hours up: %d, want 0", h.sel)
	}
	h.sel = 168
	h.send(pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, 1)})
	if h.sel != 168 {
		t.Errorf("168 hours down: %d, want 168", h.sel)
	}
}

func TestWheelClickAndDrag(t *testing.T) {
	g := newWheelRig(12, 6, true)
	x := float32(40)
	// A click on the row above the middle.
	top := f32.Pt(x, g.rowH/2)
	g.send(pointer.Event{Kind: pointer.Press, Position: top}, pointer.Event{Kind: pointer.Release, Position: top})
	g.settle()
	if g.sel != 5 {
		t.Fatalf("click above: %d, want 5", g.sel)
	}
	// Pulled up by a little over two rows: two values on.
	y := g.mid()
	g.send(pointer.Event{Kind: pointer.Press, Position: f32.Pt(x, y)})
	for i := 1; i <= 10; i++ {
		g.send(pointer.Event{Kind: pointer.Move, Position: f32.Pt(x, y-float32(i)*g.rowH*0.22)})
	}
	g.send(pointer.Event{Kind: pointer.Release, Position: f32.Pt(x, y-2.2*g.rowH)})
	g.settle()
	if g.sel != 7 || g.w.off != 0 {
		t.Fatalf("drag up 2.2 rows: %d (off %v), want 7", g.sel, g.w.off)
	}
	// The click gave it the focus: the arrow keys step it.
	g.r.Queue(key.Event{Name: key.NameDownArrow, State: key.Press})
	g.frame()
	g.r.Queue(key.Event{Name: key.NameDownArrow, State: key.Press})
	g.frame()
	g.r.Queue(key.Event{Name: key.NameUpArrow, State: key.Press})
	g.frame()
	if g.sel != 8 {
		t.Errorf("down, down, up: %d, want 8", g.sel)
	}
}

// schedAPI records the schedule the card starts.
type schedAPI struct {
	fakeAPI
	got chan string
}

func (a schedAPI) SetSchedule(cmd string, minutes int) error {
	a.got <- cmd + " in " + time.Duration(minutes*int(time.Minute)).String()
	return nil
}

func (a schedAPI) SetScheduleAt(cmd string, minutes int, at time.Time) error {
	a.got <- cmd + " at " + at.Format("01-02 15:04")
	return nil
}

func (schedAPI) GetSchedule() (gui.Schedule, error) { return gui.Schedule{}, nil }

// The card starts a length as a delay and a clock time as that time
// (#173), and lays out in both modes, a passed time included.
func TestScheduleCardStarts(t *testing.T) {
	u := sampleUI(gui.LangKo, false)
	api := schedAPI{got: make(chan string, 2)}
	u.api = api
	u.home.schedPick = gui.SchedulePick{Hours: 2, Mins: 15}
	start := func() string {
		u.home.schedStart.Click()
		frames(u, winW, 4000, 1)
		select {
		case s := <-api.got:
			return s
		case <-time.After(3 * time.Second):
			return "nothing"
		}
	}
	if got := start(); got != "shutdown in 2h15m0s" {
		t.Errorf("length: %q", got)
	}
	day := time.Now().AddDate(0, 0, 5)
	u.home.schedPick = gui.SchedulePick{At: true, Day: 5, Hour: 7, Minute: 30}
	if got, want := start(), "shutdown at "+day.Format("01-02")+" 07:30"; got != want {
		t.Errorf("clock time: %q, want %q", got, want)
	}
	u.home.schedPick = gui.SchedulePick{At: true, Hour: 0}
	frames(u, 1200, 900, 2)
}
