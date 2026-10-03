package gui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

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
	// lockNote says why the editor is locked: the service refused the
	// local session (C5).
	lockNote *widget.Label
	bar      *saveBar
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
	remove *widget.Button
	// nameErr is the inline "another preset has this name" line.
	nameErr *widget.Label
	status  *widget.Label
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

// setEditable enables the row's controls — all off while the editor is
// locked (C5), and what a URL does not use (browse, arguments) off for a
// URL row.
func (w *presetRowWidgets) setEditable(locked bool) {
	url := w.row().Type == "url"
	for _, c := range []fyne.Disableable{w.slot, w.name, w.typ, w.path, w.test, w.remove} {
		setEnabled(c, !locked)
	}
	setEnabled(w.browse, !locked && !url)
	setEnabled(w.args, !locked && !url)
}

// showLabel writes text into l and shows it, or hides it when empty.
func showLabel(l *widget.Label, text string, imp widget.Importance) {
	l.Importance = imp
	l.SetText(text)
	if text == "" {
		l.Hide()
	} else {
		l.Show()
	}
}

// setStatus writes a row's result line (hidden when empty).
func (w *presetRowWidgets) setStatus(text string, imp widget.Importance) {
	showLabel(w.status, text, imp)
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
		u.refreshDirty()
	})
	t.lockNote = widget.NewLabel(u.t("localonly.note"))
	t.lockNote.Wrapping = fyne.TextWrapWord
	t.lockNote.Importance = widget.WarningImportance
	t.lockNote.Hide()
	ft := u.forms.register(u.presetsForm(tabPresets))
	ft.bar = newSaveBar(u, func() { u.saveTab(ft) })
	t.bar = ft.bar
	t.root = container.NewVBox(
		section(u.t("presets.section"), container.NewVBox(
			t.lockNote,
			hint(u.t("presets.hint")),
			t.empty,
			t.rowsBox,
			container.NewHBox(t.addBtn, layout.NewSpacer()),
		)),
		// Trailing padding so the last row never sits flush against the
		// footer (same as the other form tabs).
		widget.NewLabel(""),
	)
	return withSaveBar(t.root, ft.bar)
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
	onEdit := func(string) { u.refreshDirty() }

	w.slot = widget.NewSelect(slotOptions(), onEdit)
	if r.Slot >= 1 && r.Slot <= presetMaxSlots {
		w.slot.SetSelectedIndex(r.Slot - 1)
	}
	w.name = widget.NewEntry()
	w.name.SetPlaceHolder(u.t("presets.name.placeholder"))
	w.name.SetText(r.Name)
	w.name.OnChanged = func(string) {
		u.checkPresetNames()
		u.refreshDirty()
	}
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
	w.nameErr = widget.NewLabel("")
	w.nameErr.Wrapping = fyne.TextWrapWord
	w.nameErr.Hide()

	w.browse = widget.NewButtonWithIcon(u.t("presets.browse"), theme.FolderOpenIcon(), func() { u.browsePresetPath(w) })
	w.test = widget.NewButtonWithIcon(u.t("presets.test"), theme.MediaPlayIcon(), func() { u.testPresetRow(w) })
	w.remove = widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	w.remove.OnTapped = func() {
		t.rows = slices.DeleteFunc(t.rows, func(x *presetRowWidgets) bool { return x == w })
		t.rowsBox.Remove(w.box)
		u.checkPresetNames()
		u.relayoutPresets()
		u.refreshDirty()
	}
	w.typ.OnChanged = func(string) {
		w.path.SetPlaceHolder(u.pathPlaceholder(w.row().Type))
		if w.row().Type == "url" {
			// A URL takes no arguments: clear them rather than keep a
			// greyed value that would still be saved or checked.
			w.args.SetText("")
		}
		w.setEditable(u.editorsLocked())
		u.refreshDirty()
	}
	w.path.SetPlaceHolder(u.pathPlaceholder(w.row().Type))
	w.setEditable(u.editorsLocked())

	label := func(key string) fyne.CanvasObject {
		l := widget.NewLabel(u.t(key))
		l.Importance = widget.LowImportance
		return l
	}
	top := container.NewBorder(nil, nil,
		container.NewHBox(label("presets.slot"), w.slot, w.typ),
		container.NewHBox(w.test, w.remove),
		w.name)
	w.box = container.NewVBox(
		top,
		w.nameErr,
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
	setEnabled(t.addBtn, len(t.rows) < presetMaxSlots && !u.editorsLocked())
	t.rowsBox.Refresh()
	t.root.Refresh()
}

// applyPresetsLock locks the editor while the service refuses the local
// session (C5) — every control, the add button and Save, under a note
// saying why — and unlocks it otherwise. UI thread only.
func (u *ui) applyPresetsLock() {
	t := u.presets
	if t == nil {
		return
	}
	locked := u.editorsLocked()
	if locked {
		t.lockNote.Show()
	} else {
		t.lockNote.Hide()
	}
	for _, w := range t.rows {
		w.setEditable(locked)
	}
	t.bar.setLocked(locked)
	u.relayoutPresets()
}

// checkPresetNames shows the inline error under every row whose name
// another row has too (any case). UI thread only.
func (u *ui) checkPresetNames() {
	t := u.presets
	if t == nil {
		return
	}
	dup := duplicateNames(t.state().Rows)
	for i, w := range t.rows {
		text := ""
		if dup[i] {
			text = fmt.Sprintf(u.t("presets.err.namedup"), w.row().Slot)
		}
		showLabel(w.nameErr, text, widget.DangerImportance)
	}
	t.root.Refresh()
}

// showPresetWarnings puts the service's warnings (C6) under the rows they
// are about. UI thread only.
func (u *ui) showPresetWarnings(ws []PresetWarning) {
	t := u.presets
	if t == nil || len(ws) == 0 {
		return
	}
	for _, w := range t.rows {
		if text := presetSlotWarnings(u.lang, ws, w.row().Slot); text != "" {
			w.setStatus(text, widget.WarningImportance)
		}
	}
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
	busy := func(on bool) { setEnabled(w.test, !on && !u.editorsLocked()) }
	runAsync(busy, func() ([]PresetWarning, error) {
		return withLocalSession(u, func() ([]PresetWarning, error) { return u.client.TestPreset(p) })
	}, func(ws []PresetWarning, err error) {
		switch {
		case err != nil:
			w.setStatus(u.actionErrorText(err), widget.DangerImportance)
		case len(ws) > 0:
			// Every warning is about this one preset, whatever slot the
			// service put in it.
			parts := []string{u.t("presets.started")}
			for _, pw := range ws {
				parts = append(parts, presetWarningText(u.lang, pw))
			}
			w.setStatus(strings.Join(parts, "\n"), widget.WarningImportance)
		default:
			w.setStatus(u.t("presets.started"), widget.SuccessImportance)
		}
		if u.presets != nil {
			u.presets.root.Refresh()
		}
	})
}

// fillPresetsTab writes cfg into the editor (the formTab's Fill). The
// command tab's buttons follow the saved config instead (onConfig). UI
// thread only.
func (u *ui) fillPresetsTab(cfg Config) {
	t := u.presets
	if t == nil {
		return
	}
	t.rows = nil
	t.rowsBox.RemoveAll()
	for _, r := range presetsStateFromConfig(cfg).Rows {
		u.addPresetRow(r)
	}
	u.checkPresetNames()
	u.relayoutPresets()
}

// presetsForm is the tab's formTab. The rows are checked first so the
// reason names the slot in the app's language; the service checks again.
func (u *ui) presetsForm(index int) *formTab {
	return &formTab{
		index: index,
		Fill:  u.fillPresetsTab,
		Dirty: func(base Config) bool { return u.presets.state().dirty(base) },
		ApplyTo: func(cfg *Config) error {
			s := u.presets.state()
			if next, err := s.applyTo(*cfg); err == nil {
				*cfg = next
			}
			if key, slot := rowsProblem(s.Rows); key != "" {
				return fmt.Errorf(u.t(key), slot)
			}
			return nil
		},
	}
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
