package health

// High-CPU capture (#176). A health line can say that the process is busy
// but not what it is busy with. When the CPU stays high, WatchCPU saves
// what a developer needs to find out: a short CPU profile and every
// goroutine's stack, in a diag folder the "send a report" feature (#175)
// packs together with the logs.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"time"
)

// DiagDir is the diag folder inside base: the tray app uses
// %LOCALAPPDATA%\SmartThings PC Control, the service its install folder.
// Everything in it is a capture worth sending with a report; the names
// follow DiagCPUPattern and DiagGoroutinesPattern.
func DiagDir(base string) string { return filepath.Join(base, "diag") }

// File names of one capture in DiagDir; the stamp is the local time the
// capture started, as yyyyMMdd-HHmmss.
const (
	// DiagCPUPattern is a CPU profile (runtime/pprof, `go tool pprof`).
	DiagCPUPattern = "cpu-*.pprof"
	// DiagGoroutinesPattern is a text dump of every goroutine's stack.
	DiagGoroutinesPattern = "goroutines-*.txt"
)

const (
	// cpuCheckEvery is how often WatchCPU reads the process times; a
	// reading is one system call.
	cpuCheckEvery = time.Minute
	// cpuHighPercent is what counts as high: half a core kept busy for a
	// whole minute. The tray app and the service idle near 0%, so this
	// is far from their normal load yet well below what a user notices
	// as "the app eats my CPU".
	cpuHighPercent = 50
	// cpuHighChecks is how many checks in a row must be high, so a burst
	// (an update download, the window opening) is not captured.
	cpuHighChecks = 2
	// captureCooldown keeps a process that stays busy from writing a
	// capture every two minutes: one tells what it is doing.
	captureCooldown = 6 * time.Hour
	// cpuProfileFor is how long the CPU profile records.
	cpuProfileFor = 10 * time.Second
	// keepCaptures is how many captures stay in the folder.
	keepCaptures = 3
	// stampLayout is the time stamp in a capture's file names.
	stampLayout = "20060102-150405"
)

// WatchCPU checks this process's CPU every minute until stop closes. When
// two checks in a row find it above half a core, it saves a 10-second CPU
// profile and a goroutine dump to dir (see DiagDir) and logs a line naming
// them — at most once every 6 hours, keeping the newest 3 captures.
func WatchCPU(stop <-chan struct{}, dir string, logf func(format string, args ...any)) {
	g := newCPUGuard(dir, logf, func(dir, stamp string) ([]string, error) {
		return writeCapture(stop, dir, stamp, cpuProfileFor)
	})
	t := time.NewTicker(cpuCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			g.check()
		}
	}
}

// cpuGuard decides when to capture; check is called once per interval.
type cpuGuard struct {
	meter   *cpuMeter
	now     func() time.Time
	dir     string
	logf    func(string, ...any)
	capture func(dir, stamp string) ([]string, error)

	high []float64 // the figures of the current high streak
	last time.Time // when the last capture was taken
}

func newCPUGuard(dir string, logf func(string, ...any), capture func(dir, stamp string) ([]string, error)) *cpuGuard {
	g := &cpuGuard{meter: newCPUMeter(), now: time.Now, dir: dir, logf: logf, capture: capture}
	// Start the first interval now: the average since the process
	// started says nothing about the last minute.
	g.meter.next()
	return g
}

func (g *cpuGuard) check() {
	pct, ok := g.meter.next()
	if !ok || pct < cpuHighPercent {
		g.high = g.high[:0]
		return
	}
	g.high = append(g.high, pct)
	if len(g.high) < cpuHighChecks {
		return
	}
	figures := make([]string, len(g.high))
	for i, p := range g.high {
		figures[i] = formatPercent(p)
	}
	g.high = g.high[:0]
	now := g.now()
	if !g.last.IsZero() && now.Sub(g.last) < captureCooldown {
		return
	}
	g.last = now
	files, err := g.capture(g.dir, now.Format(stampLayout))
	// The profile's own seconds are not the next minute's load.
	g.meter.next()
	if err != nil {
		g.logf("health: CPU at %s of one core for %d minutes; could not save a capture in %s: %v",
			strings.Join(figures, ", "), cpuHighChecks, g.dir, err)
		return
	}
	g.logf("health: CPU at %s of one core for %d minutes — saved %s in %s",
		strings.Join(figures, ", "), cpuHighChecks, strings.Join(files, " and "), g.dir)
}

// writeCapture saves the goroutine dump and then a CPU profile of
// profileFor (cut short when stop closes) to dir, and prunes old captures.
// It returns the names of the files written. The dump goes first: it is
// the state at the moment the CPU was found high.
func writeCapture(stop <-chan struct{}, dir, stamp string, profileFor time.Duration) ([]string, error) {
	// The service's folder inherits the install folder's access (Users
	// may read), which is what lets the tray app pack it into a report.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	defer pruneCaptures(dir, keepCaptures)
	gName := strings.Replace(DiagGoroutinesPattern, "*", stamp, 1)
	if err := writeGoroutines(filepath.Join(dir, gName)); err != nil {
		return nil, err
	}
	cName := strings.Replace(DiagCPUPattern, "*", stamp, 1)
	if err := writeCPUProfile(stop, filepath.Join(dir, cName), profileFor); err != nil {
		return []string{gName}, err
	}
	return []string{cName, gName}, nil
}

func writeGoroutines(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "# %s %v\n\n", time.Now().Format(time.RFC3339), Read())
	werr := pprof.Lookup("goroutine").WriteTo(f, 2)
	return errors.Join(werr, f.Close())
}

func writeCPUProfile(stop <-chan struct{}, path string, d time.Duration) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	t := time.NewTimer(d)
	select {
	case <-t.C:
	case <-stop:
		t.Stop()
	}
	pprof.StopCPUProfile()
	return f.Close()
}

// pruneCaptures keeps the newest keep files of each kind in dir. The time
// stamp in the names sorts them by age.
func pruneCaptures(dir string, keep int) {
	for _, pattern := range []string{DiagCPUPattern, DiagGoroutinesPattern} {
		names, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil || len(names) <= keep {
			continue
		}
		sort.Strings(names)
		for _, old := range names[:len(names)-keep] {
			os.Remove(old)
		}
	}
}
