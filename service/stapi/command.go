package stapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// ---- command (§3.3) --------------------------------------------------------

// CommandRequest is the POST /st/v1/command body.
type CommandRequest struct {
	Command string `json:"command"`
	Mode    string `json:"mode"`    // "default" | "immediate" | "grace"
	Minutes int    `json:"minutes"` // > 0 schedules instead of running
	// Value is the argument of the v1.2.0 commands that take one
	// (media-notify.md §3): the period in minutes for awake. nil when the
	// key is absent, which is not the same as 0.
	Value *int `json:"value"`
}

// CommandResponse is the POST /st/v1/command answer.
type CommandResponse struct {
	Accepted bool           `json:"accepted"`
	Executed bool           `json:"executed"`
	Schedule map[string]any `json:"schedule"`
	// Awake is the new keep-awake state, on the awake/awakeoff replies only.
	Awake *status.Awake `json:"awake,omitempty"`
	// Audio is the audio state after a volume/mute command (#104).
	Audio *status.Audio `json:"audio,omitempty"`
	// Preset is the slot that was started, on the preset reply only (#109).
	Preset *PresetRun `json:"preset,omitempty"`
}

// PresetRun is the "preset" block of a preset command's reply.
type PresetRun struct {
	Slot    int    `json:"slot"`
	Name    string `json:"name"`
	Started bool   `json:"started"`
}

// handleAwake runs the keep-awake commands (#111, §12):
//
//	awake     value = minutes, 0 = until turned off, absent = awake.default_minutes
//	awakeoff  value ignored
//
// They are not registry commands: they take an argument, never go through
// the grace period or a schedule, and are no power command, so neither
// last_command nor a Telegram notification records them. The hub hears
// about the change through the awake.changed push.
func (s *Server) handleAwake(w http.ResponseWriter, name string, body CommandRequest, from string) {
	if body.Minutes != 0 {
		writeError(w, http.StatusBadRequest, "minutes does not apply to "+name+"; use value")
		return
	}
	var (
		view status.AwakeView
		err  error
	)
	if name == "awakeoff" {
		view, _, err = s.d.Awake.TurnOff()
	} else {
		minutes := s.d.Config().Awake.Period(body.Value)
		if !config.ValidAwakeMinutes(minutes) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("value must be between 0 and %d", config.AwakeMaxMinutes))
			return
		}
		view, err = s.d.Awake.TurnOn(minutes)
	}
	if err != nil {
		logx.Printf("ST API: %s from %s failed: %v", name, from, err)
		writeError(w, http.StatusInternalServerError, "keep-awake failed")
		return
	}
	logx.Printf("ST API: %s from %s", name, from)
	wire := view.Wire()
	httpx.WriteJSON(w, http.StatusOK, CommandResponse{
		Accepted: true,
		Executed: true,
		Schedule: s.scheduleView(),
		Awake:    &wire,
	})
}

// handleCommand serves POST /st/v1/command. It goes through the same
// registry, grace rules and events as the legacy path so Telegram keeps
// reporting SmartThings commands.
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body CommandRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from := httpx.RemoteHost(r.RemoteAddr)
	name := strings.ToLower(strings.TrimSpace(body.Command))
	switch name {
	case "awake", "awakeoff":
		s.handleAwake(w, name, body, from)
		return
	case "preset":
		s.handlePreset(w, r, body, from)
		return
	}
	if action.IsMedia(name) {
		s.handleMedia(w, r, name, body, from)
		return
	}
	if !s.d.Commands.Known(name) {
		logx.Printf("ST API: unknown command from %s", from)
		s.d.Emit("security", "unknown_command", map[string]string{"from": from, "command": httpx.Truncate(name, 64)})
		writeError(w, http.StatusBadRequest, "unknown command")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	switch mode {
	case "", "default", "immediate", "grace":
	default:
		writeError(w, http.StatusBadRequest, "unknown mode")
		return
	}
	if body.Minutes < 0 || body.Minutes > MaxMinutes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("minutes must be between 0 and %d", MaxMinutes))
		return
	}
	// An explicit delay is a schedule the user set from their phone, so it
	// carries its own origin and needs no tray toast (§3.3). It is still
	// the last command, like one that runs now (Dispatch).
	if body.Minutes > 0 {
		s.d.Commands.Record(name, from)
		delay := time.Duration(body.Minutes) * time.Minute
		if err := s.d.Commands.Schedule(name, delay); err != nil {
			logx.Printf("ST API: scheduling %s failed: %v", name, err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		logx.Printf("ST API: %s scheduled in %s (from %s)", name, power.FormatDelay(delay), from)
		httpx.WriteJSON(w, http.StatusOK, CommandResponse{Accepted: true, Schedule: s.scheduleView()})
		return
	}

	// No delay: run now, or defer by the configured grace period so the
	// user at the PC can cancel. forceshutdown is always immediate (§3.3).
	dm := ModeDefault
	switch mode {
	case "immediate":
		dm = ModeImmediate
	case "grace":
		dm = ModeGrace
	}
	if deferred := s.d.Commands.Dispatch(name, from, dm); deferred > 0 {
		httpx.WriteJSON(w, http.StatusOK, CommandResponse{Accepted: true, Schedule: s.scheduleView()})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, CommandResponse{
		Accepted: true,
		Executed: true,
		Schedule: map[string]any{"active": false},
	})
}

// handleMedia runs one volume, mute or media-key command from
// /st/v1/command. The reply is the usual command response plus, for the
// audio commands, the audio block after the change:
//
//	403 {"error":"media_disabled"}             media.enabled is off
//	409 {"error":"no_user_session"}            nobody is logged in
//	400 {"error":"..."}                        value out of range, minutes given
//	501 {"error":"unsupported","message":...}  no playback device
//	502 {"error":"failed","message":...}       the action did not work
//	504 {"error":"timeout"}                    no answer within 3s
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request, name string, body CommandRequest, from string) {
	if body.Minutes != 0 {
		writeError(w, http.StatusBadRequest, "minutes does not apply to "+name)
		return
	}
	res, err := s.d.Media.Run(r.Context(), name, body.Value)
	if err != nil {
		f := action.Classify(err)
		logx.Printf("ST API: %s from %s failed: %v", name, from, err)
		if f.Detail == "" {
			writeError(w, f.Status, f.Code)
		} else {
			httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Detail})
		}
		return
	}
	logx.Printf("ST API: %s from %s", name, from)
	resp := CommandResponse{Accepted: true, Executed: true, Schedule: s.scheduleView()}
	if action.IsAudio(name) && res.Audio != nil {
		// The reply's own reading, stamped now: the store may hold it or
		// a newer heartbeat, and the caller asked about this command.
		view := s.d.Media.AudioView(*res.Audio)
		resp.Audio = &view
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handlePreset runs the preset command (§10): value = slot number. It is
// not a registry command — it takes an argument and never goes through the
// grace period or a schedule. The attempt is recorded as last_command
// (with the slot, the name and the outcome); a start is also reported as
// remote.received, since it launched something on this PC.
func (s *Server) handlePreset(w http.ResponseWriter, r *http.Request, body CommandRequest, from string) {
	if body.Minutes != 0 {
		writeError(w, http.StatusBadRequest, "minutes does not apply to preset")
		return
	}
	if body.Value == nil || *body.Value < config.PresetMinSlot || *body.Value > config.PresetMaxSlot {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("value must be a preset slot %d-%d", config.PresetMinSlot, config.PresetMaxSlot))
		return
	}
	slot := *body.Value
	p, ok := config.FindPreset(s.d.Config().Presets, slot)
	if !ok {
		logx.Printf("ST API: preset %d from %s: no such preset", slot, from)
		httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_preset",
			"message": fmt.Sprintf("slot %d has no preset", slot)})
		return
	}
	err := s.d.Presets.Run(r.Context(), p, "smartthings "+from)
	s.d.Presets.Record(p, from, action.ResultCode(err))
	if err != nil {
		action.WriteError(w, err)
		return
	}
	// Only a start is "executed"; a refusal is in last_command and the log.
	s.d.Emit("remote", "received", map[string]string{
		"command": httpx.Truncate(fmt.Sprintf("preset %d (%s)", p.Slot, p.Name), 64),
		"from":    from,
	})
	httpx.WriteJSON(w, http.StatusOK, CommandResponse{
		Accepted: true,
		Executed: true,
		Schedule: s.scheduleView(),
		Preset:   &PresetRun{Slot: p.Slot, Name: p.Name, Started: true},
	})
}

// ---- schedule (§3.4) -------------------------------------------------------

// handleSchedule serves DELETE /st/v1/schedule.
func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cancelled := s.d.Commands.Cancel("smartthings")
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"cancelled": cancelled})
}

// ---- notify (#106) ---------------------------------------------------------

// NotifyRequest is the POST /st/v1/notify body. A "speak" field from an
// older driver is ignored like any unknown key.
type NotifyRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// NotifyResponse is the 200 answer: the action.NotifyResult plus ok.
type NotifyResponse struct {
	OK bool `json:"ok"`
	action.NotifyResult
}

// handleNotify serves POST /st/v1/notify (media-notify.md §3).
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body NotifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from := httpx.RemoteHost(r.RemoteAddr)
	res, err := s.d.Notify.Send(r.Context(), s.d.Config().NotifyPC, "ip "+from, body.Title, body.Text)
	if err != nil {
		action.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, NotifyResponse{OK: true, NotifyResult: res})
}
