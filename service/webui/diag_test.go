package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/health"
)

func fakeDiag(t *testing.T, p health.Process, err error) {
	t.Helper()
	savedRead, savedNow := diagProcess, diagNow
	t.Cleanup(func() { diagProcess, diagNow = savedRead, savedNow })
	diagProcess = func() (health.Process, error) { return p, err }
	diagNow = func() time.Time { return p.Started.Add(90 * time.Minute) }
}

func TestDiagProcessJSON(t *testing.T) {
	fakeDiag(t, health.Process{
		PID: 4321, CPU: 12*time.Second + 345*time.Microsecond, Started: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC),
		WorkingSet: 30 << 20, Private: 20 << 20,
		Sample: health.Sample{Goroutines: 57, Threads: 21, Handles: 310},
	}, nil)
	s := newTestServer(t, config.Config{Port: 5001})
	w := httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("GET", "/api/diag/process", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got DiagProcessBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := DiagProcessBody{PID: 4321, CPUTimeMicros: 12000345, UptimeSeconds: 5400, WorkingSet: 30 << 20, Private: 20 << 20, Threads: 21, Handles: 310, Goroutines: 57}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestDiagProcessRefusals(t *testing.T) {
	fakeDiag(t, health.Process{Started: time.Now()}, nil)
	s := newTestServer(t, config.Config{Port: 5001})

	// From the LAN, even with the local session's cookie: refused.
	r := localRequest(s, httptest.NewRequest("GET", "/api/diag/process", nil))
	r.RemoteAddr = "192.168.1.30:50000"
	w := httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || w.Body.String() == "" {
		t.Errorf("LAN: %d %s", w.Code, w.Body.String())
	}

	// Loopback without the local session: refused as local_only.
	r = httptest.NewRequest("GET", "/api/diag/process", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	w = httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, r)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusForbidden || body["error"] != "local_only" {
		t.Errorf("loopback: %d %s", w.Code, w.Body.String())
	}

	// Only GET.
	w = httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("POST", "/api/diag/process", nil)))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}

	// A failed read is a 500, not half a reply.
	fakeDiag(t, health.Process{}, errors.New("no"))
	w = httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("GET", "/api/diag/process", nil)))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("failed read: %d", w.Code)
	}
}
