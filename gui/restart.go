package gui

// Restarting the service from the app: the settings tab's [서비스 재시작]
// and the offer after a save whose change only applies on restart (#139).

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// serviceNeedsRestart reports whether the service's save confirmation
// says the change applies only after a restart (the port, browser
// access). The service answers in English only.
func serviceNeedsRestart(serviceMsg string) bool {
	return strings.Contains(serviceMsg, "Restart service")
}

// restartService asks the service to restart itself and shows the status
// bar as reconnecting; the poll picks the service up again. busy marks the
// controls that started it (nil for none). UI thread only.
func (u *ui) restartService(busy func(bool)) {
	runAsyncErr(busy, u.client.RestartService, func(err error) {
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.setStatus(u.t("settings.restarting"))
		u.setConn(connPending)
	})
}

// offerRestart is the save confirmation for a change that needs a
// restart: msg with [지금 재시작] next to [나중에], so a new command port
// (and its firewall rule, which the service fixes at start) does not sit
// unapplied until the next reboot (#139). u.confirm answers in the tests.
// UI thread only.
func (u *ui) offerRestart(msg string) {
	cb := func(now bool) {
		if now {
			u.restartService(nil)
		}
	}
	if u.confirm != nil {
		u.confirm(u.t("settings.saved"), msg, u.t("settings.restart.now"), cb)
		return
	}
	body := widget.NewLabel(msg)
	body.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomConfirm(u.t("settings.saved"), u.t("settings.restart.now"), u.t("settings.restart.later"), body, cb, u.win)
	d.Resize(fyne.NewSize(440, 0))
	d.Show()
}
