package stapi

import "github.com/Protomothis/smartthings-pc-control/internal/config"

// audioDeviceFeatures are the §3 features entries of the device slots
// (#145, #159): "audio_output" while an output slot is filled,
// "audio_input" while an input slot is, both only with media.enabled (the
// slots are part of the audio block). The service fills the slots itself
// as devices show up, so a PC with sound lists both soon after the first
// device reading. Nothing switches devices: the entries say that status
// audio.outputs / audio.inputs report which slot is the current device.
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
