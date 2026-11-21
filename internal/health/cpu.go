package health

// Process CPU (#176). Reports said the app used a lot of CPU while Apple
// Music played and that the PC slowed down after days of uptime, and the
// health lines could not say whether the app itself was busy. These
// helpers turn GetProcessTimes into "percent of one core since the last
// look", which is cheap enough to read every minute.

import (
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

// processTimes is the CPU time (kernel plus user) this process has used
// and when it started.
func processTimes() (time.Duration, time.Time, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return 0, time.Time{}, err
	}
	return filetimeDuration(kernel) + filetimeDuration(user), time.Unix(0, creation.Nanoseconds()), nil
}

// filetimeDuration reads a FILETIME that holds a span (100ns ticks) rather
// than a date.
func filetimeDuration(ft windows.Filetime) time.Duration {
	return time.Duration(int64(ft.HighDateTime)<<32|int64(ft.LowDateTime)) * 100
}

// cpuMeter measures the CPU this process used between two calls of next.
type cpuMeter struct {
	read func() (time.Duration, time.Time, error) // processTimes in the app
	now  func() time.Time

	lastCPU  time.Duration
	lastWall time.Time
}

func newCPUMeter() *cpuMeter { return &cpuMeter{read: processTimes, now: time.Now} }

// next returns the CPU used since the previous call — since the process
// started on the first — as percent of one core: 100 is one core kept
// busy, and several busy cores read above 100. ok is false when the
// times cannot be read or no time has passed.
func (m *cpuMeter) next() (pct float64, ok bool) {
	cpu, started, err := m.read()
	if err != nil {
		return 0, false
	}
	now := m.now()
	from, used := m.lastWall, cpu-m.lastCPU
	if from.IsZero() {
		from, used = started, cpu
	}
	m.lastCPU, m.lastWall = cpu, now
	wall := now.Sub(from)
	if wall <= 0 {
		return 0, false
	}
	return float64(used) / float64(wall) * 100, true
}

// formatCPU is the cpu= part of a health line: the share of one core and,
// since a figure above 100% surprises, of the whole machine.
func formatCPU(pct float64, ok bool) string {
	if !ok {
		return "cpu=?"
	}
	n := runtime.NumCPU()
	return fmt.Sprintf("cpu=%s (%s of %d cores)", formatPercent(pct), formatPercent(pct/float64(n)), n)
}

// formatPercent keeps one decimal for small figures, where 0.4% and 4%
// mean different things, and drops it elsewhere.
func formatPercent(pct float64) string {
	if pct < 10 {
		return fmt.Sprintf("%.1f%%", pct)
	}
	return fmt.Sprintf("%.0f%%", pct)
}
