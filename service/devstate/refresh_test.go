package devstate

import (
	"testing"
	"time"
)

// One reading at a time, at most one start per interval.
func TestRefreshSingleFlightAndInterval(t *testing.T) {
	var r Refresh
	every := 30 * time.Second
	if !r.Begin(t0, every, false) {
		t.Fatal("the first reading did not start")
	}
	if r.Begin(t0.Add(time.Minute), every, false) {
		t.Error("a second reading started while the first ran")
	}
	if r.End(t0.Add(time.Second)) {
		t.Error("End asked for another reading nobody forced")
	}
	if r.Begin(t0.Add(29*time.Second), every, false) {
		t.Error("a reading started within the interval")
	}
	if !r.Begin(t0.Add(30*time.Second), every, false) {
		t.Error("no reading once the interval had passed")
	}
	r.End(t0.Add(31 * time.Second))
	// A clock that went back does not block the next reading for good.
	if !r.Begin(t0.Add(-time.Hour), every, false) {
		t.Error("a clock that went back blocked the reading")
	}
	r.End(t0)
}

// force skips the interval, and while a reading runs it asks for one more.
func TestRefreshForce(t *testing.T) {
	var r Refresh
	every := 30 * time.Second
	r.Begin(t0, every, false)
	r.End(t0)
	if !r.Begin(t0.Add(time.Second), every, true) {
		t.Fatal("a forced reading waited for the interval")
	}
	if r.Begin(t0.Add(2*time.Second), every, true) {
		t.Error("a forced reading started next to a running one")
	}
	if !r.End(t0.Add(3 * time.Second)) {
		t.Fatal("End dropped the forced request")
	}
	if r.Begin(t0.Add(4*time.Second), every, false) {
		t.Error("the repeated reading is not running")
	}
	if r.End(t0.Add(5 * time.Second)) {
		t.Error("End asked for a second repeat")
	}
	if r.Begin(t0.Add(10*time.Second), every, false) {
		t.Error("the repeat did not count as the newest reading")
	}
	r.Reset()
	if !r.Begin(t0.Add(10*time.Second), every, false) {
		t.Error("Reset kept the interval")
	}
}
