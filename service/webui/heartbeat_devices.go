package webui

import (
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// heartbeatAudioDevices is the heartbeat's device block (#145): the active
// playback and recording devices by name, the default marked, with the
// sampled_at rule of the audio block. The tray app sends it with the 30s
// heartbeat; ids stay on the PC (the service asks the user session for
// them when it switches).
type heartbeatAudioDevices struct {
	Outputs   []useraction.AudioEndpoint `json:"outputs"`
	Inputs    []useraction.AudioEndpoint `json:"inputs"`
	SampledAt string                     `json:"sampled_at,omitempty"`
}

// check validates the block and returns its sample time; msg is the 400
// message of a bad block, "" when it is fine (or absent: d is nil).
func (d *heartbeatAudioDevices) check(received time.Time) (at time.Time, msg string) {
	if d == nil {
		return time.Time{}, ""
	}
	for _, list := range [][]useraction.AudioEndpoint{d.Outputs, d.Inputs} {
		if err := useraction.ValidateEndpoints(list); err != nil {
			return time.Time{}, "audio_devices: " + err.Error()
		}
	}
	at, ok := heartbeatSampleTime(d.SampledAt, received)
	if !ok {
		return time.Time{}, "audio_devices: sampled_at is not RFC3339"
	}
	if d.Outputs == nil {
		d.Outputs = []useraction.AudioEndpoint{}
	}
	if d.Inputs == nil {
		d.Inputs = []useraction.AudioEndpoint{}
	}
	return at, ""
}
