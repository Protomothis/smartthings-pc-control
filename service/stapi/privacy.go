package stapi

import (
	"errors"
	"net/http"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/privacy"
)

// DeviceInUseBody is the 409 a power command meets while the camera or
// the microphone is in use and privacy.guard is "block" (#143):
//
//	409 {"error":"device_in_use","devices":["camera"]}
//
// devices lists "camera" and/or "microphone", never an app.
type DeviceInUseBody struct {
	Error   string   `json:"error"`
	Devices []string `json:"devices"`
}

// writeDispatchError answers a command Dispatch did not run.
func writeDispatchError(w http.ResponseWriter, name, from string, err error) {
	var inUse *privacy.DeviceInUseError
	if errors.As(err, &inUse) {
		logx.Printf("ST API: %s from %s refused: device in use", name, from)
		httpx.WriteJSON(w, http.StatusConflict, DeviceInUseBody{Error: privacy.ErrorCode, Devices: inUse.Devices})
		return
	}
	logx.Printf("ST API: %s from %s failed: %v", name, from, err)
	writeError(w, http.StatusBadRequest, err.Error())
}
