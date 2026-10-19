package tgcontrol

// The /status line of the default playback device (#145). The device
// slots only report since #159; there is no command that switches them.

import (
	"html"

	"github.com/Protomothis/smartthings-pc-control/service/status"
)

func init() {
	texts["st_output"] = [2]string{"출력", "Output"}
}

// outputLine is the /status "출력: 스피커" line from the stored audio
// sample, "" while there is none.
func (c *Control) outputLine(a status.Audio) string {
	if !a.Available || a.Device == nil || *a.Device == "" {
		return ""
	}
	return c.text("st_output") + ": " + html.EscapeString(*a.Device)
}
