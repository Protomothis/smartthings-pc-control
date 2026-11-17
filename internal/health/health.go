// Package health samples what a long-running process holds — goroutines,
// OS threads, kernel handles — so a leak shows up in the log long before
// the PC slows down. A report (2026-11) described the app and Explorer
// using most of the CPU after days of uptime; these lines are what would
// tell a pile-up apart from something outside the app.
package health

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// Sample is one reading of this process.
type Sample struct {
	Goroutines int
	Threads    int // OS threads alive in this process; 0 when unknown
	Handles    int // kernel handles; 0 when unknown
}

// Read samples this process.
func Read() Sample {
	s := Sample{Goroutines: runtime.NumGoroutine(), Threads: liveThreads()}
	var n uint32
	if r, _, _ := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n))); r != 0 {
		s.Handles = int(n)
	}
	return s
}

func (s Sample) String() string {
	return fmt.Sprintf("goroutines=%d threads=%d handles=%d", s.Goroutines, s.Threads, s.Handles)
}

// Limits is when a sample is worth a log line.
type Limits struct{ Goroutines, Threads, Handles int }

// Over reports whether s passes any of l.
func (s Sample) Over(l Limits) bool {
	return s.Goroutines > l.Goroutines || s.Threads > l.Threads || s.Handles > l.Handles
}

// Watch logs a sample every checkEvery while one is over l, and one every
// baselineEvery anyway (and the first at the start), until stop closes.
// The baseline lines are what a later "over" line is compared with.
func Watch(stop <-chan struct{}, l Limits, logf func(format string, args ...any)) {
	watch(stop, l, logf, checkEvery, baselineEvery)
}

const (
	checkEvery    = 10 * time.Minute
	baselineEvery = 6 * time.Hour
)

func watch(stop <-chan struct{}, l Limits, logf func(string, ...any), check, baseline time.Duration) {
	logf("health: %v", Read())
	t := time.NewTicker(check)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			s := Read()
			switch {
			case s.Over(l):
				logf("health: %v — above the usual (goroutines %d, threads %d, handles %d)", s, l.Goroutines, l.Threads, l.Handles)
				last = now
			case now.Sub(last) >= baseline:
				logf("health: %v", s)
				last = now
			}
		}
	}
}

// liveThreads counts the threads of this process now. The runtime's own
// threadcreate count only grows: every tray job ends its locked thread.
func liveThreads() int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	pid := windows.GetCurrentProcessId()
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	n := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID == pid {
			n++
		}
	}
	return n
}
