package gui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Unsaved-changes handling shared by the form tabs (forms.go): a fixed
// footer with a pulsing "unsaved changes" indicator and the Save button
// (so Save is always visible, however long the tab scrolls), a "•" marker
// on the tab title, and a Save / Discard / Keep editing prompt when the
// user switches tabs or closes the window with edits pending.

// Tab indices, in the order rebuild adds them (#128): the everyday tabs
// first, then what the PC shares and how it connects, settings and logs
// last. Presets, sharing, SmartThings, Telegram and settings own a save
// bar.
const (
	tabCommands = iota
	tabSchedule
	tabPresets
	tabShare
	tabSmartThings
	tabTelegram
	// tabSettings is the only tab usable while the service is unreachable
	// (installing and starting it live there).
	tabSettings
	// tabLogs is only polled while it is in front.
	tabLogs
)

// tabKeys are the tab titles' i18n keys, by index.
var tabKeys = []string{
	tabCommands:    "tab.commands",
	tabSchedule:    "tab.schedule",
	tabPresets:     "tab.presets",
	tabShare:       "tab.share",
	tabSmartThings: "tab.smartthings",
	tabTelegram:    "tab.telegram",
	tabSettings:    "tab.settings",
	tabLogs:        "tab.logs",
}

// selectTab switches to index without the unsaved-changes prompt (a
// switch the app makes, not the user). UI thread only.
func (u *ui) selectTab(index int) {
	u.switching = true
	u.tabs.SelectIndex(index)
	u.switching = false
	u.curTab = index
	u.shownTab.Store(int32(index))
}

// saveBar is the footer under a form tab.
type saveBar struct {
	box   *fyne.Container
	dot   *canvas.Circle
	label *widget.Label
	save  *widget.Button
	anim  *fyne.Animation
	// dirty is whether the tab has something to save, shown whether the
	// indicator is up, busy whether a save is in flight (Save is off then).
	// locked keeps Save off while the service would refuse the tab (the
	// presets without the local session, C5).
	dirty, shown, busy, locked bool
}

// canSave is whether Save is usable.
func (b *saveBar) canSave() bool { return b.dirty && !b.busy && !b.locked }

// setLocked turns Save off for good (until unlocked). UI thread only.
func (b *saveBar) setLocked(on bool) {
	b.locked = on
	setEnabled(b.save, b.canSave())
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
	b.shown = true // so the first setDirty hides the label
	b.setDirty(false)
	return b
}

// setDirty enables Save (unless a save is in flight) and shows the pulsing
// indicator while on; hides both otherwise. UI thread only.
func (b *saveBar) setDirty(on bool) {
	b.dirty = on
	setEnabled(b.save, b.canSave())
	if on == b.shown {
		return
	}
	b.shown = on
	if on {
		b.label.Show()
		b.anim.Start()
	} else {
		b.label.Hide()
		b.anim.Stop()
		b.dot.FillColor = color.Transparent
		b.dot.Refresh()
	}
}

// setBusy turns Save off while a save is in flight and back to the dirty
// state afterwards. UI thread only.
func (b *saveBar) setBusy(on bool) {
	b.busy = on
	setEnabled(b.save, b.canSave())
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

// promptUnsaved shows Save / Discard / Keep editing for the dirty tabs.
// Save posts them together through the one save path; onDone runs after a
// successful save or a discard; nothing happens on Keep editing. UI thread
// only.
func (u *ui) promptUnsaved(bodyKey string, dirty []*formTab, onDone func()) {
	body := widget.NewLabel(u.t(bodyKey))
	body.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomWithoutButtons(u.t("unsaved.title"), body, u.win)

	var saveBtn, discardBtn, keepBtn *widget.Button
	saveBtn = widget.NewButtonWithIcon(u.t("unsaved.save"), theme.DocumentSaveIcon(), func() {
		busy := busyControls(saveBtn, discardBtn, keepBtn)
		busy(true)
		u.saveForms(dirty, true, func(ok bool) {
			busy(false)
			if !ok {
				return // error dialog already shown; keep this prompt open
			}
			d.Hide()
			onDone()
		})
	})
	saveBtn.Importance = widget.HighImportance
	discardBtn = widget.NewButtonWithIcon(u.t("unsaved.discard"), theme.CancelIcon(), func() {
		if base := u.forms.base; base != nil {
			for _, t := range dirty {
				u.forms.fill(t, *base)
			}
			u.refreshDirty()
		}
		d.Hide()
		onDone()
	})
	keepBtn = widget.NewButton(u.t("unsaved.keep"), d.Hide)

	// One centred row, like Fyne's own confirm dialogs: the safe choice on
	// the left, the primary action on the right.
	d.SetButtons([]fyne.CanvasObject{keepBtn, discardBtn, saveBtn})
	d.Resize(fyne.NewSize(440, 0))
	d.Show()
}

// setCurTab records the tab now on screen and refreshes the ones that only
// load when shown. The SmartThings tab has no polling loop of its own (#70), so
// its WoL list and SmartThings hub state are re-read here; the Telegram tab's
// Telegram conflict warning (#75) is re-read the same way, and the logs,
// polled only while their tab is in front, catch up at once.
func (u *ui) setCurTab(index int) {
	u.curTab = index
	u.shownTab.Store(int32(index))
	if !u.connected.Load() {
		return
	}
	switch index {
	case tabSmartThings:
		u.refreshNetwork()
	case tabTelegram:
		u.refreshTelegramState()
	case tabLogs:
		background(u.loadLogs)
	}
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
		u.setCurTab(target)
		return
	}
	from := u.curTab
	leaving := u.forms.tabAt(from)
	if from == target || leaving == nil || !u.forms.isDirty(leaving) {
		u.setCurTab(target)
		return
	}
	// Stay on the dirty tab while asking.
	u.switching = true
	u.tabs.SelectIndex(from)
	u.switching = false
	u.promptUnsaved("unsaved.body.tab", []*formTab{leaving}, func() {
		u.switching = true
		u.tabs.SelectIndex(target)
		u.switching = false
		u.setCurTab(target)
	})
}

// onCloseRequest hides the window to the tray, asking first when a form tab
// has unsaved edits. Installed with SetCloseIntercept in rebuild.
func (u *ui) onCloseRequest() {
	dirty := u.forms.dirtyTabs()
	if len(dirty) == 0 {
		u.win.Hide()
		return
	}
	u.promptUnsaved("unsaved.body.close", dirty, u.win.Hide)
}
