package gui

// The commands tab: the test buttons, the media card (media_card.go), the
// keep-awake row (awake.go) and the preset buttons (presets_tab.go).

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// cmdButtonSize keeps command buttons compact instead of stretching full-width.
var cmdButtonSize = fyne.NewSize(170, 38)

// Commands shown on the test panel. Destructive ones ask for confirmation
// before firing, since a test click acts on this very PC.
var testCommands = []struct {
	Name        string
	LabelKey    string
	Icon        func() fyne.Resource
	Destructive bool
}{
	{"ping", "cmd.ping", theme.ConfirmIcon, false},
	{"lock", "cmd.lock", theme.AccountIcon, false},
	{"turnscreenoff", "cmd.screenoff", theme.VisibilityOffIcon, false},
	{"turnscreenon", "cmd.screenon", theme.VisibilityIcon, false},
	{"suspend", "cmd.suspend", theme.MediaPauseIcon, true},
	{"hibernate", "cmd.hibernate", theme.MediaStopIcon, true},
	{"restart", "cmd.restart", theme.ViewRefreshIcon, true},
	{"shutdown", "cmd.shutdown", theme.LogoutIcon, true},
	{"forceshutdown", "cmd.forceshutdown", theme.WarningIcon, true},
}

func (u *ui) buildCommandsTab() fyne.CanvasObject {
	makeButton := func(name, label string, icon fyne.Resource, destructive bool) fyne.CanvasObject {
		var btn *widget.Button
		run := func() {
			runAsync(busyControls(btn), func() (string, error) { return u.client.TestCommand(name) },
				func(_ string, err error) {
					if err != nil {
						dialog.ShowError(err, u.win)
						return
					}
					u.setStatus(fmt.Sprintf(u.t("cmd.sent"), label))
				})
		}
		btn = widget.NewButtonWithIcon(label, icon, func() {
			if destructive {
				dialog.ShowConfirm(u.t("cmd.confirm.title"), fmt.Sprintf(u.t("cmd.confirm.body"), label), func(ok bool) {
					if ok {
						run()
					}
				}, u.win)
				return
			}
			run()
		})
		if destructive {
			btn.Importance = widget.DangerImportance
		}
		return btn
	}

	var safe, power []fyne.CanvasObject
	for _, tc := range testCommands {
		btn := makeButton(tc.Name, u.t(tc.LabelKey), tc.Icon(), tc.Destructive)
		if tc.Destructive {
			power = append(power, btn)
		} else {
			safe = append(safe, btn)
		}
	}

	note := widget.NewLabel(u.t("cmd.immediate.note"))
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance

	// Cards: general, power (with the note on what "immediately" means),
	// media (#117), keep-awake, presets (#109).
	return container.NewVScroll(container.NewPadded(container.NewVBox(
		section(u.t("cmd.group.safe"), container.NewGridWrap(cmdButtonSize, safe...)),
		widget.NewSeparator(),
		section(u.t("cmd.group.power"), container.NewVBox(container.NewGridWrap(cmdButtonSize, power...), note)),
		widget.NewSeparator(),
		u.buildMediaCard(),
		widget.NewSeparator(),
		u.buildAwakeRow(),
		widget.NewSeparator(),
		u.buildPresetButtons(),
	)))
}
