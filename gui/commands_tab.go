package gui

// The commands tab: the test buttons, the keep-awake row (awake.go) and,
// while their feature is on, the media card (media_card.go), the monitor
// card (monitor_card.go) and the preset buttons (presets_tab.go).

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

	// Always: general, power (with the note on what "immediately" means)
	// and keep-awake. Then only what is turned on (#157): media (#117),
	// monitor (#144), presets (#109) — see applyCommandCards.
	c := &u.cmdCards
	c.media = container.NewVBox(widget.NewSeparator(), u.buildMediaCard())
	c.monitor = container.NewVBox(widget.NewSeparator(), u.buildMonitorCard())
	c.presets = container.NewVBox(widget.NewSeparator(), u.buildPresetButtons())
	more := widget.NewHyperlink(u.t("cmd.more"), nil)
	more.OnTapped = func() { u.tabs.SelectIndex(tabFeatures) }
	c.more = container.NewVBox(widget.NewSeparator(), more)
	u.applyCommandCards(nil)
	return container.NewVScroll(container.NewPadded(container.NewVBox(
		section(u.t("cmd.group.safe"), container.NewGridWrap(cmdButtonSize, safe...)),
		widget.NewSeparator(),
		section(u.t("cmd.group.power"), container.NewVBox(container.NewGridWrap(cmdButtonSize, power...), note)),
		widget.NewSeparator(),
		u.buildAwakeRow(),
		c.media,
		c.monitor,
		c.presets,
		c.more,
	)))
}

// cmdCards are the command tab's optional cards, each with the separator
// above it, and the line that points to the features tab.
type cmdCards struct {
	media, monitor, presets, more *fyne.Container
}

// commandCardsShown is which optional cards the command tab shows for the
// saved config (nil before the first load: none): media and monitor while
// their feature is on, the presets while there are any, and the line to
// the features tab while media or monitor is off.
func commandCardsShown(cfg *Config) (media, monitor, presets, more bool) {
	if cfg == nil {
		return false, false, false, false
	}
	media, monitor = cfg.Media.Enabled, cfg.Monitor.Enabled
	return media, monitor, len(cfg.Presets) > 0, !media || !monitor
}

// applyCommandCards shows the cards commandCardsShown picks. It follows the
// saved config, not unsaved edits on the features tab. UI thread only.
func (u *ui) applyCommandCards(cfg *Config) {
	c := &u.cmdCards
	if c.media == nil {
		return
	}
	media, monitor, presets, more := commandCardsShown(cfg)
	for _, s := range []struct {
		box *fyne.Container
		on  bool
	}{{c.media, media}, {c.monitor, monitor}, {c.presets, presets}, {c.more, more}} {
		if s.on {
			s.box.Show()
		} else {
			s.box.Hide()
		}
	}
}
