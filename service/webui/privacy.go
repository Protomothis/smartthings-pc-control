package webui

// The camera/microphone detection on the WebUI port (#143): the state the
// app's sharing tab shows, and the refusal a WebUI power command meets
// while the guard says "block".

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/privacy"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// ErrUnknownCommand is Commands.RunNow's answer for a name the catalogue
// does not have.
var ErrUnknownCommand = errors.New("unknown command")

// writeCommandError answers a POST /api/command that did not run:
//
//	409 {"status":"error","error":"device_in_use","devices":["camera"],...}
//	404 {"status":"error","message":"Unknown command",...}
func writeCommandError(w http.ResponseWriter, name string, err error) {
	var inUse *privacy.DeviceInUseError
	if errors.As(err, &inUse) {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"status":  "error",
			"error":   privacy.ErrorCode,
			"devices": inUse.Devices,
			"command": name,
			"message": strings.Join(inUse.Devices, ", ") + " in use: command refused",
		})
		return
	}
	httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"status": "error", "command": httpx.Truncate(name, 64), "message": "Unknown command"})
}

// PrivacyBody is GET /api/privacy: the two settings and, while enabled,
// the status block exactly as /st/v1/status shows it.
type PrivacyBody struct {
	Enabled bool            `json:"enabled"`
	Guard   string          `json:"guard"`
	Privacy *status.Privacy `json:"privacy,omitempty"`
}

// servePrivacy serves GET /api/privacy.
func (s *Server) servePrivacy(w http.ResponseWriter, r *http.Request) {
	cfg := s.d.Config()
	body := PrivacyBody{Enabled: cfg.Privacy.Enabled, Guard: cfg.Privacy.WithDefaults().Guard}
	if s.d.Privacy != nil {
		body.Privacy = s.d.Privacy(cfg)
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}
