package health

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTimes is a process clock for cpuMeter: wall and cpu move only when a
// test says so.
type fakeTimes struct {
	start time.Time
	wall  time.Time
	cpu   time.Duration
	err   error
}

func newFakeTimes() *fakeTimes {
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	return &fakeTimes{start: t0, wall: t0}
}

func (f *fakeTimes) read() (time.Duration, time.Time, error) {
	return f.cpu, f.start, f.err
}

// pass moves the clock on by wall while the process uses cpu.
func (f *fakeTimes) pass(wall, cpu time.Duration) { f.wall, f.cpu = f.wall.Add(wall), f.cpu+cpu }

func (f *fakeTimes) meter() *cpuMeter {
	return &cpuMeter{read: f.read, now: func() time.Time { return f.wall }}
}

func TestCPUMeter(t *testing.T) {
	f := newFakeTimes()
	m := f.meter()
	// The first figure is the average since the process started.
	f.pass(10*time.Second, 5*time.Second)
	if pct, ok := m.next(); !ok || pct != 50 {
		t.Errorf("since start: %v %v", pct, ok)
	}
	// Then each is the interval since the previous call; two busy cores
	// read 200%.
	f.pass(time.Minute, 2*time.Minute)
	if pct, ok := m.next(); !ok || pct != 200 {
		t.Errorf("two cores: %v %v", pct, ok)
	}
	if _, ok := m.next(); ok {
		t.Error("no time passed, still a figure")
	}
	f.err = errors.New("access denied")
	f.pass(time.Minute, 0)
	if _, ok := m.next(); ok {
		t.Error("read error, still a figure")
	}
}

func TestFormatCPU(t *testing.T) {
	if got := formatCPU(0, false); got != "cpu=?" {
		t.Errorf("unknown: %q", got)
	}
	if got := formatCPU(37.4, true); !strings.HasPrefix(got, "cpu=37% (") || !strings.HasSuffix(got, " cores)") {
		t.Errorf("37.4: %q", got)
	}
	if got := formatPercent(0.42); got != "0.4%" {
		t.Errorf("small: %q", got)
	}
}

// guardFor is a cpuGuard on fake times that records its captures.
func guardFor(f *fakeTimes, fail error) (*cpuGuard, *[]string, *[]string) {
	var stamps, lines []string
	g := &cpuGuard{
		meter: f.meter(),
		now:   func() time.Time { return f.wall },
		dir:   `C:\diag`,
		logf:  func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) },
		capture: func(dir, stamp string) ([]string, error) {
			stamps = append(stamps, stamp)
			return []string{"cpu-" + stamp + ".pprof", "goroutines-" + stamp + ".txt"}, fail
		},
	}
	g.meter.next()
	return g, &stamps, &lines
}

func TestCPUGuardCapturesAfterTwoHighMinutes(t *testing.T) {
	f := newFakeTimes()
	g, stamps, lines := guardFor(f, nil)
	minute := func(busy time.Duration) { f.pass(time.Minute, busy); g.check() }

	// High, low, high: no two in a row.
	minute(40 * time.Second)
	minute(10 * time.Second)
	minute(40 * time.Second)
	if len(*stamps) != 0 {
		t.Fatalf("captured on a burst: %v", *stamps)
	}
	minute(45 * time.Second)
	if len(*stamps) != 1 || (*stamps)[0] != f.wall.Format(stampLayout) {
		t.Fatalf("stamps = %v", *stamps)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "CPU at 67%, 75% of one core for 2 minutes — saved cpu-") {
		t.Errorf("lines = %q", *lines)
	}

	// Still busy: nothing more for 6 hours.
	for range 6*60 - 2 {
		minute(50 * time.Second)
	}
	if len(*stamps) != 1 {
		t.Fatalf("captured again within 6h: %v", *stamps)
	}
	minute(50 * time.Second)
	minute(50 * time.Second)
	if len(*stamps) != 2 {
		t.Errorf("no capture after 6h: %v", *stamps)
	}
}

func TestCPUGuardLogsFailure(t *testing.T) {
	f := newFakeTimes()
	g, _, lines := guardFor(f, errors.New("disk full"))
	for range 2 {
		f.pass(time.Minute, time.Minute)
		g.check()
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "could not save a capture") || !strings.Contains((*lines)[0], "disk full") {
		t.Errorf("lines = %q", *lines)
	}
}

func TestWriteCaptureAndPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diag")
	// Four older captures already there.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"20261101-000000", "20261102-000000", "20261103-000000", "20261104-000000"} {
		for _, n := range []string{"cpu-" + s + ".pprof", "goroutines-" + s + ".txt"} {
			if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	files, err := writeCapture(nil, dir, "20261121-090000", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, " ") != "cpu-20261121-090000.pprof goroutines-20261121-090000.txt" {
		t.Errorf("files = %v", files)
	}
	dump, err := os.ReadFile(filepath.Join(dir, "goroutines-20261121-090000.txt"))
	if err != nil || !strings.Contains(string(dump), "goroutine ") || !strings.Contains(string(dump), "TestWriteCaptureAndPrune") {
		t.Errorf("dump: %v %.200s", err, dump)
	}
	if st, err := os.Stat(filepath.Join(dir, "cpu-20261121-090000.pprof")); err != nil || st.Size() == 0 {
		t.Errorf("profile: %v", err)
	}
	for _, pattern := range []string{DiagCPUPattern, DiagGoroutinesPattern} {
		names, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(names) != keepCaptures || !strings.Contains(names[0], "20261103") {
			t.Errorf("%s left: %v", pattern, names)
		}
	}
}

func TestDiagDir(t *testing.T) {
	if got := DiagDir(`C:\Program Files\SmartThings PC Control`); got != `C:\Program Files\SmartThings PC Control\diag` {
		t.Errorf("DiagDir = %q", got)
	}
}
