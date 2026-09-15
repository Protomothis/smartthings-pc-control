package gui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Unsaved-changes handling shared by the Settings and Notifications tabs:
// a fixed footer with a pulsing "unsaved changes" indicator and the Save
// button (so Save is always visible, however long the tab scrolls), a "•"
// marker on the tab title, and a Save / Discard / Keep editing prompt when
// the user switches tabs or closes the window with edits pending.

// Tab indices that own a save bar (order set in rebuild).
const (
	tabSettings = 0
	tabNotify   = 3
)

// saveBar is the footer under a form tab.
type saveBar struct {
	box   *fyne.Container
	dot   *canvas.Circle
	label *widget.Label
	save  *widget.Button
	anim  *fyne.Animation
	dirty bool
}

// newSaveBar builds the footer. onSave runs when Save is pressed.
func newSaveBar(u *ui, onSave func()) *saveBar {
	b := &saveBar{}
	b.dot = canvas.NewCircle(color.Transparent)
	b.label = widget.NewLabel(u.t("unsaved.indicator"))
	b.label.Importance = widget.WarningImportance
	b.label.Truncation = fyne.TextTruncateEllipsis
	b.save = widget.NewButtonWithIcon(u.t("settings.save"), theme.DocumentSaveIcon(), onSave)
	b.save.Importance = widget.HighImportance
	b.save.Disable()

	dot := container.NewCenter(container.NewGridWrap(fyne.NewSize(10, 10), b.dot))
	row := container.NewBorder(nil, nil, container.NewPadded(dot), b.save, b.label)
	b.box = container.NewVBox(widget.NewSeparator(), container.NewPadded(row))

	// Pulse the dot between faint and full warning colour while dirty.
	base := theme.Color(theme.ColorNameWarning)
	r, g, bl, _ := base.RGBA()
	b.anim = fyne.NewAnimation(900*time.Millisecond, func(f float32) {
		b.dot.FillColor = color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: uint8(60 + 195*f)}
		b.dot.Refresh()
	})
	b.anim.AutoReverse = true
	b.anim.RepeatCount = fyne.AnimationRepeatForever
	b.setDirty(false)
	return b
}

// setDirty enables Save and shows the pulsing indicator while on; hides
// both otherwise. Must be called on the UI thread.
func (b *saveBar) setDirty(on bool) {
	if on == b.dirty && (on || b.label.Hidden) {
		return
	}
	b.dirty = on
	if on {
		b.save.Enable()
		b.label.Show()
		b.anim.Start()
	} else {
		b.save.Disable()
		b.label.Hide()
		b.anim.Stop()
		b.dot.FillColor = color.Transparent
		b.dot.Refresh()
	}
}

// withSaveBar lays out a tab as scrollable content over a fixed footer.
func withSaveBar(content fyne.CanvasObject, bar *saveBar) fyne.CanvasObject {
	return container.NewBorder(nil, bar.box, nil, nil, container.NewVScroll(container.NewPadded(content)))
}

// markTab appends "•" to a tab title while that tab has unsaved changes.
// Must be called on the UI thread.
func (u *ui) markTab(index int, dirty bool) {
	if u.tabs == nil || index < 0 || index >= len(u.tabs.Items) || index >= len(u.tabTitles) {
		return
	}
	title := u.tabTitles[index]
	if dirty {
		title += " •"
	}
	if u.tabs.Items[index].Text != title {
		u.tabs.Items[index].Text = title
		u.tabs.Refresh()
	}
}

// tabDirty reports whether the form tab at index has unsaved edits.
func (u *ui) tabDirty(index int) bool {
	switch index {
	case tabSettings:
		return u.settingsDirty()
	case tabNotify:
		return u.notifyDirty()
	}
	return false
}

// saveTab saves the form tab at index without the "Saved" dialog; returns
// false when validation or the request failed (the tab stays dirty).
func (u *ui) saveTab(index int) bool {
	switch index {
	case tabSettings:
		return u.saveSettings(true)
	case tabNotify:
		return u.saveNotifyTab(true)
	}
	return true
}

// discardTab puts the form tab at index back to the last saved config.
func (u *ui) discardTab(index int) {
	if u.cfgBaseline == nil {
		return
	}
	switch index {
	case tabSettings:
		u.fillSettingsTab(*u.cfgBaseline)
	case tabNotify:
		u.fillNotifyTab(*u.cfgBaseline)
	}
}

// promptUnsaved shows Save / Discard / Keep editing for the tabs listed in
// dirtyTabs. onDone runs after a successful save or a discard; nothing
// happens on Keep editing. Must be called on the UI thread.
func (u *ui) promptUnsaved(bodyKey string, dirtyTabs []int, onDone func()) {
	body := widget.NewLabel(u.t(bodyKey))
	body.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomWithoutButtons(u.t("unsaved.title"), body, u.win)

	saveBtn := widget.NewButtonWithIcon(u.t("unsaved.save"), theme.DocumentSaveIcon(), func() {
		for _, i := range dirtyTabs {
			if !u.saveTab(i) {
				return // error dialog already shown; keep this prompt open
			}
		}
		d.Hide()
		onDone()
	})
	saveBtn.Importance = widget.HighImportance
	discardBtn := widget.NewButtonWithIcon(u.t("unsaved.discard"), theme.DeleteIcon(), func() {
		for _, i := range dirtyTabs {
			u.discardTab(i)
		}
		d.Hide()
		onDone()
	})
	discardBtn.Importance = widget.DangerImportance
	keepBtn := widget.NewButton(u.t("unsaved.keep"), d.Hide)

	d.SetButtons([]fyne.CanvasObject{keepBtn, layout.NewSpacer(), discardBtn, saveBtn})
	d.Resize(fyne.NewSize(420, 0))
	d.Show()
}

// onTabSelected guards tab switches: leaving a dirty form tab asks first.
// Installed as AppTabs.OnSelected in rebuild.
func (u *ui) onTabSelected(item *container.TabItem) {
	target := -1
	for i, it := range u.tabs.Items {
		if it == item {
			target = i
		}
	}
	if target < 0 || u.switching {
		u.curTab = target
		return
	}
	from := u.curTab
	if from == target || from < 0 || !u.tabDirty(from) {
		u.curTab = target
		return
	}
	// Stay on the dirty tab while asking.
	u.switching = true
	u.tabs.SelectIndex(from)
	u.switching = false
	u.promptUnsaved("unsaved.body.tab", []int{from}, func() {
		u.switching = true
		u.tabs.SelectIndex(target)
		u.switching = false
		u.curTab = target
	})
}

// onCloseRequest hides the window to the tray, asking first when a form tab
// has unsaved edits. Installed with SetCloseIntercept in Run.
func (u *ui) onCloseRequest() {
	var dirty []int
	for _, i := range []int{tabSettings, tabNotify} {
		if u.tabDirty(i) {
			dirty = append(dirty, i)
		}
	}
	if len(dirty) == 0 {
		u.win.Hide()
		return
	}
	u.promptUnsaved("unsaved.body.close", dirty, u.win.Hide)
}
