package ui

import (
	"testing"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// The two steppers move hours and minutes of a length, or the hour and
// minute of a clock time, by the mode the card is in.
func TestScheduleSteppers(t *testing.T) {
	u := sampleUI(gui.LangKo, false)
	u.home.schedPick = gui.SchedulePick{Hours: 1, Mins: 30, Hour: 22, Minute: 0}
	u.schedStepA(1)
	u.schedStepB(-1)
	if p := u.home.schedPick; p.Hours != 2 || p.Mins != 25 || p.Hour != 22 || p.Minute != 0 {
		t.Errorf("length mode moved %+v", p)
	}
	u.home.schedPick.At = true
	u.schedStepA(2)
	u.schedStepB(1)
	if p := u.home.schedPick; p.Hour != 0 || p.Minute != 5 || p.Hours != 2 || p.Mins != 25 {
		t.Errorf("clock mode moved %+v", p)
	}
	// The card lays out in both modes, a passed time included.
	u.home.schedPick = gui.SchedulePick{At: true, Hour: 0}
	frames(u, 1200, 900, 2)
	u.home.schedPick = gui.SchedulePick{}
	frames(u, winW, winH, 2)
}
