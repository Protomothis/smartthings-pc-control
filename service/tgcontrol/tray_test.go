package tgcontrol

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// #164: with the tray app off the user-session commands fail with
// tray_unavailable, and the replies say what to do about it rather than
// "failed".
func TestTrayUnavailableReplies(t *testing.T) {
	c := newTestControl(t, config.Config{})
	err := fmt.Errorf("get session: %w", fmt.Errorf("%w: no tray app polls in session 1", session.ErrTrayUnavailable))
	want := c.text("tray_unavailable")

	if got, _, ferr := c.mediaError(err); got != want || ferr != nil {
		t.Errorf("media: %q, %v; want the tray text and no fault", got, ferr)
	}
	if got := c.actionError(err); got != want {
		t.Errorf("/say, /run: %q, want the tray text", got)
	}
}

// The screen replies and /status add a note while someone is logged in but
// no tray app runs; lock (no tray involved) never does.
func TestTrayOffNote(t *testing.T) {
	c := newTestControl(t, config.Config{})
	note := c.text("tray_off_note")
	var asked []session.Target
	missing := true
	c.d.TrayMissing = func(target session.Target) bool {
		asked = append(asked, target)
		return missing
	}

	reply := func(cmd string) string {
		h, _, err := c.handleCommand(context.Background(), "42", cmd, nil)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if h := reply("screenoff"); !strings.Contains(h, note) {
		t.Errorf("screenoff without a tray: %q lacks the note", h)
	}
	if len(asked) != 1 || asked[0] != session.Console {
		t.Errorf("the screen note asked about %v, want the console", asked)
	}
	if h := reply("lock"); strings.Contains(h, note) {
		t.Errorf("lock carries the tray note: %q", h)
	}
	if h := reply("status"); !strings.Contains(h, note) {
		t.Errorf("/status without a tray: %q lacks the note", h)
	}

	missing = false
	for _, cmd := range []string{"screenon", "status"} {
		if h := reply(cmd); strings.Contains(h, note) {
			t.Errorf("%s with the tray running: %q has the note", cmd, h)
		}
	}
}
