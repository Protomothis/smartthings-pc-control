package gui

// The schedule tab: a delayed power command, its countdown, and the
// schedule poll that also feeds the tray and the grace toast.

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Preset delays (minutes) on the schedule tab. These are the only choices:
// free-form minute entry was dropped in v0.3.4 (#52).
//
// #89: the same sixteen the Edge driver's list offers, up to three days
// (maxScheduleMinutes in service/server.go). Sixteen radio buttons do not fit
// on a row, so the tab shows them in a dropdown.
var schedulePresets = []int{5, 10, 15, 30, 45, 60, 90, 120, 180, 240, 360, 480, 720, 1440, 2880, 4320}

// defaultSchedulePreset is preselected on the schedule tab.
const defaultSchedulePreset = 30

// Commands offered for scheduling.
var scheduleCommands = []struct {
	Name     string
	LabelKey string
}{
	{"shutdown", "cmd.shutdown"},
	{"forceshutdown", "cmd.forceshutdown"},
	{"restart", "cmd.restart"},
	{"hibernate", "cmd.hibernate"},
	{"suspend", "cmd.suspend"},
	{"lock", "cmd.lock"},
}

func (u *ui) buildScheduleTab() fyne.CanvasObject {
	// Countdown block: the remaining time in large type, the command it
	// belongs to underneath. Both are filled by loadSchedule.
	seg := &widget.TextSegment{Text: u.t("schedule.idle"), Style: widget.RichTextStyleHeading}
	seg.Style.Alignment = fyne.TextAlignCenter
	seg.Style.SizeName = sizeNameCountdown
	u.schedBig = widget.NewRichText(seg)
	u.scheduleLabel = widget.NewLabel(u.t("schedule.none"))
	u.scheduleLabel.Alignment = fyne.TextAlignCenter
	u.scheduleLabel.Wrapping = fyne.TextWrapWord

	labels := make([]string, len(scheduleCommands))
	for i, sc := range scheduleCommands {
		labels[i] = u.t(sc.LabelKey)
	}
	cmdSelect := widget.NewSelect(labels, nil)
	cmdSelect.SetSelectedIndex(0)

	// Delay is preset-only (#52): one entry per preset. #89 grew the list to
	// sixteen (5 minutes … 3 days), which is a dropdown rather than a row of
	// radio buttons.
	presetLabels := make([]string, len(schedulePresets))
	defaultLabel := ""
	for i, m := range schedulePresets {
		presetLabels[i] = u.formatMinutes(m)
		if m == defaultSchedulePreset {
			defaultLabel = presetLabels[i]
		}
	}
	delaySelect := widget.NewSelect(presetLabels, nil)
	delaySelect.SetSelected(defaultLabel)

	var startBtn *widget.Button
	startBtn = widget.NewButtonWithIcon(u.t("schedule.start"), theme.MediaPlayIcon(), func() {
		minutes := defaultSchedulePreset
		for i, l := range presetLabels {
			if l == delaySelect.Selected {
				minutes = schedulePresets[i]
			}
		}
		name := scheduleCommands[cmdSelect.SelectedIndex()].Name
		label := u.t(scheduleCommands[cmdSelect.SelectedIndex()].LabelKey)
		dialog.ShowConfirm(u.t("cmd.confirm.title"), fmt.Sprintf(u.t("cmd.confirm.body"), label), func(ok bool) {
			if !ok {
				return
			}
			runAsyncErr(busyControls(startBtn), func() error { return u.client.SetSchedule(name, minutes) },
				func(err error) {
					if err != nil {
						dialog.ShowError(err, u.win)
						return
					}
					u.refreshNow()
				})
		}, u.win)
	})
	startBtn.Importance = widget.HighImportance

	var cancelBtn *widget.Button
	cancelBtn = widget.NewButtonWithIcon(u.t("schedule.cancel"), theme.CancelIcon(), func() {
		runAsyncErr(busyControls(cancelBtn), func() error { return u.client.CancelSchedule("app") },
			func(err error) {
				if err != nil {
					dialog.ShowError(err, u.win)
				}
				// Back to "disabled unless a schedule is active" once the
				// refresh lands.
				u.refreshNow()
			})
	})
	u.schedCancelBtn = cancelBtn
	u.schedCancelBtn.Disable() // enabled by loadSchedule while a schedule is active

	form := widget.NewForm(
		widget.NewFormItem(u.t("schedule.command"), cmdSelect),
		widget.NewFormItem(u.t("schedule.delay"), delaySelect),
	)

	return container.NewVScroll(container.NewPadded(container.NewVBox(
		container.NewPadded(container.NewVBox(u.schedBig, u.scheduleLabel)),
		widget.NewSeparator(),
		form,
		container.NewHBox(layout.NewSpacer(), startBtn, u.schedCancelBtn),
	)))
}

// setCountdown replaces the large remaining-time text on the schedule tab.
// Must be called on the UI thread.
func (u *ui) setCountdown(text string) {
	if u.schedBig == nil || len(u.schedBig.Segments) == 0 {
		return
	}
	if seg, ok := u.schedBig.Segments[0].(*widget.TextSegment); ok && seg.Text != text {
		seg.Text = text
		u.schedBig.Refresh()
	}
}

func (u *ui) loadSchedule() {
	s, err := u.client.GetSchedule()
	if err != nil {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() {
		if !s.Active {
			u.lastSchedCmd = ""
			u.setCountdown(u.t("schedule.idle"))
			u.showScheduleDetail(u.t("schedule.none"), false, false)
			u.setScheduleText("")
			return
		}
		// #89: a schedule can be three days out, so the countdown carries the
		// days in front of hh:mm:ss ("2일 3:15:00") instead of running up to
		// "72:00:00".
		remain := u.formatCountdown(time.Duration(s.RemainingSec) * time.Second)
		cmdLabel := u.commandLabel(s.Command)
		// Origin decides the wording everywhere (#54): a remote grace
		// deferral is "SmartThings … grace period", a timer set here is
		// "scheduled from this app".
		countdownKey, titleKey, originKey := "schedule.countdown", "notify.schedule.title", "schedule.origin.ui"
		switch s.Origin {
		case "remote":
			countdownKey, titleKey, originKey = "schedule.countdown.remote", "notify.grace.title", "schedule.origin.remote"
		case "telegram":
			originKey = "schedule.origin.telegram"
		case "smartthings":
			originKey = "schedule.origin.smartthings"
		}
		countdown := fmt.Sprintf(u.t(countdownKey), cmdLabel, remain)
		u.setCountdown(remain)
		detail := u.t(originKey) + "\n" + fmt.Sprintf(u.t("schedule.for"), cmdLabel)
		if s.Replaced != nil {
			detail += "\n" + fmt.Sprintf(u.t("schedule.replaced"), u.commandLabel(s.Replaced.Command), u.t(u.originShortKey(s.Replaced.Origin)))
		}
		u.showScheduleDetail(detail, s.IsRemote(), true)
		// Tray entry and icon tooltip carry the one-line form.
		u.setScheduleText(countdown)

		// A schedule appeared or changed hands (a remote grace deferral
		// replacing a local timer, or vice versa) — notify with Run now /
		// Cancel buttons so it can be handled straight from the toast.
		key := s.Origin + ":" + s.Command
		if key != u.lastSchedCmd {
			u.lastSchedCmd = key
			lang := u.lang
			title := u.t(titleKey)
			go func() {
				if err := showGraceToast(lang, title, countdown); err != nil {
					// Toast failed (e.g. PowerShell unavailable) — plain
					// notification, sent from the UI goroutine like every
					// other Fyne call.
					body := fmt.Sprintf(T(lang, "notify.grace.body"), cmdLabel, remain)
					fyne.Do(func() { u.app.SendNotification(fyne.NewNotification(title, body)) })
				}
			}()
		}
	})
}

// commandLabel returns the localised name of a scheduleable command,
// falling back to the raw name.
func (u *ui) commandLabel(name string) string {
	for _, sc := range scheduleCommands {
		if sc.Name == name {
			return u.t(sc.LabelKey)
		}
	}
	return name
}

// originShortKey maps a wire origin to the i18n key of its short label.
func (u *ui) originShortKey(origin string) string {
	switch origin {
	case "remote":
		return "origin.remote.short"
	case "telegram":
		return "origin.telegram.short"
	case "smartthings":
		return "origin.smartthings.short"
	}
	return "origin.ui.short"
}

// showScheduleDetail writes the line under the countdown and enables the
// cancel button while a schedule is active. Remote deferrals get the
// warning colour so a countdown the user did not start stands out. A no-op
// before the window is built (a minimized start: the tray still gets the
// countdown). UI thread only.
func (u *ui) showScheduleDetail(detail string, remote, active bool) {
	if u.scheduleLabel == nil {
		return
	}
	u.scheduleLabel.SetText(detail)
	if remote {
		u.scheduleLabel.Importance = widget.WarningImportance
	} else {
		u.scheduleLabel.Importance = widget.MediumImportance
	}
	u.scheduleLabel.Refresh()
	setEnabled(u.schedCancelBtn, active)
}
