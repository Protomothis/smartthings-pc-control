package health

// This process's own figures for the app's problem report (#175). The tray
// runs as the user and may not open the SYSTEM service's process, so the
// service hands its numbers over itself (GET /api/diag/process). The CPU
// is the time used so far, not a percentage: the app reads it at both ends
// of the window it measures every other process over, so all its figures
// share one scale.

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process is a reading of this process.
type Process struct {
	PID uint32
	// CPU is the kernel plus user time used since the process started.
	CPU time.Duration
	// Started is when the process started.
	Started time.Time
	// WorkingSet and Private are bytes of memory.
	WorkingSet, Private uint64
	Sample
}

// processMemoryCountersEx is PROCESS_MEMORY_COUNTERS_EX.
type processMemoryCountersEx struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
	privateUsage               uintptr
}

var procK32GetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// ReadProcess reads this process: its CPU time, start, memory, threads,
// handles and goroutines. Memory left zero when it cannot be read.
func ReadProcess() (Process, error) {
	cpu, started, err := processTimes()
	if err != nil {
		return Process{}, err
	}
	p := Process{PID: windows.GetCurrentProcessId(), CPU: cpu, Started: started, Sample: Read()}
	var mc processMemoryCountersEx
	mc.cb = uint32(unsafe.Sizeof(mc))
	if r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb)); r != 0 {
		p.WorkingSet, p.Private = uint64(mc.workingSetSize), uint64(mc.privateUsage)
	}
	return p, nil
}
