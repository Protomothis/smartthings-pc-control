package gui

// The state at send time (#175): how busy the PC is, what our two
// processes use and which programs take the most CPU, measured over about
// a second when the report is made. "It got slow" reports need it: the
// logs say what the app did, not whether something else was eating the
// PC.
//
// Only image names leave the PC (chrome.exe): no paths, command lines or
// window titles. A process this user may not open (another user's, a
// protected one) is left out and counted. Everything Windows-side goes
// through snapSource so the tests hand in their own process lists.

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// snapshotWindow is how long the CPU is watched.
	snapshotWindow = time.Second
	// snapshotTop is how many programs the top list shows.
	snapshotTop = 10
)

// ProcStat is one process, or one program (same-name processes merged)
// in the top list.
type ProcStat struct {
	Name string
	// Count is how many processes the row merges (top list only).
	Count int
	// CPU is the share of the whole PC (all cores) in percent over the
	// window; CPUOK is false when it could not be measured.
	CPU   float64
	CPUOK bool
	// WorkingSet and Private are bytes; Threads and Handles counts.
	WorkingSet, Private uint64
	Threads, Handles    uint32
	// Err says why the process could not be read ("" when it was).
	Err string
}

// StateSnapshot is the PC at the moment the report was made.
type StateSnapshot struct {
	// CPU is the whole PC's usage in percent over the window.
	CPU   float64
	CPUOK bool
	// App is this process (the tray); Service is the Windows service, its
	// Err set when it is not running or could not be opened.
	App, Service ProcStat
	// Top is the programs that used the most CPU, highest first, merged
	// by name.
	Top []ProcStat
	// Skipped is how many processes could not be opened.
	Skipped int
}

// Masked is s with every process name passed through m: a program named
// after its user would otherwise give the name away.
func (s StateSnapshot) Masked(m *Masker) StateSnapshot {
	s.App.Name, s.Service.Name = m.Line(s.App.Name), m.Line(s.Service.Name)
	top := make([]ProcStat, len(s.Top))
	for i, p := range s.Top {
		p.Name = m.Line(p.Name)
		top[i] = p
	}
	s.Top = top
	return s
}

// procSample is one process as read at one moment.
type procSample struct {
	pid, threads        uint32
	name                string
	open                bool // could be opened: cpu and memory are known
	err                 string
	cpu                 time.Duration // kernel plus user so far
	workingSet, private uint64
	handles             uint32
}

// snapSource is where TakeStateSnapshot reads from; the tests replace it.
type snapSource struct {
	// systemTimes is GetSystemTimes: idle, kernel (which includes idle)
	// and user time of all cores so far.
	systemTimes func() (idle, kernel, user time.Duration, err error)
	processes   func() ([]procSample, error)
	servicePID  func() (uint32, error)
	selfPID     func() uint32
	sleep       func(time.Duration)
}

// TakeStateSnapshot measures the PC for about a second. Call it off the
// window goroutine.
func TakeStateSnapshot() StateSnapshot { return takeSnapshot(realSnapSource()) }

func takeSnapshot(src snapSource) StateSnapshot {
	var snap StateSnapshot
	idle1, kern1, user1, sysErr := src.systemTimes()
	before, _ := src.processes()
	src.sleep(snapshotWindow)
	idle2, kern2, user2, sysErr2 := src.systemTimes()
	after, err := src.processes()

	total := (kern2 - kern1) + (user2 - user1)
	if sysErr == nil && sysErr2 == nil && total > 0 {
		snap.CPU, snap.CPUOK = pct(total-(idle2-idle1), total), true
	}
	prev := make(map[uint32]procSample, len(before))
	for _, p := range before {
		prev[p.pid] = p
	}
	stat := func(p procSample) ProcStat {
		st := ProcStat{Name: p.name, Count: 1, Threads: p.threads, Err: p.err}
		if !p.open {
			return st
		}
		st.WorkingSet, st.Private, st.Handles = p.workingSet, p.private, p.handles
		if q, ok := prev[p.pid]; ok && q.open && q.name == p.name && total > 0 && p.cpu >= q.cpu {
			st.CPU, st.CPUOK = pct(p.cpu-q.cpu, total), true
		}
		return st
	}

	self := src.selfPID()
	svc, svcErr := src.servicePID()
	snap.App = ProcStat{Err: "not found"}
	snap.Service = ProcStat{Err: "not running"}
	if svcErr != nil {
		snap.Service.Err = svcErr.Error()
	}
	byName := map[string]*ProcStat{}
	for _, p := range after {
		if p.pid == 0 {
			continue // the idle "process" is the free CPU, not a program
		}
		st := stat(p)
		switch {
		case p.pid == self:
			snap.App = st
		case svcErr == nil && svc != 0 && p.pid == svc:
			if !p.open && st.Err == "" {
				st.Err = "cannot be opened"
			}
			snap.Service = st
		}
		if !p.open {
			snap.Skipped++
			continue
		}
		key := strings.ToLower(p.name)
		if m, ok := byName[key]; ok {
			m.Count++
			m.CPU += st.CPU
			m.CPUOK = m.CPUOK || st.CPUOK
			m.WorkingSet += st.WorkingSet
			continue
		}
		byName[key] = &ProcStat{Name: p.name, Count: 1, CPU: st.CPU, CPUOK: st.CPUOK, WorkingSet: st.WorkingSet}
	}
	if err != nil && len(after) == 0 {
		// No process list at all: say why on both of our rows.
		snap.App.Err, snap.Service.Err = err.Error(), err.Error()
	}
	for _, m := range byName {
		snap.Top = append(snap.Top, *m)
	}
	// Highest CPU first; memory, then the name, break ties so the list
	// does not shuffle between two looks at an idle PC.
	slices.SortFunc(snap.Top, func(a, b ProcStat) int {
		if c := cmp.Compare(b.CPU, a.CPU); c != 0 {
			return c
		}
		if c := cmp.Compare(b.WorkingSet, a.WorkingSet); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(snap.Top) > snapshotTop {
		snap.Top = snap.Top[:snapshotTop]
	}
	return snap
}

// pct is part of whole in percent.
func pct(part, whole time.Duration) float64 { return float64(part) / float64(whole) * 100 }

// --- Windows ---

func realSnapSource() snapSource {
	return snapSource{
		systemTimes: systemTimes,
		processes:   listProcesses,
		servicePID:  servicePID,
		selfPID:     windows.GetCurrentProcessId,
		sleep:       time.Sleep,
	}
}

var (
	snapKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes         = snapKernel32.NewProc("GetSystemTimes")
	procGetProcessHandleCount  = snapKernel32.NewProc("GetProcessHandleCount")
	procK32GetProcessMemoryInf = snapKernel32.NewProc("K32GetProcessMemoryInfo")
)

// ticks reads a FILETIME that holds a span (100ns ticks).
func ticks(ft windows.Filetime) time.Duration {
	return time.Duration(int64(ft.HighDateTime)<<32|int64(ft.LowDateTime)) * 100
}

func systemTimes() (idle, kernel, user time.Duration, err error) {
	var i, k, u windows.Filetime
	if r, _, e := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u))); r == 0 {
		return 0, 0, 0, e
	}
	return ticks(i), ticks(k), ticks(u), nil
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

// listProcesses is every process with what this user may read of it. Only
// PROCESS_QUERY_LIMITED_INFORMATION is asked for: it is all the times and
// counters need, and the most a user gets on a SYSTEM process.
func listProcesses() ([]procSample, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var out []procSample
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := procSample{pid: e.ProcessID, threads: e.Threads, name: windows.UTF16ToString(e.ExeFile[:])}
		readProcess(&p)
		out = append(out, p)
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		err = nil
	}
	return out, err
}

// readProcess fills in p's CPU time, memory and handles, or why it could
// not.
func readProcess(p *procSample) {
	if p.pid == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p.pid)
	if err != nil {
		p.err = err.Error()
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		p.err = err.Error()
		return
	}
	p.open, p.cpu = true, ticks(kernel)+ticks(user)
	var mc processMemoryCountersEx
	mc.cb = uint32(unsafe.Sizeof(mc))
	if r, _, _ := procK32GetProcessMemoryInf.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb)); r != 0 {
		p.workingSet, p.private = uint64(mc.workingSetSize), uint64(mc.privateUsage)
	}
	var n uint32
	if r, _, _ := procGetProcessHandleCount.Call(uintptr(h), uintptr(unsafe.Pointer(&n))); r != 0 {
		p.handles = n
	}
}

// errServiceStopped is a service that is installed but has no process.
var errServiceStopped = errors.New("not running")

// servicePID is the service's process id, asked of the SCM (any user may
// query this service's status).
func servicePID() (uint32, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseServiceHandle(m) }()
	name, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return 0, err
	}
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return 0, errors.New("not installed")
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseServiceHandle(s) }()
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need); err != nil {
		return 0, err
	}
	if st.ProcessId == 0 {
		return 0, errServiceStopped
	}
	return st.ProcessId, nil
}
