package gui

// The service's own process figures for the problem report (#175): GET
// /api/diag/process. The tray may not open the SYSTEM service's process,
// so the service says them itself; an older service answers 404 and the
// report falls back to what the process list shows.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// diagTimeout is how long the report waits for the service: the figures
// are a nicety, the report must not hang on them.
const diagTimeout = 1500 * time.Millisecond

// errNoDiag is a service that predates /api/diag/process.
var errNoDiag = errors.New("not supported by this service version")

// ServiceProcess is the service's reading of its own process.
type ServiceProcess struct {
	PID        uint32 `json:"pid"`
	CPUMicros  int64  `json:"cpu_time_us"` // kernel plus user time so far
	UptimeSecs int64  `json:"uptime_seconds"`
	WorkingSet uint64 `json:"working_set"`
	Private    uint64 `json:"private"`
	Threads    int    `json:"threads"`
	Handles    int    `json:"handles"`
	Goroutines int    `json:"goroutines"`
}

// CPU is the time the service has used so far.
func (p ServiceProcess) CPU() time.Duration { return time.Duration(p.CPUMicros) * time.Microsecond }

// DiagProcess asks the service for its own process figures. It needs the
// local trusted session, like the process list.
func (c *Client) DiagProcess() (ServiceProcess, error) {
	var p ServiceProcess
	ctx, cancel := context.WithTimeout(context.Background(), diagTimeout)
	defer cancel()
	resp, err := c.doCtx(ctx, "GET", "/api/diag/process", nil)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return p, errNoDiag
	case resp.StatusCode == http.StatusUnauthorized:
		return p, errUnauthorized
	case resp.StatusCode == http.StatusForbidden && localOnlyReply(resp.Body):
		return p, errLocalOnly
	case resp.StatusCode != http.StatusOK:
		return p, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&p)
	return p, err
}
