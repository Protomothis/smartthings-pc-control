package gui

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"golang.org/x/sys/windows"
)

// instanceNames are kernel object names of the test's own, so the real app
// running on the machine is neither found nor disturbed.
func instanceNames(t *testing.T) (mutex, event string) {
	base := fmt.Sprintf("STPC-test-%d-%s-%d", os.Getpid(), t.Name(), time.Now().UnixNano())
	return base + "-mutex", base + "-activate"
}

// stubActivationUI keeps activateRunning off the real desktop: it counts
// the title lookup an older running version would need.
func stubActivationUI(t *testing.T) *atomic.Int32 {
	t.Helper()
	var byTitle atomic.Int32
	savedFocus, savedAllow := focusExistingWindow, allowForeground
	focusExistingWindow = func() { byTitle.Add(1) }
	allowForeground = func() {}
	t.Cleanup(func() { focusExistingWindow, allowForeground = savedFocus, savedAllow })
	return &byTitle
}

func closeInstance(inst guiInstance) {
	if inst.activate != 0 {
		windows.CloseHandle(inst.activate)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no activation within 5 s", what)
	}
}

// The first launch is the app; a later one is not, and finds the running
// instance's activation event.
func TestClaimInstanceIsSingle(t *testing.T) {
	m, e := instanceNames(t)
	first := claimInstance(m, e)
	defer closeInstance(first)
	if !first.first || first.activate == 0 || first.activateExisted {
		t.Fatalf("first launch: %+v, want the slot and a new event", first)
	}
	second := claimInstance(m, e)
	defer closeInstance(second)
	if second.first {
		t.Fatal("a second launch also claimed the slot")
	}
	if second.activate == 0 || !second.activateExisted {
		t.Fatalf("second launch: %+v, want the running instance's event", second)
	}
}

// The bug: after a minimized start there is no window to find, so a later
// launch must reach the running instance through the event, not by title.
func TestLaterLaunchActivatesRunningInstance(t *testing.T) {
	byTitle := stubActivationUI(t)
	m, e := instanceNames(t)
	running := claimInstance(m, e)
	defer closeInstance(running)
	activated := make(chan struct{}, 4)
	stop := watchActivation(running.activate, func() { activated <- struct{}{} })
	defer stop()

	for i := 0; i < 2; i++ { // every launch, not just the first
		later := claimInstance(m, e)
		activateRunning(later)
		closeInstance(later)
		waitSignal(t, activated, fmt.Sprintf("launch %d", i+1))
	}
	if n := byTitle.Load(); n != 0 {
		t.Errorf("looked for the window by title %d times", n)
	}
}

// A launch while the running instance is still starting (its watcher not
// yet up) is not lost: the auto-reset event stays set until it is waited on.
func TestActivationBeforeWatcherIsKept(t *testing.T) {
	stubActivationUI(t)
	m, e := instanceNames(t)
	running := claimInstance(m, e)
	defer closeInstance(running)
	later := claimInstance(m, e)
	activateRunning(later)
	closeInstance(later)

	activated := make(chan struct{}, 1)
	stop := watchActivation(running.activate, func() { activated <- struct{}{} })
	defer stop()
	waitSignal(t, activated, "launch before the watcher")
}

// A running instance older than the event has none: the launch falls back
// on restoring its window by title.
func TestLaunchFallsBackOnOlderInstance(t *testing.T) {
	byTitle := stubActivationUI(t)
	m, e := instanceNames(t)
	name, _ := windows.UTF16PtrFromString(m)
	old, err := windows.CreateMutex(nil, false, name) // the old app: mutex only
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(old)

	later := claimInstance(m, e)
	defer closeInstance(later)
	if later.first || later.activateExisted {
		t.Fatalf("launch against an old instance: %+v", later)
	}
	activateRunning(later)
	if n := byTitle.Load(); n != 1 {
		t.Errorf("looked for the old window %d times, want 1", n)
	}
}

// stop ends the watcher: nothing is activated after it returns.
func TestStopEndsWatcher(t *testing.T) {
	stubActivationUI(t)
	m, e := instanceNames(t)
	running := claimInstance(m, e)
	defer closeInstance(running)
	var calls atomic.Int32
	stop := watchActivation(running.activate, func() { calls.Add(1) })
	stop()
	stop() // idempotent
	windows.SetEvent(running.activate)
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("activated %d times after stop", n)
	}
}

// End to end on the app side: a minimized start that only built the tray
// gets its whole window when a later launch asks, the way Run wires the
// watcher. The tray's Open entry and left click call the same showWindow.
func TestLaunchOpensWindowOfMinimizedStart(t *testing.T) {
	stubActivationUI(t)
	syncBackground = true
	savedOnScreen := windowOnScreen
	windowOnScreen = func() bool { return false }
	t.Cleanup(func() { syncBackground = false; windowOnScreen = savedOnScreen })
	a := test.NewTempApp(t)
	u := &ui{app: a, client: NewClient(1), version: "test", quit: make(chan struct{}), lang: LangEn, deferContent: true}
	u.win = a.NewWindow(windowTitle)
	defer u.win.Close()
	placeholder := u.win.Content()
	u.rebuild()
	if u.tabs != nil {
		t.Fatal("the minimized start built the window")
	}

	m, e := instanceNames(t)
	running := claimInstance(m, e)
	defer closeInstance(running)
	shown := make(chan struct{}, 1)
	// As in Run, plus a signal once the UI work is done.
	stop := watchActivation(running.activate, func() { fyne.Do(u.showWindow); shown <- struct{}{} })
	defer stop()

	later := claimInstance(m, e)
	activateRunning(later)
	closeInstance(later)
	waitSignal(t, shown, "launch after a minimized start")
	if u.deferContent || u.tabs == nil || u.win.Content() == placeholder {
		t.Fatal("a later launch did not open the window")
	}
}
