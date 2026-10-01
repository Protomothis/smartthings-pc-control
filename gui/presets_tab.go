package gui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The presets tab (#109): the editor for the actions SmartThings and
// Telegram may run by slot number. It is a tab of its own rather than a
// section of the settings tab: up to ten three-line rows would bury the
// service settings. The command tab gets the [실행] buttons for the saved
// presets (buildPresetButtons).

// presetsTab holds the tab's widgets; rebuilt with the window on a
// language change, filled by fillPresetsTab.
type presetsTab struct {
	root    *fyne.Container
	rowsBox *fyne.Container
	rows    []*presetRowWidgets
	addBtn  *widget.Button
	empty   *widget.Label
	bar     *saveBar
	// filling suppresses OnChanged while the tab is written.
	filling bool
}

// presetRowWidgets is one editor row.
type presetRowWidgets struct {
	box    *fyne.Container
	slot   *widget.Select
	name   *widget.Entry
	typ    *widget.Select
	path   *widget.Entry
	args   *widget.Entry
	browse *widget.Button
	test   *widget.Button
	status *widget.Label
}

// slotOptions are "1" … "10".
func slotOptions() []string {
	out := make([]string, presetMaxSlots)
	for i := range out {
		out[i] = strconv.Itoa(i + 1)
	}
	return out
}

func (u *ui) presetTypeLabels() []string {
	out := make([]string, len(presetTypes))
	for i, t := range presetTypes {
		out[i] = u.t("presets.type." + t)
	}
	return out
}

// row reads the widgets of one editor row.
func (w *presetRowWidgets) row() presetRow {
	r := presetRow{Name: w.name.Text, Path: w.path.Text, Args: w.args.Text}
	r.Slot = w.slot.SelectedIndex() + 1
	if i := w.typ.SelectedIndex(); i >= 0 && i < len(presetTypes) {
		r.Type = presetTypes[i]
	}
	return r
}

// setTypeEnabled greys what a URL does not use.
func (w *presetRowWidgets) setTypeEnabled() {
	if w.row().Type == "url" {
		w.browse.Disable()
		w.args.Disable()
	} else {
		w.browse.Enable()
		w.args.Enable()
	}
}

// setStatus writes a row's result line (hidden when empty).
func (w *presetRowWidgets) setStatus(text string, imp widget.Importance) {
	w.status.Importance = imp
	w.status.SetText(text)
	if text == "" {
		w.status.Hide()
	} else {
		w.status.Show()
	}
}

// state reads the tab into the pure form model.
func (t *presetsTab) state() presetsFormState {
	var s presetsFormState
	for _, w := range t.rows {
		s.Rows = append(s.Rows, w.row())
	}
	return s
}

func (u *ui) buildPresetsTab() fyne.CanvasObject {
	t := &presetsTab{}
	u.presets = t
	t.rowsBox = container.NewVBox()
	t.empty = hint(u.t("presets.empty"))
	t.addBtn = widget.NewButtonWithIcon(u.t("presets.add"), theme.ContentAddIcon(), func() {
		slot := freeSlot(t.state().Rows)
		if slot == 0 {
			return
		}
		u.addPresetRow(presetRow{Slot: slot, Type: "program"})
		u.relayoutPresets()
		u.updatePresetsSaveState()
	})
	t.bar = newSaveBar(u, func() { u.savePresetsTab(false) })
	t.root = container.NewVBox(
		section(u.t("presets.section"), container.NewVBox(
			hint(u.t("presets.hint")),
			t.empty,
			t.rowsBox,
			container.NewHBox(t.addBtn, layout.NewSpacer()),
		)),
		// Trailing padding so the last row never sits flush against the
		// footer (same as the other form tabs).
		widget.NewLabel(""),
	)
	return withSaveBar(t.root, t.bar)
}

// pathPlaceholder is the example path for a preset type.
func (u *ui) pathPlaceholder(typ string) string {
	if typ == "" {
		return ""
	}
	return u.t("presets.path.placeholder." + typ)
}

// addPresetRow appends an editor row for r. UI thread only.
func (u *ui) addPresetRow(r presetRow) {
	t := u.presets
	w := &presetRowWidgets{}
	onEdit := func(string) { u.updatePresetsSaveState() }

	w.slot = widget.NewSelect(slotOptions(), onEdit)
	if r.Slot >= 1 && r.Slot <= presetMaxSlots {
		w.slot.SetSelectedIndex(r.Slot - 1)
	}
	w.name = widget.NewEntry()
	w.name.SetPlaceHolder(u.t("presets.name.placeholder"))
	w.name.SetText(r.Name)
	w.name.OnChanged = onEdit
	w.typ = widget.NewSelect(u.presetTypeLabels(), nil)
	if i := slices.Index(presetTypes, r.Type); i >= 0 {
		w.typ.SetSelectedIndex(i)
	}
	w.path = widget.NewEntry()
	w.path.SetText(r.Path)
	w.path.OnChanged = onEdit
	w.args = widget.NewEntry()
	w.args.SetPlaceHolder(u.t("presets.args.placeholder"))
	w.args.SetText(r.Args)
	w.args.OnChanged = onEdit
	w.status = widget.NewLabel("")
	w.status.Wrapping = fyne.TextWrapWord
	w.status.Hide()

	w.browse = widget.NewButtonWithIcon(u.t("presets.browse"), theme.FolderOpenIcon(), func() { u.browsePresetPath(w) })
	w.test = widget.NewButtonWithIcon(u.t("presets.test"), theme.MediaPlayIcon(), func() { u.testPresetRow(w) })
	remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	remove.OnTapped = func() {
		t.rows = slices.DeleteFunc(t.rows, func(x *presetRowWidgets) bool { return x == w })
		t.rowsBox.Remove(w.box)
		u.relayoutPresets()
		u.updatePresetsSaveState()
	}
	w.typ.OnChanged = func(string) {
		w.path.SetPlaceHolder(u.pathPlaceholder(w.row().Type))
		w.setTypeEnabled()
		u.updatePresetsSaveState()
	}
	w.path.SetPlaceHolder(u.pathPlaceholder(w.row().Type))
	w.setTypeEnabled()

	label := func(key string) fyne.CanvasObject {
		l := widget.NewLabel(u.t(key))
		l.Importance = widget.LowImportance
		return l
	}
	top := container.NewBorder(nil, nil,
		container.NewHBox(label("presets.slot"), w.slot, w.typ),
		container.NewHBox(w.test, remove),
		w.name)
	w.box = container.NewVBox(
		top,
		container.NewBorder(nil, nil, label("presets.path"), w.browse, w.path),
		container.NewBorder(nil, nil, label("presets.args"), nil, w.args),
		w.status,
		widget.NewSeparator(),
	)
	t.rows = append(t.rows, w)
	t.rowsBox.Add(w.box)
}

// relayoutPresets shows the "no presets" line, caps the add button at ten
// rows and re-lays out the tab (rows change height).
func (u *ui) relayoutPresets() {
	t := u.presets
	if len(t.rows) == 0 {
		t.empty.Show()
	} else {
		t.empty.Hide()
	}
	if len(t.rows) >= presetMaxSlots {
		t.addBtn.Disable()
	} else {
		t.addBtn.Enable()
	}
	t.rowsBox.Refresh()
	t.root.Refresh()
}

// browsePresetPath picks the exe or script with a file dialog.
func (u *ui) browsePresetPath(w *presetRowWidgets) {
	d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil || rc == nil {
			return
		}
		defer rc.Close()
		w.path.SetText(filepath.FromSlash(rc.URI().Path()))
	}, u.win)
	exts := []string{".exe"}
	if w.row().Type == "script" {
		exts = []string{".ps1", ".bat", ".cmd"}
	}
	d.SetFilter(storage.NewExtensionFileFilter(exts))
	d.Resize(fyne.NewSize(640, 460))
	d.Show()
}

// testPresetRow runs the row as typed via /api/presets/test.
func (u *ui) testPresetRow(w *presetRowWidgets) {
	r := w.row()
	if r.Name == "" {
		r.Name = "test"
	}
	if key := rowProblem(r); key != "" {
		w.setStatus(fmt.Sprintf(u.t(key), r.Slot), widget.DangerImportance)
		u.presets.root.Refresh()
		return
	}
	p, _ := r.preset()
	w.setStatus(u.t("presets.testing"), widget.LowImportance)
	runAsyncErr(busyControls(w.test), func() error { return u.client.TestPreset(p) }, func(err error) {
		if err != nil {
			w.setStatus(u.actionErrorText(err), widget.DangerImportance)
		} else {
			w.setStatus(u.t("presets.started"), widget.SuccessImportance)
		}
		if u.presets != nil {
			u.presets.root.Refresh()
		}
	})
}

// fillPresetsTab writes cfg into the editor and the command tab's preset
// buttons. UI thread only, after cfgBaseline is set.
func (u *ui) fillPresetsTab(cfg Config) {
	u.fillPresetButtons(cfg.Presets)
	t := u.presets
	if t == nil {
		return
	}
	t.filling = true
	t.rows = nil
	t.rowsBox.RemoveAll()
	for _, r := range presetsStateFromConfig(cfg).Rows {
		u.addPresetRow(r)
	}
	u.relayoutPresets()
	t.filling = false
	u.updatePresetsSaveState()
}

// presetsDirty reports whether the editor differs from cfgBaseline.
func (u *ui) presetsDirty() bool {
	t := u.presets
	if t == nil || t.bar == nil || t.filling || u.cfgBaseline == nil {
		return false
	}
	return t.state().dirty(*u.cfgBaseline)
}

// updatePresetsSaveState enables Save, the indicator and the tab marker
// while the editor differs from cfgBaseline. UI thread only.
func (u *ui) updatePresetsSaveState() {
	t := u.presets
	if t == nil || t.bar == nil || t.filling {
		return
	}
	dirty := u.presetsDirty()
	t.bar.setDirty(dirty)
	u.markTab(tabPresets, dirty)
}

// savePresetsTab validates the rows, posts the baseline with the presets
// over it and re-reads the config (the service sorts them). quiet skips
// the "Saved" dialog. UI thread only.
func (u *ui) savePresetsTab(quiet bool) bool {
	t := u.presets
	if t == nil || u.cfgBaseline == nil {
		return false
	}
	s := t.state()
	if key, slot := rowsProblem(s.Rows); key != "" {
		dialog.ShowError(fmt.Errorf(u.t(key), slot), u.win)
		return false
	}
	cfg, err := s.applyTo(*u.cfgBaseline)
	if err != nil {
		dialog.ShowError(err, u.win)
		return false
	}
	msg, err := u.client.SaveConfig(cfg)
	if err != nil {
		dialog.ShowError(err, u.win)
		return false
	}
	fresh, err := u.client.GetConfig()
	if err != nil {
		fresh = cfg
	}
	fresh = withGraceFallback(fresh)
	u.cfgBaseline = &fresh
	u.fillPresetsTab(fresh)
	// The other form tabs compare against the same baseline.
	u.updateSaveState()
	u.updateNotifySaveState()
	u.updateSTSaveState()
	if !quiet {
		dialog.ShowInformation(u.t("settings.saved"), msg, u.win)
	}
	return true
}

// --- command tab ---------------------------------------------------------

// buildPresetButtons is the command tab's preset section; its buttons are
// filled from the config (fillPresetButtons).
func (u *ui) buildPresetButtons() fyne.CanvasObject {
	u.presetButtons = container.NewGridWrap(cmdButtonSize)
	u.presetEmpty = hint(u.t("cmd.presets.empty"))
	return section(u.t("cmd.group.presets"), container.NewVBox(u.presetEmpty, u.presetButtons))
}

// fillPresetButtons shows one [실행] button per saved preset. UI thread.
func (u *ui) fillPresetButtons(ps []Preset) {
	if u.presetButtons == nil {
		return
	}
	u.presetButtons.RemoveAll()
	for _, p := range sortedPresets(ps) {
		label := presetButtonLabel(p)
		btn := widget.NewButtonWithIcon(label, theme.MediaPlayIcon(), nil)
		btn.OnTapped = func() {
			runAsyncErr(busyControls(btn), func() error { return u.client.RunPreset(p.Slot) }, func(err error) {
				if err != nil {
					dialog.ShowError(errors.New(u.actionErrorText(err)), u.win)
					return
				}
				u.setStatus(fmt.Sprintf(u.t("cmd.presets.started"), label))
			})
		}
		u.presetButtons.Add(btn)
	}
	if len(ps) == 0 {
		u.presetEmpty.Show()
	} else {
		u.presetEmpty.Hide()
	}
	u.presetButtons.Refresh()
}
