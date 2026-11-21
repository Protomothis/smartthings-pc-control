package gui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func diagClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{base: srv.URL, http: srv.Client()}
}

func TestClientDiagProcess(t *testing.T) {
	c := diagClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/diag/process" || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"pid":4321,"cpu_time_us":12000345,"uptime_seconds":5400,"working_set":31457280,"private":20971520,"threads":21,"handles":310,"goroutines":57}`))
	})
	p, err := c.DiagProcess()
	if err != nil {
		t.Fatal(err)
	}
	want := ServiceProcess{PID: 4321, CPUMicros: 12000345, UptimeSecs: 5400, WorkingSet: 30 << 20, Private: 20 << 20, Threads: 21, Handles: 310, Goroutines: 57}
	if p != want {
		t.Errorf("got %+v", p)
	}
	if p.CPU() != 12*time.Second+345*time.Microsecond {
		t.Errorf("cpu %v", p.CPU())
	}
}

func TestClientDiagProcessErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		h    http.HandlerFunc
		want error
	}{
		{"older service", http.NotFound, errNoDiag},
		{"no session", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, errUnauthorized},
		{"not local", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"local_only"}`))
		}, errLocalOnly},
	} {
		if _, err := diagClient(t, c.h).DiagProcess(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := diagClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }).DiagProcess(); err == nil {
		t.Error("a 500 is an error")
	}
	if _, err := diagClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{")) }).DiagProcess(); err == nil {
		t.Error("bad JSON is an error")
	}
}

// A service that never answers is given up on quickly.
func TestClientDiagProcessTimesOut(t *testing.T) {
	done := make(chan struct{})
	c := diagClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	})
	defer close(done)
	start := time.Now()
	if _, err := c.DiagProcess(); err == nil {
		t.Fatal("no error from a silent service")
	}
	if d := time.Since(start); d > diagTimeout+time.Second {
		t.Errorf("took %v", d)
	}
}

// With the service's own figures the service row is filled, its CPU the
// time used between the two asks on the whole-PC scale.
func TestTakeSnapshotServiceFigures(t *testing.T) {
	after := []procSample{proc(100, "smartthings-pc-control.exe", 0, 0),
		{pid: 400, name: "smartthings-pc-control.exe", threads: 23, err: "Access is denied."}}
	src := fakeSnap(after, after, 400, nil)
	asks := 0
	src.service = func() (ServiceProcess, error) {
		asks++
		cpu := int64(2_000_000)
		if asks == 2 {
			cpu += 200_000 // 0.2s of the window's 4s of CPU: 5%
		}
		return ServiceProcess{PID: 400, CPUMicros: cpu, UptimeSecs: 3 * 3600, WorkingSet: 30 << 20, Private: 20 << 20, Threads: 21, Handles: 310, Goroutines: 57}, nil
	}
	s := takeSnapshot(src)
	sv := s.Service
	if asks != 2 || sv.Err != "" || !sv.CPUOK || sv.CPU != 5 || sv.WorkingSet != 30<<20 || sv.Private != 20<<20 ||
		sv.Threads != 21 || sv.Handles != 310 || sv.Goroutines != 57 || sv.Uptime != 3*time.Hour {
		t.Errorf("asks %d service %+v", asks, sv)
	}
	r := Report{State: ReportState{Lang: LangKo, Snapshot: &s}, Include: ReportInclude{PCInfo: true}}
	want := "| 서비스 | CPU 5.0% · 메모리 30.0 MB (전용 20.0 MB) · 스레드 21 · 핸들 310 · 고루틴 57 · 켜진 지 3시간 0분 |"
	if sec := r.StateSection(10); !strings.Contains(sec, want) {
		t.Errorf("section lacks %q:\n%s", want, sec)
	}

	// Only the second ask answered: figures without a CPU share.
	asks = 0
	src = fakeSnap(after, after, 400, nil)
	src.service = func() (ServiceProcess, error) {
		asks++
		if asks == 1 {
			return ServiceProcess{}, errors.New("timeout")
		}
		return ServiceProcess{PID: 400, Threads: 21}, nil
	}
	if sv := takeSnapshot(src).Service; sv.CPUOK || sv.Threads != 21 || sv.Err != "" {
		t.Errorf("one answer: %+v", sv)
	}

	// The service does not answer (older, or a refusal): the process
	// list's row stays.
	src = fakeSnap(after, after, 400, nil)
	src.service = func() (ServiceProcess, error) { return ServiceProcess{}, errNoDiag }
	if sv := takeSnapshot(src).Service; sv.Err != "Access is denied." || sv.Threads != 23 {
		t.Errorf("fallback: %+v", sv)
	}
}
