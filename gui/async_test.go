package gui

import (
	"errors"
	"slices"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// runAsync marks the control busy before the work starts and releases it
// before done sees the result, so done may leave it disabled.
func TestRunAsyncOrder(t *testing.T) {
	test.NewTempApp(t)
	syncBackground = true
	defer func() { syncBackground = false }()

	var log []string
	busy := func(on bool) {
		if on {
			log = append(log, "busy")
		} else {
			log = append(log, "idle")
		}
	}
	boom := errors.New("boom")
	runAsync(busy, func() (int, error) {
		log = append(log, "work")
		return 7, boom
	}, func(v int, err error) {
		if v != 7 || !errors.Is(err, boom) {
			t.Errorf("done got %d, %v", v, err)
		}
		log = append(log, "done")
	})
	if want := []string{"busy", "work", "idle", "done"}; !slices.Equal(log, want) {
		t.Errorf("order = %v, want %v", log, want)
	}

	// nil busy and done are allowed (tray entries).
	runAsyncErr(nil, func() error { return nil }, nil)
}

func TestBusyControls(t *testing.T) {
	test.NewTempApp(t)
	a, b := widget.NewButton("a", nil), widget.NewButton("b", nil)
	busy := busyControls(a, b)
	busy(true)
	if !a.Disabled() || !b.Disabled() {
		t.Error("busy(true) left a control enabled")
	}
	busy(false)
	if a.Disabled() || b.Disabled() {
		t.Error("busy(false) left a control disabled")
	}
}
