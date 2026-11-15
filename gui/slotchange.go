package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// confirmSlotChanges asks whether to save the changes; cb learns the
// answer. UI thread only.
func (u *ui) confirmSlotChanges(changes []slotChange, cb func(bool)) {
	u.ask(u.t("slotchange.title"), slotChangeBody(u.lang, changes), u.t("settings.save"), cb)
}

// ask is a yes/no dialog (u.confirm in the tests). UI thread only.
func (u *ui) ask(title, body, ok string, cb func(bool)) {
	if u.confirm != nil {
		u.confirm(title, body, ok, cb)
		return
	}
	l := widget.NewLabel(body)
	l.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomConfirm(title, ok, u.t("slotchange.cancel"), l, cb, u.win)
	d.Resize(fyne.NewSize(440, 0))
	d.Show()
}
