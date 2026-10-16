package config

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// AudioSlots is how many device slots each direction has (#145): the
// SmartThings list is a fixed enum, so the devices sit in numbered slots
// the user fills on the PC.
const AudioSlots = 5

// AudioSlotNameRunes bounds a slot's device name, the same limit the
// user-action replies apply to a device's friendly name.
const AudioSlotNameRunes = 128

// AudioConfig is the "audio" object in config.json (#145). Outputs and
// Inputs are device names by slot: index 0 is slot 1, "" an empty slot.
// Names, not endpoint ids: an id changes when a driver is reinstalled or
// a USB device moves to another port, the name the user picked does not.
type AudioConfig struct {
	Outputs []string `json:"outputs"`
	Inputs  []string `json:"inputs"`
}

// WithDefaults normalises both lists (NormalizeAudioSlots); never nil.
func (a AudioConfig) WithDefaults() AudioConfig {
	a.Outputs = NormalizeAudioSlots(a.Outputs)
	a.Inputs = NormalizeAudioSlots(a.Inputs)
	return a
}

// NormalizeAudioSlots cleans one slot list: control characters dropped,
// spaces trimmed, a name cut at AudioSlotNameRunes, at most AudioSlots
// entries, no trailing empty slots. Empty slots in between stay — they
// keep the slots after them at their numbers. The result is a new slice,
// never nil.
func NormalizeAudioSlots(names []string) []string {
	out := make([]string, 0, AudioSlots)
	for i, n := range names {
		if i == AudioSlots {
			break
		}
		n = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, n))
		if utf8.RuneCountInString(n) > AudioSlotNameRunes {
			n = strings.TrimSpace(string([]rune(n)[:AudioSlotNameRunes]))
		}
		out = append(out, n)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// AudioSlotName is the device name in slot (1-based) of names, "" for an
// empty slot or one out of range.
func AudioSlotName(names []string, slot int) string {
	if slot < 1 || slot > len(names) || slot > AudioSlots {
		return ""
	}
	return names[slot-1]
}

// HasAudioSlots reports whether any slot of names is filled.
func HasAudioSlots(names []string) bool {
	for _, n := range names {
		if strings.TrimSpace(n) != "" {
			return true
		}
	}
	return false
}
