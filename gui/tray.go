package gui

import (
	_ "embed"
	"fmt"
	"os/exec"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
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

// setStatus updates both the window status bar and the tray status entry.
// Must be called on the UI thread.
func (u *ui) setStatus(text string) {
	u.status.SetText(text)
	if u.trayStatus != nil {
		u.trayStatus.Label = text
		u.trayMenu.Refresh()
	}
}
