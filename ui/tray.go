package ui

// The tray icon, through fyne.io/systray directly: a left click opens the
// window, the menu has the status, the two safe quick commands, schedule
// cancel, the WebUI and quit — the Fyne tray's entries (gui/tray.go).

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"sync"

	"fyne.io/systray"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// trayTooltipMax caps the tooltip; the Windows NOTIFYICONDATA tip buffer
// holds 127 UTF-16 units and systray silently truncates beyond that.
const trayTooltipMax = 120

// tray is the menu's state. All fields are guarded by mu.
type tray struct {
	mu     sync.Mutex
	ready  bool
	lang   gui.Lang
	cancel context.CancelFunc
	status *systray.MenuItem
	// needConn are the entries that only work while the service answers.
	needConn []*systray.MenuItem
	shown    string // the status text last pushed, to skip needless calls
	conn     bool
}

// trayReady runs on systray's ready callback.
func (u *UI) trayReady() {
	if ico := pngToICO(gui.IconPNG()); ico != nil {
		systray.SetIcon(ico)
	}
	systray.SetTitle(windowTitle)
	systray.SetOnTapped(u.Show) // left click opens the window; right click the menu
	u.tray.mu.Lock()
	u.tray.ready = true
	u.tray.mu.Unlock()
	u.refreshTray()
}

// refreshTray rebuilds the menu after a language change and pushes the
// status into the status entry and the tooltip. Safe from any goroutine,
// and a no-op before the tray is up.
func (u *UI) refreshTray() {
	t := &u.tray
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
		return
	}
	lang := u.language()
	if t.status == nil || t.lang != lang {
		t.lang = lang
		u.buildTrayMenu()
		t.shown = ""
	}
	s := u.current()
	text := u.statusText(s)
	if cd := u.scheduleLine(s); cd != "" {
		text += " · " + cd
	}
	if text != t.shown {
		t.shown = text
		t.status.SetTitle(text)
		tip := windowTitle + "\n" + text
		if r := []rune(tip); len(r) > trayTooltipMax {
			tip = string(r[:trayTooltipMax-1]) + "…"
		}
		systray.SetTooltip(tip)
	}
	if on := s.conn == connOK; on != t.conn {
		t.conn = on
		for _, item := range t.needConn {
			if on {
				item.Enable()
			} else {
				item.Disable()
			}
		}
	}
}

// buildTrayMenu replaces the whole menu (systray has no insert-at API).
// u.tray.mu is held.
func (u *UI) buildTrayMenu() {
	t := &u.tray
	if t.cancel != nil {
		t.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	systray.ResetMenu()
	l := t.lang
	on := func(item *systray.MenuItem, fn func()) {
		go func() {
			for {
				select {
				case <-item.ClickedCh:
					fn()
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	run := func(name string) func() {
		return func() { u.act(func() error { _, err := u.api.TestCommand(name); return err }, "", nil) }
	}

	on(systray.AddMenuItem(gui.T(l, "tray.open"), ""), u.Show)
	systray.AddSeparator()
	t.status = systray.AddMenuItem(gui.T(l, "status.connecting"), "")
	t.status.Disable()
	cmds := systray.AddMenuItem(gui.T(l, "home.quick"), "")
	on(cmds.AddSubMenuItem(gui.T(l, "cmd.lock"), ""), run("lock"))
	on(cmds.AddSubMenuItem(gui.T(l, "cmd.screenoff"), ""), run("turnscreenoff"))
	cancelSched := systray.AddMenuItem(gui.T(l, "schedule.cancel"), "")
	on(cancelSched, func() {
		u.act(func() error { return u.api.CancelSchedule("tray") }, "", u.loadSchedule)
	})
	webUI := systray.AddMenuItem(gui.T(l, "settings.openwebui"), "")
	on(webUI, func() {
		_ = systool.Command(systool.Cmd, "/c", "start", fmt.Sprintf("http://127.0.0.1:%d", u.current().port)).Start()
	})
	systray.AddSeparator()
	on(systray.AddMenuItem(gui.T(l, "tray.exit"), ""), u.quit)
	t.needConn = []*systray.MenuItem{cmds, cancelSched, webUI}
	t.conn = true // forces the first enable/disable pass
}

// pngToICO wraps a PNG in a one-image ICO, which is what the Windows tray
// takes (Vista and later read PNG-compressed entries).
func pngToICO(data []byte) []byte {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	dim := func(n int) byte {
		if n >= 256 {
			return 0 // 0 means 256
		}
		return byte(n)
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, [3]uint16{0, 1, 1}) // reserved, type icon, one image
	b.Write([]byte{dim(cfg.Width), dim(cfg.Height), 0, 0})
	_ = binary.Write(&b, le, [2]uint16{1, 32})                  // planes, bits per pixel
	_ = binary.Write(&b, le, [2]uint32{uint32(len(data)), 22}) // size, offset (6+16)
	b.Write(data)
	return b.Bytes()
}
