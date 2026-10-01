package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The settings tab's 미디어·알림 section (media-notify doc §4, "UI 구성"):
// the media switches of #104 and #117 next to the PC-notification switch
// of #106 and [테스트 알림]. It shares the settings tab's save bar:
// settingsState reads it into settingsFormState.NotifyPC.

// --- Pure model (unit-tested) ------------------------------------------------

// notifyPCState is the PC-notification part of the section as plain values.
type notifyPCState struct {
	Enabled bool
}

func notifyPCStateFromConfig(cfg Config) notifyPCState {
	return notifyPCState{Enabled: cfg.NotifyPC.Enabled}
}

// applyTo writes the state into cfg.
func (s notifyPCState) applyTo(cfg *Config) {
	cfg.NotifyPC = NotifyPCConfig(s)
}

// dirty reports whether saving would change base.
func (s notifyPCState) dirty(base Config) bool {
	return s.Enabled != base.NotifyPC.Enabled
}

// --- Widgets -----------------------------------------------------------------

// notifySection holds the section's PC-notification widgets; rebuilt with
// the settings tab on a language change.
type notifySection struct {
	notifyCheck *toggle
	testBtn     *widget.Button
	testStatus  *widget.Label
}

// buildMediaNotifySection creates the section. head are the settings
// tab's own media switches (media.enabled and media.now_playing with their
// hints), placed first; the PC-notification controls follow.
func (u *ui) buildMediaNotifySection(head ...fyne.CanvasObject) fyne.CanvasObject {
	n := &notifySection{}
	u.pcNotify = n
	n.notifyCheck = newToggle(u.t("notifypc.enabled"), func(bool) { u.refreshDirty() })
	n.testStatus = widget.NewLabel("")
	n.testStatus.Wrapping = fyne.TextWrapWord
	n.testBtn = widget.NewButtonWithIcon(u.t("notifypc.test"), theme.MailSendIcon(), func() { u.sendNotifyTest() })

	return container.NewVBox(append(head,
		n.notifyCheck,
		hint(u.t("notifypc.hint")),
		container.NewHBox(n.testBtn, layout.NewSpacer()),
		n.testStatus,
	)...)
}

// notifySectionState reads the widgets; the zero state before the section
// exists.
func (u *ui) notifySectionState() notifyPCState {
	n := u.pcNotify
	if n == nil {
		return notifyPCState{}
	}
	return notifyPCState{Enabled: n.notifyCheck.Checked}
}

// fillNotifySection writes cfg into the section. UI thread only; the
// caller re-evaluates the save state.
func (u *ui) fillNotifySection(cfg Config) {
	n := u.pcNotify
	if n == nil {
		return
	}
	n.notifyCheck.SetChecked(notifyPCStateFromConfig(cfg).Enabled)
	n.testStatus.SetText("")
}

// sendNotifyTest shows a test notification, whether or not PC
// notifications are allowed yet.
func (u *ui) sendNotifyTest() {
	n := u.pcNotify
	n.testStatus.Importance = widget.LowImportance
	n.testStatus.SetText(u.t("notifypc.test.sending"))
	runAsync(busyControls(n.testBtn), u.client.TestNotify, func(_ NotifyResult, err error) {
		if err != nil {
			n.testStatus.Importance = widget.DangerImportance
			n.testStatus.SetText(u.actionErrorText(err))
		} else {
			n.testStatus.Importance = widget.SuccessImportance
			n.testStatus.SetText(u.t("notifypc.test.shown"))
		}
		n.testStatus.Refresh()
	})
}

// actionErrorText is err in the user's words when the app knows its code.
func (u *ui) actionErrorText(err error) string {
	if key := actionErrorKey(err); key != "" {
		return u.t(key)
	}
	return err.Error()
}
