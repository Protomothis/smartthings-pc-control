package stapi

import "github.com/Protomothis/smartthings-pc-control/internal/config"

// audioDeviceFeatures are the §3 features entries of the device switches
// (#145): "audio_output" while an output slot is filled, "audio_input"
// while an input slot is, both only with media.enabled (the switches are
// media commands). The driver offers the device list only then; the slots
// themselves are status audio.outputs / audio.inputs.
func audioDeviceFeatures(cfg config.Config) []string {
	if !cfg.Media.Enabled {
		return nil
	}
	var out []string
	if config.HasAudioSlots(cfg.Audio.Outputs) {
		out = append(out, "audio_output")
	}
	if config.HasAudioSlots(cfg.Audio.Inputs) {
		out = append(out, "audio_input")
	}
	return out
}
