package useraction

// The audio handler (#104): `user-action audio get|set|step|mute` on the
// default playback device (eRender/eConsole), through the Core Audio
// binding in coreaudio.go. Every reply carries the resulting state,
//
//	{"ok":true,"audio":{"volume":30,"muted":false,"device":"스피커"}}
//
// read back from the device after the change rather than echoed from the
// request, so the service stores what Windows actually did.
//
// ReadAudio is the same getter for in-process use: the tray app calls it
// for the audio block of its heartbeat.

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// audioEndpoint is the part of IAudioEndpointVolume the handler uses,
// behind an interface so the verbs can be tested without a sound card.
type audioEndpoint interface {
	// Volume is the master level as a scalar 0.0..1.0.
	Volume() (float32, error)
	SetVolume(level float32) error
	Muted() (bool, error)
	SetMuted(on bool) error
	// Device is the friendly name, "" when unknown.
	Device() string
	Close()
}

var (
	// openAudio opens the default playback endpoint; tests replace it.
	openAudio = openDefaultEndpoint
	// runCOM runs f where COM is usable; tests replace it with a plain call.
	runCOM = withCOM
)

func init() {
	Register(ActionAudio, audioHandler)
}

// audioHandler serves `user-action audio …`.
func audioHandler(req Request) (map[string]any, error) {
	a, err := withAudio(func(ep audioEndpoint) (Audio, error) { return applyAudio(ep, req) })
	if err != nil {
		return nil, err
	}
	return map[string]any{"audio": a}, nil
}

// ReadAudio returns the default playback device's current state. The error
// is a *Error: unsupported when there is no playback device, failed when a
// Core Audio call did not work.
func ReadAudio() (Audio, error) {
	return withAudio(readAudio)
}

// withAudio opens the endpoint inside a COM scope, runs f and maps the
// failure to a user-action code.
func withAudio(f func(audioEndpoint) (Audio, error)) (Audio, error) {
	var out Audio
	err := runCOM(func() error {
		ep, err := openAudio()
		if err != nil {
			return err
		}
		defer ep.Close()
		out, err = f(ep)
		return err
	})
	switch {
	case err == nil:
		return out, nil
	case errors.Is(err, errNoPlaybackDevice):
		return Audio{}, Unsupported("%v", err)
	}
	var ue *Error
	if errors.As(err, &ue) {
		return Audio{}, err
	}
	return Audio{}, Failed("audio: %v", err)
}

// applyAudio performs one audio verb and returns the state afterwards.
//
//	get          nothing
//	set <n>      level n
//	step <±n>    current level + n, clamped to 0..100
//	mute <m>     on, off or toggle
//
// A level change leaves the mute state alone: the SmartThings slider and
// the mute switch are separate controls, and a hub setting the level of a
// muted PC must not also unmute it.
func applyAudio(ep audioEndpoint, req Request) (Audio, error) {
	switch req.Verb {
	case "get":
	case "set":
		if err := ep.SetVolume(percentToLevel(req.Value)); err != nil {
			return Audio{}, err
		}
	case "step":
		level, err := ep.Volume()
		if err != nil {
			return Audio{}, err
		}
		if err := ep.SetVolume(percentToLevel(clampPercent(levelToPercent(level) + req.Value))); err != nil {
			return Audio{}, err
		}
	case "mute":
		on := req.Mode == "on"
		if req.Mode == "toggle" {
			muted, err := ep.Muted()
			if err != nil {
				return Audio{}, err
			}
			on = !muted
		}
		if err := ep.SetMuted(on); err != nil {
			return Audio{}, err
		}
	default:
		return Audio{}, badArgs("audio: unknown verb %q", req.Verb)
	}
	return readAudio(ep)
}

// readAudio reads the state back from the endpoint.
func readAudio(ep audioEndpoint) (Audio, error) {
	level, err := ep.Volume()
	if err != nil {
		return Audio{}, err
	}
	muted, err := ep.Muted()
	if err != nil {
		return Audio{}, err
	}
	return Audio{Volume: levelToPercent(level), Muted: muted, Device: sanitizeDevice(ep.Device())}, nil
}

// levelToPercent maps the 0.0..1.0 scalar to the 0..100 the volume flyout
// shows, rounding to the nearest step so a level set as n reads back as n.
func levelToPercent(level float32) int {
	return clampPercent(int(math.Round(float64(level) * 100)))
}

func percentToLevel(p int) float32 {
	return float32(clampPercent(p)) / 100
}

func clampPercent(p int) int {
	return min(max(p, 0), 100)
}

// sanitizeDevice keeps a device name inside Audio.Validate's rules: no
// control characters and at most MaxDeviceRunes characters. Drivers name
// devices, so neither is guaranteed.
func sanitizeDevice(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxDeviceRunes {
		s = string([]rune(s)[:MaxDeviceRunes])
	}
	return s
}
