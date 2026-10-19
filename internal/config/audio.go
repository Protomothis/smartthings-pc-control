package config

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// AudioSlots is how many device slots each direction has (#145): the
// SmartThings list is a fixed enum, so the devices sit in numbered slots
// that routines compare against ("current output is slot 2").
const AudioSlots = 5

// AudioSlotNameRunes bounds a slot's device name, the same limit the
// user-action replies apply to a device's friendly name.
const AudioSlotNameRunes = 128

// AudioConfig is the "audio" object in config.json (#145). Outputs and
// Inputs are device names by slot: index 0 is slot 1, "" an empty slot.
// Names, not endpoint ids: an id changes when a driver is reinstalled or
// a USB device moves to another port, the name does not.
//
// Since #159 the service fills the slots itself (AssignAudioSlots): a
// device seen for the first time takes the lowest free slot and keeps it,
// connected or not, until the app's reset empties both lists.
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
		out = append(out, normalizeSlotName(n))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// normalizeSlotName is one name in NormalizeAudioSlots' form.
func normalizeSlotName(n string) string {
	n = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, n))
	if utf8.RuneCountInString(n) > AudioSlotNameRunes {
		n = strings.TrimSpace(string([]rune(n)[:AudioSlotNameRunes]))
	}
	return n
}

// SameAudioDevice matches a slot's name with a device's friendly name:
// spaces around and letter case do not count.
func SameAudioDevice(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// AssignAudioSlots puts every device of devices that has no slot in names
// into the lowest free slot — an empty one in between first, then the next
// one at the end — while one of the AudioSlots is free (#159). Devices are
// taken in the order given (the caller puts the default first); an empty
// name, or one already slotted, is skipped. A slotted name whose device is
// gone keeps its slot. changed reports whether anything was added; the
// result is a new, normalised slice either way.
func AssignAudioSlots(names, devices []string) (out []string, changed bool) {
	out = NormalizeAudioSlots(names)
	for _, d := range devices {
		d = normalizeSlotName(d)
		if d == "" || slotOf(out, d) >= 0 {
			continue
		}
		free := slotOf(out, "")
		switch {
		case free >= 0:
			out[free] = d
		case len(out) < AudioSlots:
			out = append(out, d)
		default:
			return out, changed
		}
		changed = true
	}
	return out, changed
}

// slotOf is the index of name in names, -1 when absent. "" finds the
// first empty slot.
func slotOf(names []string, name string) int {
	for i, n := range names {
		if (name == "" && n == "") || (name != "" && SameAudioDevice(n, name)) {
			return i
		}
	}
	return -1
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
