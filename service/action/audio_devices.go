package action

// Output and input device switching (#145). The commands are media
// commands (IsMedia, IsAudio): they follow media.enabled, run in the user
// session and answer with the audio block, through the same /st/v1, app
// and Telegram paths as the volume. Unlike the volume they cannot be
// turned into one argument vector up front — the slot names a device,
// which first has to be found among the active ones — so the service
// runs them itself rather than through MediaArgs.

import (
	"fmt"
	"net/http"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

// The device switch commands: value = slot 1–5 of audio.outputs or
// audio.inputs.
const (
	CommandAudioOutput = "audio_output"
	CommandAudioInput  = "audio_input"
)

// Wire codes of a refused switch, besides the media ones (media_disabled,
// no_user_session, timeout).
const (
	// AudioSlotEmpty: the slot names no device.
	AudioSlotEmpty = "slot_empty"
	// AudioDeviceMissing: no active device has that name now.
	AudioDeviceMissing = "device_missing"
	// AudioSwitchFailed: the device is there, Windows did not switch.
	AudioSwitchFailed = "switch_failed"
)

// IsAudioDevice reports whether name is a device switch command.
func IsAudioDevice(name string) bool {
	return name == CommandAudioOutput || name == CommandAudioInput
}

// AudioSlotValue checks a switch command's value: a slot 1–5.
func AudioSlotValue(value *int) (int, error) {
	if value == nil || *value < 1 || *value > config.AudioSlots {
		return 0, &ValueError{fmt.Sprintf("value must be a slot 1-%d", config.AudioSlots)}
	}
	return *value, nil
}

// AudioDeviceError is a refused switch. Code is one of the Audio* codes;
// Message is a sentence a client may show (no device id).
type AudioDeviceError struct {
	Code    string
	Message string
}

func (e *AudioDeviceError) Error() string { return e.Code + ": " + e.Message }

func (e *AudioDeviceError) failure() Failure {
	status := http.StatusBadGateway
	switch e.Code {
	case AudioSlotEmpty:
		status = http.StatusNotFound
	case AudioDeviceMissing:
		status = http.StatusConflict
	}
	return Failure{Status: status, Code: e.Code, Message: e.Message, Detail: e.Message}
}
