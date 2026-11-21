package webui

// GET /api/diag/process (#175): the service's own process figures for the
// app's problem report. The tray runs as the user and is refused when it
// opens the SYSTEM service's process, so the service says them itself.
//
// The CPU is the time used so far, not a percentage: the app reads it at
// both ends of the second it measures every other process over, so the
// service's share is on the same scale as the rest of the report, and the
// endpoint never blocks for a measurement of its own.
//
// Local trusted session only, like /api/processes: a LAN caller has no
// business with the service's internals.

import (
	"net/http"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/health"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
)

// DiagProcessBody is the reply of GET /api/diag/process.
type DiagProcessBody struct {
	PID uint32 `json:"pid"`
	// CPUTimeMicros is the kernel plus user time used since the start.
	CPUTimeMicros int64  `json:"cpu_time_us"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	WorkingSet    uint64 `json:"working_set"`
	Private       uint64 `json:"private"`
	Threads       int    `json:"threads"`
	Handles       int    `json:"handles"`
	Goroutines    int    `json:"goroutines"`
}

// diagProcess reads this process; the tests replace it.
var diagProcess = health.ReadProcess

// diagNow is the clock the uptime is read against; the tests replace it.
var diagNow = time.Now

func (s *Server) serveDiagProcess(w http.ResponseWriter, r *http.Request) {
	p, err := diagProcess()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "The process figures are unavailable.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, DiagProcessBody{
		PID:           p.PID,
		CPUTimeMicros: p.CPU.Microseconds(),
		UptimeSeconds: int64(diagNow().Sub(p.Started) / time.Second),
		WorkingSet:    p.WorkingSet,
		Private:       p.Private,
		Threads:       p.Threads,
		Handles:       p.Handles,
		Goroutines:    p.Goroutines,
	})
}
