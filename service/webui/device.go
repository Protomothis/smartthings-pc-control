package webui

// The desktop app's device endpoints: keep-awake (#111), battery (#112),
// the media card (#117), the running-program picker (#110), the test
// notification (#106) and the preset buttons (#109).

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
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// ---- /api/awake (#111) -----------------------------------------------------

// AwakeBody is GET/POST/DELETE /api/awake: the state plus what the app's
// command tab needs to draw it. RemainingSeconds is 0 while off and while
// on until turned off.
type AwakeBody struct {
	Status           string `json:"status"`
	On               bool   `json:"on"`
	Until            string `json:"until"`
	RemainingSeconds int    `json:"remaining_seconds"`
	DefaultMinutes   int    `json:"default_minutes"`
	KeepDisplay      bool   `json:"keep_display"`
}

func awakeBody(v status.AwakeView, cfg config.Config, now time.Time) AwakeBody {
	a := cfg.Awake.WithDefaults()
	out := AwakeBody{
		Status:         "ok",
		On:             v.On,
		Until:          v.Wire().Until,
		DefaultMinutes: a.DefaultMinutes,
		KeepDisplay:    a.KeepDisplay,
	}
	if v.On && !v.Until.IsZero() {
		if left := v.Until.Sub(now); left > 0 {
			out.RemainingSeconds = int(left.Round(time.Second) / time.Second)
		}
	}
	return out
}

// serveAwake serves /api/awake on the WebUI port, behind the same session
// and CSRF checks as /api/schedule:
//
//	GET                         the state
//	POST {"minutes": n}         turn on for n minutes (0 = until turned off,
//	                            key absent = awake.default_minutes)
//	DELETE                      turn off
func (s *Server) serveAwake(w http.ResponseWriter, r *http.Request) {
	liveCfg := s.d.Config()
	ctl := s.d.Awake
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, awakeBody(ctl.View(), liveCfg, ctl.Now()))
		return
	}
	var (
		view status.AwakeView
		err  error
	)
	if r.Method == http.MethodDelete {
		view, _, err = ctl.TurnOff()
	} else {
		var body struct {
			Minutes *int `json:"minutes"`
		}
		if r.Body != nil {
			if derr := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); derr != nil && derr != io.EOF {
				writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
				return
			}
		}
		minutes := s.d.Config().Awake.Period(body.Minutes)
		if !config.ValidAwakeMinutes(minutes) {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("Minutes must be between 0 and %d", config.AwakeMaxMinutes))
			return
		}
		view, err = ctl.TurnOn(minutes)
	}
	if err != nil {
		logx.Printf("Keep-awake via app failed: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "Keep-awake failed: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, awakeBody(view, s.d.Config(), ctl.Now()))
}

// ---- /api/battery (#112) ---------------------------------------------------

// serveBattery serves GET /api/battery for the app's status bar.
func (s *Server) serveBattery(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.d.Status.Battery())
}

// ---- /api/media (#117) -----------------------------------------------------

// MediaBody is GET /api/media and the reply of POST /api/media: the two
// switches, whether anyone is logged in, and the audio and media blocks
// exactly as /st/v1/status shows them.
type MediaBody struct {
	Enabled    bool         `json:"enabled"`
	NowPlaying bool         `json:"now_playing"`
	Session    bool         `json:"session"`
	Audio      status.Audio `json:"audio"`
	Media      status.Media `json:"media"`
}

func (s *Server) mediaBody(cfg config.Config) MediaBody {
	return MediaBody{
		Enabled:    cfg.Media.Enabled,
		NowPlaying: cfg.Media.NowPlaying,
		Session:    s.d.Media.SessionPresent(),
		Audio:      s.d.Media.Audio(cfg),
		Media:      s.d.Media.Media(cfg),
	}
}

// serveMedia serves /api/media for the command tab's media card:
//
//	GET                                 the state
//	POST {"command":"next"}             one media command, then the state
//	POST {"command":"volume","value":30}
//
// The commands are the /st/v1 ones (action.IsMedia) with the same ranges,
// switch and errors: 403 media_disabled, 409 no_user_session, 400, 501
// unsupported, 502 failed, 504 timeout.
func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, s.mediaBody(s.d.Config()))
		return
	}
	var body struct {
		Command string `json:"command"`
		Value   *int   `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !action.IsMedia(body.Command) {
		writeAPIError(w, http.StatusBadRequest, "unknown media command")
		return
	}
	res, err := s.d.Media.Run(r.Context(), body.Command, body.Value)
	if err != nil {
		f := action.Classify(err)
		logx.Printf("App: %s failed: %v", body.Command, err)
		httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Detail})
		return
	}
	view := s.mediaBody(s.d.Config())
	if res.Audio != nil {
		// The reply's own reading: the store may already hold a newer
		// heartbeat, but the caller asked about this command.
		view.Audio = s.d.Media.AudioView(*res.Audio)
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// ---- /api/processes (#110) -------------------------------------------------

// serveProcesses serves GET /api/processes for the desktop app's "pick
// from running programs" dialog. The route is localOnly: the WebUI may be
// open to the LAN (webui_remote), and the process list is meant for this
// PC's screen only — a secret login is not enough, the local trusted
// session (loopback only) is. Nothing is logged about the names.
func (s *Server) serveProcesses(w http.ResponseWriter, r *http.Request) {
	names, err := s.d.Status.Processes()
	if err != nil {
		logx.Printf("Activity: process list for the app unavailable: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "The process list is unavailable.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]string{"processes": names})
}

// ---- /api/notify/test (#106) -----------------------------------------------

// serveNotifyTest serves POST /api/notify/test for the app's [테스트 알림]
// button. The enabled switch is ignored (testing is how the user decides),
// the rate limit is not.
func (s *Server) serveNotifyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil && err != io.EOF {
			writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
	}
	text := body.Text
	if strings.TrimSpace(text) == "" {
		text = "PC Control 테스트 알림입니다 · This is a test notification"
	}
	// Enabled: true stands in for "do not check the switch".
	res, err := s.d.Notify.Send(r.Context(), config.NotifyPCConfig{Enabled: true}, "app", body.Title, text)
	if err != nil {
		status, code, msg := action.Status(err)
		httpx.WriteJSON(w, status, map[string]string{"status": "error", "error": code, "message": msg})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
		action.NotifyResult
	}{"ok", res})
}

// ---- /api/presets (#109) ---------------------------------------------------

// servePresetsRun serves POST /api/presets/run {slot} — the command tab's
// [실행] buttons, which run a saved preset.
func (s *Server) servePresetsRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slot int `json:"slot"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	p, ok := config.FindPreset(s.d.Config().Presets, body.Slot)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"status": "error", "error": "no_such_preset",
			"message": fmt.Sprintf("slot %d has no preset", body.Slot)})
		return
	}
	writePresetResult(w, p, s.d.Presets.Run(r.Context(), p, "app"), nil)
}

// servePresetsTest serves POST /api/presets/test {slot, name, type, path,
// args} — the editor's [테스트] button, which runs the row as typed, before
// it is saved. It passes the same validation a save does, is local-only
// (the route), and carries the writable_by_others warnings a save would.
func (s *Server) servePresetsTest(w http.ResponseWriter, r *http.Request) {
	var p config.Preset
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if p.Slot == 0 {
		p.Slot = config.PresetMinSlot // an unsaved row may not have one yet
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "test"
	}
	if err := config.ValidatePreset(p); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	warnings := s.presetWarnings([]config.Preset{p})
	writePresetResult(w, p, s.d.Presets.Run(r.Context(), p, "app test"), warnings)
}

// writePresetResult answers a preset run. A failure's message is
// action.PresetFailure's: no path, no URL (the run endpoint is reachable
// from a remote WebUI login). warnings, when there are any, ride along
// either way.
func writePresetResult(w http.ResponseWriter, p config.Preset, err error, warnings []PresetWarning) {
	if err != nil {
		f, _ := action.PresetFailure(err, p)
		body := map[string]any{"status": "error", "error": f.Code, "message": f.Message}
		if len(warnings) > 0 {
			body["warnings"] = warnings
		}
		httpx.WriteJSON(w, f.Status, body)
		return
	}
	body := map[string]any{"status": "ok", "started": true, "slot": p.Slot, "name": p.Name}
	if len(warnings) > 0 {
		body["warnings"] = warnings
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}
