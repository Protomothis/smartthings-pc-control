package stapi

// Monitor control over DDC/CI (#144): the "monitors" status block and the
// monitor_* commands of POST /st/v1/command.
//
//	{"command":"monitor_brightness","value":40}
//	{"command":"monitor_input","value":"hdmi1"}
//	{"command":"monitor_power","value":"off"}
//
// They act on the primary monitor. Unlike every other command their value
// can be a string, so they are told apart before the body is decoded into
// CommandRequest (whose value is a number) and decoded into
// MonitorCommandRequest instead. The reply is the usual command response
// with the monitor read back after the change:
//
//	200 {"accepted":true,"executed":true,"schedule":{…},"monitor":{…}}
//	403 {"error":"monitor_disabled"}                      monitor.enabled is off
//	409 {"error":"no_user_session"}                       nobody at the console
//	400 {"error":"value must be …"}                       bad value, minutes given
//	501 {"error":"monitor_unsupported","message":…}       no DDC/CI on the primary
//	502 {"error":"monitor_failed","message":…}            the monitor refused
//	504 {"error":"timeout"}                               no answer in time

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// MonitorControl is the DDC/CI monitor control (#144).
type MonitorControl interface {
	// Monitors is the status "monitors" block for cfg: nil while
	// monitor.enabled is off (the key is left out), the newest reading
	// otherwise — [] before the first. fromCache (the power.stopping push)
	// never starts a reading.
	Monitors(cfg config.Config, fromCache bool) *[]status.Monitor
	// Run sets one value on the primary monitor (action.MonitorArgs) and
	// returns the monitor read back.
	Run(ctx context.Context, name, value string) (status.Monitor, error)
}

// MonitorCommandRequest is the POST /st/v1/command body of a monitor
// command: Value is a number (brightness) or a string (input, power).
type MonitorCommandRequest struct {
	Command string          `json:"command"`
	Value   json.RawMessage `json:"value"`
	// Minutes is refused, as for the media commands.
	Minutes int `json:"minutes"`
}

// MonitorCommandResponse is the command response plus the monitor.
type MonitorCommandResponse struct {
	CommandResponse
	Monitor *status.Monitor `json:"monitor,omitempty"`
}

// monitorsView is the status block, nil without a monitor control.
func (s *Server) monitorsView(cfg config.Config, fromCache bool) *[]status.Monitor {
	if s.d.Monitor == nil {
		return nil
	}
	return s.d.Monitor.Monitors(cfg, fromCache)
}

// monitorFeature is the "monitor" features entry while monitor.enabled is
// on.
func monitorFeature(cfg config.Config) []string {
	if cfg.Monitor.Enabled {
		return []string{"monitor"}
	}
	return nil
}

// routeMonitor serves raw when it is a monitor command and reports whether
// it did. Anything else — another command, a body that is not JSON — is
// left to handleCommand.
func (s *Server) routeMonitor(w http.ResponseWriter, r *http.Request, raw []byte, from string) bool {
	var body MonitorCommandRequest
	if json.NewDecoder(bytes.NewReader(raw)).Decode(&body) != nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(body.Command))
	if !action.IsMonitor(name) {
		return false
	}
	s.handleMonitor(w, r, name, body, from)
	return true
}

// handleMonitor runs one monitor command. Like the media commands it is
// not a registry command: no grace period, no schedule, no last_command,
// no notification.
func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request, name string, body MonitorCommandRequest, from string) {
	if body.Minutes != 0 {
		writeError(w, http.StatusBadRequest, "minutes does not apply to "+name)
		return
	}
	var (
		m   status.Monitor
		err error
	)
	switch {
	case s.d.Monitor == nil || !s.d.Config().Monitor.Enabled:
		err = action.ErrMonitorDisabled
	default:
		var value string
		if value, err = action.MonitorValueText(name, body.Value); err == nil {
			m, err = s.d.Monitor.Run(r.Context(), name, value)
		}
	}
	if err != nil {
		f := action.ClassifyMonitor(err)
		logx.Printf("ST API: %s from %s failed: %v", name, from, err)
		if f.Detail == "" {
			writeError(w, f.Status, f.Code)
		} else {
			httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Detail})
		}
		return
	}
	logx.Printf("ST API: %s from %s", name, from)
	httpx.WriteJSON(w, http.StatusOK, MonitorCommandResponse{
		CommandResponse: CommandResponse{Accepted: true, Executed: true, Schedule: s.scheduleView()},
		Monitor:         &m,
	})
}
