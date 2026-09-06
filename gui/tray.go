package gui

import (
	_ "embed"
	"fmt"
	"os/exec"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/systray"
)

//go:embed icon.png
var iconBytes []byte

var appIcon = fyne.NewStaticResource("icon.png", iconBytes)

// setupTray installs the system tray icon and menu. Called from rebuild()
// so menu labels follow the current language.
func (u *ui) setupTray() {
	desk, ok := u.app.(desktop.App)
	if !ok {
		return
	}

	u.trayStatus = fyne.NewMenuItem(u.t("status.connecting"), nil)
	u.trayStatus.Disabled = true
	// Force the next refreshTrayStatus to repaint the freshly built entry.
	u.trayShown = ""

	openItem := fyne.NewMenuItem(u.t("tray.open"), func() {
		u.win.Show()
		u.win.RequestFocus()
	})
	webUIItem := fyne.NewMenuItem(u.t("settings.openwebui"), func() {
		exec.Command("cmd", "/c", "start", fmt.Sprintf("http://127.0.0.1:%d", webUIPort)).Start()
	})

	// Quick commands (safe ones only — destructive commands live in the
	// window, behind a confirmation dialog).
	trayCmd := func(name string) func() {
		return func() { go u.client.TestCommand(name) }
	}
	commandsItem := fyne.NewMenuItem(u.t("tab.commands"), nil)
	commandsItem.ChildMenu = fyne.NewMenu("",
		fyne.NewMenuItem(u.t("cmd.lock"), trayCmd("lock")),
		fyne.NewMenuItem(u.t("cmd.screenoff"), trayCmd("turnscreenoff")),
	)

	cancelScheduleItem := fyne.NewMenuItem(u.t("schedule.cancel"), func() {
		go func() {
			u.client.CancelSchedule()
			u.loadSchedule()
		}()
	})

	quitItem := fyne.NewMenuItem(u.t("tray.exit"), func() {
		u.app.Quit()
	})
	// Mark it as the quit entry — otherwise Fyne appends its own "Quit"
	// and the menu ends up with two exit items.
	quitItem.IsQuit = true

	u.trayNeedsConn = []*fyne.MenuItem{commandsItem, cancelScheduleItem, webUIItem}

	u.trayMenu = fyne.NewMenu(windowTitle,
		openItem,
		fyne.NewMenuItemSeparator(),
		u.trayStatus,
		commandsItem,
		cancelScheduleItem,
		webUIItem,
		fyne.NewMenuItemSeparator(),
		quitItem,
	)
	desk.SetSystemTrayMenu(u.trayMenu)
	desk.SetSystemTrayIcon(appIcon)
	// Left click on the tray icon opens the window; the menu stays on
	// right click only (without this Fyne shows the menu on both).
	desk.SetSystemTrayWindow(u.win)
}

// setStatus updates the window status bar and the tray status/tooltip.
// Must be called on the UI thread.
func (u *ui) setStatus(text string) {
	u.statusText = text
	u.status.SetText(text)
	u.refreshTrayStatus()
}

// setScheduleText records the active-schedule line ("" when none) shown in
// the tray status entry and tooltip. Must be called on the UI thread.
func (u *ui) setScheduleText(text string) {
	u.schedText = text
	u.refreshTrayStatus()
}

// refreshTrayStatus pushes the connection state plus the schedule countdown
// (when active) into the disabled tray menu entry and the icon tooltip. It
// is a no-op when nothing changed, so the 2s schedule poll only touches the
// native menu while a countdown is ticking.
func (u *ui) refreshTrayStatus() {
	text := u.statusText
	if u.schedText != "" {
		text += " · " + u.schedText
	}
	if text == u.trayShown {
		return
	}
	u.trayShown = text
	if u.trayStatus != nil {
		u.trayStatus.Label = text
		u.trayMenu.Refresh()
	}
	tip := windowTitle + "\n" + u.statusText
	if u.schedText != "" {
		tip += "\n" + u.schedText
	}
	setTrayTooltip(tip)
}

// trayTooltipMax caps the tooltip; the Windows NOTIFYICONDATA tip buffer
// holds 127 UTF-16 units and systray silently truncates beyond that.
const trayTooltipMax = 120

// setTrayTooltip sets the tray icon hover text. Fyne's desktop.App has no
// tooltip API, but its driver runs on fyne.io/systray whose package-level
// SetTooltip works once the tray exists (before that it just logs). Any
// panic from a driver without a tray is swallowed — the tooltip is cosmetic.
func setTrayTooltip(text string) {
	defer func() { _ = recover() }()
	if r := []rune(text); len(r) > trayTooltipMax {
		text = string(r[:trayTooltipMax-1]) + "…"
	}
	systray.SetTooltip(text)
}
