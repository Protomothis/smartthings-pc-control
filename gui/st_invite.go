package gui

// The driver channel invite on the SmartThings tab (#155). Installing the
// Edge driver starts from the Protomothis channel invite, which used to be
// in the README only. The tab offers it as a button (default browser), a
// copy button and a QR code for the phone; while no hub is connected the
// short install steps and the QR code sit right under the "no hub" line.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// stHubMissing is the "no hub connected" case of the status line: no
// driver has polled this PC (or the service does not know its address).
func stHubMissing(h STHub) bool {
	return !h.Connected || h.IP == ""
}

// buildSTInvite returns the button row — [Install driver (channel
// invite)] [Copy link] [QR code] — and fills t.inviteHelp (steps and QR
// code) and t.qrBtn. Both start hidden or shown as for "no hub" only once
// /api/st/hub has answered: before that the status line says "checking".
func (u *ui) buildSTInvite(t *stSection) fyne.CanvasObject {
	openBtn := widget.NewButtonWithIcon(u.t("st.invite.open"), theme.DownloadIcon(), func() {
		if err := openLink(driverInviteURL); err != nil {
			dialog.ShowError(err, u.win)
		}
	})
	copyBtn := widget.NewButtonWithIcon(u.t("st.invite.copy"), theme.ContentCopyIcon(), u.copyDriverInvite)
	t.qrBtn = widget.NewButton(u.t("st.invite.qr"), u.showDriverInviteQR)
	t.qrBtn.Hide()

	steps := widget.NewLabel(u.t("st.invite.steps"))
	steps.Wrapping = fyne.TextWrapWord
	t.inviteHelp = container.NewVBox(
		steps,
		container.NewHBox(newQRCode(driverInviteURL), layout.NewSpacer()),
		hint(u.t("st.invite.qr.hint")),
	)
	t.inviteHelp.Hide()

	return container.NewHBox(openBtn, copyBtn, t.qrBtn, layout.NewSpacer())
}

// showSTInviteHelp shows the steps and QR code while no hub is connected
// and swaps them for the [QR code] button once one is. UI thread only.
func (u *ui) showSTInviteHelp(missing bool) {
	t := u.st
	if t == nil || t.inviteHelp == nil {
		return
	}
	if missing {
		t.inviteHelp.Show()
		t.qrBtn.Hide()
	} else {
		t.inviteHelp.Hide()
		t.qrBtn.Show()
	}
	// The section changes height, so the tab root lays out again (#53).
	if u.stRoot != nil {
		u.stRoot.Refresh()
	}
}

// copyDriverInvite puts the invite link on the clipboard and says so, as
// the machine id's [복사] does. UI thread only.
func (u *ui) copyDriverInvite() {
	u.app.Clipboard().SetContent(driverInviteURL)
	dialog.ShowInformation(u.t("st.invite.copied"), driverInviteURL, u.win)
}

// showDriverInviteQR is the [QR code] button: the invite as a QR code,
// for a phone, once a hub is connected and the inline one is gone.
func (u *ui) showDriverInviteQR() {
	body := container.NewVBox(
		container.NewCenter(newQRCode(driverInviteURL)),
		hint(u.t("st.invite.qr.hint")),
	)
	d := dialog.NewCustom(u.t("st.invite.qr"), u.t("st.invite.close"), body, u.win)
	d.Resize(fyne.NewSize(360, 0))
	d.Show()
}
