package tgcontrol

// The /status lines of the speakers and the microphone (#145, #159):
//
//	출력: 스피커 (Realtek(R) Audio) · 재생 중
//	입력: 마이크 (Realtek(R) Audio) · 음소거
//
// Read-only, from the status audio block: the device slots only report
// since #159, and no command switches devices.

import (
	"html"

	"github.com/Protomothis/smartthings-pc-control/service/status"
)

func init() {
	for k, v := range map[string][2]string{
		"snd_output":  {"출력", "Output"},
		"snd_input":   {"입력", "Input"},
		"snd_playing": {"재생 중", "playing"},
		"snd_silent":  {"소리 없음", "silent"},
		"snd_muted":   {"음소거", "muted"},
		"snd_unmuted": {"켜짐", "on"},
	} {
		if _, dup := texts[k]; dup {
			panic("tgcontrol: text " + k + " defined twice")
		}
		texts[k] = v
	}
}

// outputLine is the /status "출력: 스피커 · 재생 중" line from the stored
// audio sample, "" while there is none. The playing part needs the tray
// app, which measures it.
func (c *Control) outputLine(a status.Audio) string {
	if !a.Available || a.Device == nil || *a.Device == "" {
		return ""
	}
	line := c.text("snd_output") + ": " + html.EscapeString(*a.Device)
	if a.Playing != nil {
		state := c.text("snd_silent")
		if *a.Playing {
			state = c.text("snd_playing")
		}
		line += " · " + state
	}
	return line
}

// inputLine is the /status "입력: 마이크 · 음소거" line, "" while the
// recording device is unknown.
func (c *Control) inputLine(a status.Audio) string {
	if !a.Available || a.InputDevice == nil || a.InputMuted == nil {
		return ""
	}
	name := *a.InputDevice
	state := c.text("snd_unmuted")
	if *a.InputMuted {
		state = c.text("snd_muted")
	}
	if name == "" {
		return c.text("snd_input") + ": " + state
	}
	return c.text("snd_input") + ": " + html.EscapeString(name) + " · " + state
}
