package gui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeSnap answers the first look with before and the second with after;
// the system clock moves by 4s of CPU (four cores for one second), 1s of
// it idle.
func fakeSnap(before, after []procSample, svcPID uint32, svcErr error) snapSource {
	looks, sysLooks := 0, 0
	return snapSource{
		systemTimes: func() (time.Duration, time.Duration, time.Duration, error) {
			sysLooks++
			if sysLooks == 1 {
				return 10 * time.Second, 30 * time.Second, 20 * time.Second, nil
			}
			// idle +1s; kernel (with idle) +2s; user +2s → 4s, 75% busy
			return 11 * time.Second, 32 * time.Second, 22 * time.Second, nil
		},
		processes: func() ([]procSample, error) {
			looks++
			if looks == 1 {
				return before, nil
			}
			return after, nil
		},
		servicePID: func() (uint32, error) { return svcPID, svcErr },
		selfPID:    func() uint32 { return 100 },
		sleep:      func(time.Duration) {},
	}
}

func proc(pid uint32, name string, cpu time.Duration, ws uint64) procSample {
	return procSample{pid: pid, name: name, open: true, cpu: cpu, workingSet: ws, private: ws / 2, threads: 10, handles: 200}
}

func TestTakeSnapshot(t *testing.T) {
	before := []procSample{
		{pid: 0, name: "[System Process]"},
		proc(100, "smartthings-pc-control.exe", time.Second, 0),
		proc(200, "chrome.exe", 1*time.Second, 0),
		proc(201, "chrome.exe", 1*time.Second, 0),
		proc(300, "game.exe", 0, 0),
		{pid: 400, name: "smartthings-pc-control.exe", threads: 23, err: "Access is denied."},
		{pid: 500, name: "MsMpEng.exe", err: "Access is denied."},
	}
	after := []procSample{
		{pid: 0, name: "[System Process]"},
		proc(100, "smartthings-pc-control.exe", time.Second+40*time.Millisecond, 50<<20),
		proc(200, "chrome.exe", 1*time.Second+400*time.Millisecond, 300<<20),
		proc(201, "chrome.exe", 1*time.Second+200*time.Millisecond, 200<<20),
		proc(300, "game.exe", 2*time.Second, 2<<30),
		{pid: 400, name: "smartthings-pc-control.exe", threads: 23, err: "Access is denied."},
		{pid: 500, name: "MsMpEng.exe", err: "Access is denied."},
		proc(600, "new.exe", 5*time.Second, 1<<20), // started during the window
	}
	s := takeSnapshot(fakeSnap(before, after, 400, nil))
	if !s.CPUOK || s.CPU != 75 {
		t.Errorf("PC CPU %v %v", s.CPU, s.CPUOK)
	}
	if !s.App.CPUOK || s.App.CPU != 1 || s.App.WorkingSet != 50<<20 || s.App.Private != 25<<20 || s.App.Threads != 10 || s.App.Handles != 200 {
		t.Errorf("app %+v", s.App)
	}
	if s.Service.Err != "Access is denied." || s.Service.Threads != 23 {
		t.Errorf("service %+v", s.Service)
	}
	if s.Skipped != 2 {
		t.Errorf("skipped %d", s.Skipped)
	}
	var got []string
	for _, p := range s.Top {
		got = append(got, fmt.Sprintf("%s×%d %.0f %v", p.Name, p.Count, p.CPU, p.CPUOK))
	}
	want := "game.exe×1 50 true|chrome.exe×2 15 true|smartthings-pc-control.exe×1 1 true|new.exe×1 0 false"
	if strings.Join(got, "|") != want {
		t.Errorf("top\n got %s\nwant %s", strings.Join(got, "|"), want)
	}
}

func TestTakeSnapshotLimitsAndErrors(t *testing.T) {
	var before, after []procSample
	for i := range 15 {
		before = append(before, proc(uint32(1000+i), fmt.Sprintf("p%02d.exe", i), 0, 0))
		after = append(after, proc(uint32(1000+i), fmt.Sprintf("p%02d.exe", i), time.Duration(i)*10*time.Millisecond, 0))
	}
	s := takeSnapshot(fakeSnap(before, after, 0, errServiceStopped))
	if len(s.Top) != snapshotTop || s.Top[0].Name != "p14.exe" {
		t.Errorf("top %d, first %q", len(s.Top), s.Top[0].Name)
	}
	if s.Service.Err != "not running" || s.App.Err != "not found" {
		t.Errorf("service %+v app %+v", s.Service, s.App)
	}

	src := fakeSnap(nil, nil, 0, nil)
	src.systemTimes = func() (time.Duration, time.Duration, time.Duration, error) { return 0, 0, 0, errors.New("no") }
	src.processes = func() ([]procSample, error) { return nil, errors.New("snapshot failed") }
	s = takeSnapshot(src)
	if s.CPUOK || s.App.Err != "snapshot failed" || s.Service.Err != "snapshot failed" || len(s.Top) != 0 {
		t.Errorf("%+v", s)
	}
}

func TestSnapshotMasked(t *testing.T) {
	s := StateSnapshot{App: ProcStat{Name: "jdoe-tool.exe"}, Top: []ProcStat{{Name: "jdoe.exe"}, {Name: "chrome.exe"}}}
	m := s.Masked(NewMasker(MaskInput{Names: []string{"jdoe"}}))
	if m.App.Name != "<name>-tool.exe" || m.Top[0].Name != "<name>.exe" || m.Top[1].Name != "chrome.exe" {
		t.Errorf("%+v", m)
	}
	if s.Top[0].Name != "jdoe.exe" {
		t.Error("the original changed")
	}
}

// The real source measures this PC: only that it answers sensibly.
func TestTakeStateSnapshotReal(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a second")
	}
	s := TakeStateSnapshot(nil)
	t.Logf("cpu %.1f%% service %+v skipped %d top %d", s.CPU, s.Service, s.Skipped, len(s.Top))
	if !s.CPUOK || s.App.Err != "" || s.App.WorkingSet == 0 || s.App.Threads == 0 || len(s.Top) == 0 {
		t.Errorf("%+v", s)
	}
}
