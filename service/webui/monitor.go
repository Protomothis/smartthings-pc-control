package webui

// /api/monitor (#144): the command tab's monitor card.
//
//	GET                                            the state
//	POST {"command":"monitor_brightness","value":40}
//	POST {"command":"monitor_input","value":"hdmi1"}
//	POST {"command":"monitor_power","value":"off"}  one command, then the state
//
// The state is {"enabled":…,"monitors":[…]} — the status block, [] while
// monitor.enabled is off — plus, after a command, "monitor": the primary
// read back. The commands, values, switch and errors are /st/v1's: 403
// monitor_disabled, 409 no_user_session, 400, 501 monitor_unsupported,
// 502 monitor_failed, 504 timeout.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// Monitor is the DDC/CI monitor control (#144).
type Monitor interface {
	// Monitors is the status block, nil while monitor.enabled is off.
	Monitors(cfg config.Config, fromCache bool) *[]status.Monitor
	Run(ctx context.Context, name, value string) (status.Monitor, error)
}

// monitorBody is the /api/monitor state.
type monitorBody struct {
	Enabled  bool             `json:"enabled"`
	Monitors []status.Monitor `json:"monitors"`
	Monitor  *status.Monitor  `json:"monitor,omitempty"`
}

func (s *Server) monitorState(cfg config.Config) monitorBody {
	out := monitorBody{Enabled: cfg.Monitor.Enabled, Monitors: []status.Monitor{}}
	if s.d.Monitor == nil {
		return out
	}
	if list := s.d.Monitor.Monitors(cfg, false); list != nil {
		out.Monitors = *list
	}
	return out
}

// serveMonitor serves /api/monitor.
func (s *Server) serveMonitor(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, s.monitorState(s.d.Config()))
		return
	}
	var body struct {
		Command string          `json:"command"`
		Value   json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !action.IsMonitor(body.Command) {
		writeAPIError(w, http.StatusBadRequest, "unknown monitor command")
		return
	}
	var (
		m   status.Monitor
		err error
	)
	if s.d.Monitor == nil || !s.d.Config().Monitor.Enabled {
		err = action.ErrMonitorDisabled
	} else {
		var value string
		if value, err = action.MonitorValueText(body.Command, body.Value); err == nil {
			m, err = s.d.Monitor.Run(r.Context(), body.Command, value)
		}
	}
	if err != nil {
		f := action.ClassifyMonitor(err)
		logx.Printf("App: %s failed: %v", body.Command, err)
		httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Detail})
		return
	}
	out := s.monitorState(s.d.Config())
	out.Monitor = &m
	httpx.WriteJSON(w, http.StatusOK, out)
}
