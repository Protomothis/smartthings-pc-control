package health

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadCountsThisProcess(t *testing.T) {
	s := Read()
	if s.Goroutines < 1 || s.Threads < 1 || s.Handles < 1 {
		t.Fatalf("sample = %v", s)
	}
	// Goroutines parked on a channel show up.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); <-stop }()
	}
	if got := Read(); got.Goroutines < s.Goroutines+50 {
		t.Errorf("50 more goroutines: %v then %v", s, got)
	}
	close(stop)
	wg.Wait()
}

func TestOver(t *testing.T) {
	l := Limits{Goroutines: 10, Threads: 10, Handles: 10}
	for s, want := range map[Sample]bool{
		{5, 5, 5}:    false,
		{11, 5, 5}:   true,
		{5, 11, 5}:   true,
		{5, 5, 11}:   true,
		{10, 10, 10}: false,
	} {
		if got := s.Over(l); got != want {
			t.Errorf("%v.Over = %v", s, got)
		}
	}
}

func TestWatchLogsBaselineAndOver(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watch(stop, Limits{Goroutines: 1, Threads: 1 << 20, Handles: 1 << 20}, logf, 5*time.Millisecond, time.Hour)
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "health: goroutines=") || !strings.Contains(lines[1], "above the usual") {
		t.Errorf("lines = %q", lines)
	}
}

// Threads held by LockOSThread (what each tray job does) are counted
// while they live: the leak the counter is there to show.
func TestThreadsCountsLockedThreads(t *testing.T) {
	var ready, wg sync.WaitGroup
	stop := make(chan struct{})
	for range 10 {
		ready.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			ready.Done()
			<-stop
		}()
	}
	ready.Wait()
	// Idle runtime threads may take the locks, so only a floor holds:
	// ten held threads and the one running this test.
	if got := Read().Threads; got < 11 {
		t.Errorf("threads %d with 10 locked", got)
	}
	close(stop)
	wg.Wait()
}
