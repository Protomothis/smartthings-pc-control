package action

// Volume, mute and media keys (#104, #105; docs/design/media-notify.md §3,
// §6). SmartThings, the app and Telegram share these names and MediaArgs,
// so all of them apply the same ranges.

import (
	"fmt"
	"strconv"
)

// DefaultVolumeStep is volumeup/volumedown without a value (§3), and the
// Windows volume keys' own step.
const DefaultVolumeStep = 5

// mediaKinds are the /st/v1 command names, true for the audio ones (their
// reply carries the new audio state), false for the media keys.
var mediaKinds = map[string]bool{
	"volume":     true,
	"volumeup":   true,
	"volumedown": true,
	"mute":       true,
	"unmute":     true,
	"playpause":  false,
	"play":       false,
	"pause":      false,
	"stop":       false,
	"next":       false,
	"prev":       false,
}

// IsMedia reports whether name is a volume, mute or media-key command.
func IsMedia(name string) bool {
	_, ok := mediaKinds[name]
	return ok
}

// IsAudio reports whether name is one of the audio commands, whose reply
// carries the audio state after the change (volume…unmute).
func IsAudio(name string) bool { return mediaKinds[name] }

// MediaArgs builds the user-action argument vector for a command:
//
//	volume      value 0–100, required
//	volumeup    value 1–100, default 5
//	volumedown  value 1–100, default 5
//	mute        audio mute on      (value ignored)
//	unmute      audio mute off
//	play…prev   media <key>        (the session tells play from pause, #117)
func MediaArgs(name string, value *int) ([]string, error) {
	switch name {
	case "volume":
		if value == nil {
			return nil, &ValueError{"volume needs a value 0-100"}
		}
		if *value < 0 || *value > 100 {
			return nil, &ValueError{"value must be between 0 and 100"}
		}
		return []string{"audio", "set", strconv.Itoa(*value)}, nil
	case "volumeup", "volumedown":
		step := DefaultVolumeStep
		if value != nil {
			step = *value
		}
		if step < 1 || step > 100 {
			return nil, &ValueError{"value must be between 1 and 100"}
		}
		sign := "+"
		if name == "volumedown" {
			sign = "-"
		}
		return []string{"audio", "step", sign + strconv.Itoa(step)}, nil
	case "mute":
		return []string{"audio", "mute", "on"}, nil
	case "unmute":
		return []string{"audio", "mute", "off"}, nil
	case "playpause", "play", "pause", "stop", "next", "prev":
		return []string{"media", name}, nil
	}
	return nil, fmt.Errorf("not a media command: %q", name)
}
