package gui

import (
	"cmp"
	"errors"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Running-app detection (#110, #123, media-notify doc §11) on the sharing
// tab (share_tab.go), under the session-info and now-playing opt-ins: the
// on/off toggle and the watch-list editor. It is part of the tab's form
// (shareFormState) and saves with it.
//
// Each entry sits in a numbered slot, 1–5, like a preset: the slot is the
// priority (the lowest running slot is the one the PC device's watch-list
// card names) and the routine condition "감시 N" on the PC device. A row's
// slot selector offers its own slot and the free ones only.
//
// [실행 중인 프로그램에서 고르기] asks the service for the running .exe
// names (GET /api/processes, the local trusted session only — C5) and
// shows them in a dialog. Without that session the list is locked under a
// note (locallogin.go); the detection toggle stays usable.
// That list exists only inside the dialog: nothing but the picked name is
// kept, and it only goes anywhere once the user saves it as an entry.

// Limits shared with the service (internal/config/activity.go): five
// entries, which is also the highest slot.
const (
	activityMaxWatch = 5
	activityMaxLabel = 30
)

// --- Pure form model (unit-tested) -----------------------------------------

// activityDefaultLabel is "steam" for "steam.exe", the service's own
// fallback for an empty label; the form applies it too so what it saves
// is what the service stores and the section is clean after a save.
func activityDefaultLabel(process string) string {
	if len(process) > 4 && strings.EqualFold(process[len(process)-4:], ".exe") {
		process = process[:len(process)-4]
	}
	if utf8.RuneCountInString(process) > activityMaxLabel {
		process = string([]rune(process)[:activityMaxLabel])
	}
	return process
}

// sortActivity is a copy of watch in slot order (stable), the order the
// service stores and the editor shows.
func sortActivity(watch []ActivityWatch) []ActivityWatch {
	out := slices.Clone(watch)
	slices.SortStableFunc(out, func(a, b ActivityWatch) int { return cmp.Compare(a.Slot, b.Slot) })
	return out
}

// normalizeActivity trims the rows, drops rows left completely blank,
// fills an empty label the way the service does, sorts by slot, and never
// returns a nil list (null would mean "keep the stored list").
func normalizeActivity(a ActivityConfig) ActivityConfig {
	out := ActivityConfig{Enabled: a.Enabled, Watch: []ActivityWatch{}}
	for _, w := range a.Watch {
		w.Process = strings.TrimSpace(w.Process)
		w.Label = strings.TrimSpace(w.Label)
		if w.Process == "" && w.Label == "" {
			continue
		}
		if w.Label == "" {
			w.Label = activityDefaultLabel(w.Process)
		}
		out.Watch = append(out.Watch, w)
	}
	out.Watch = sortActivity(out.Watch)
	return out
}

// activityEqual compares two configs after normalisation, slots included.
func activityEqual(a, b ActivityConfig) bool {
	a, b = normalizeActivity(a), normalizeActivity(b)
	return a.Enabled == b.Enabled && slices.Equal(a.Watch, b.Watch)
}

// cloneActivity copies the list so editing never writes into the baseline.
func cloneActivity(a ActivityConfig) ActivityConfig {
	return ActivityConfig{Enabled: a.Enabled, Watch: slices.Clone(a.Watch)}
}

// activityProblem checks the (normalised) list the way the service will,
// so the user gets the reason in their language before anything is sent.
// "" means the list is fine.
func activityProblem(l Lang, a ActivityConfig) string {
	a = normalizeActivity(a)
	if len(a.Watch) > activityMaxWatch {
		return fmt.Sprintf(T(l, "activity.err.max"), activityMaxWatch)
	}
	seen := map[string]bool{}
	slots := map[int]bool{}
	for _, w := range a.Watch {
		p := w.Process
		switch {
		case w.Slot < 1 || w.Slot > activityMaxWatch:
			return fmt.Sprintf(T(l, "activity.err.slot"), p, activityMaxWatch)
		case slots[w.Slot]:
			return fmt.Sprintf(T(l, "activity.err.slotdup"), w.Slot)
		case p == "":
			return fmt.Sprintf(T(l, "activity.err.process"), w.Slot)
		case strings.ContainsAny(p, `\/:*?"<>|`) || strings.IndexFunc(p, unicode.IsControl) >= 0:
			return fmt.Sprintf(T(l, "activity.err.path"), p)
		case len(p) <= 4 || !strings.EqualFold(p[len(p)-4:], ".exe"):
			return fmt.Sprintf(T(l, "activity.err.exe"), p)
		case utf8.RuneCountInString(w.Label) > activityMaxLabel || strings.IndexFunc(w.Label, unicode.IsControl) >= 0:
			return fmt.Sprintf(T(l, "activity.err.label"), p, activityMaxLabel)
		}
		slots[w.Slot] = true
		key := strings.ToLower(p)
		if seen[key] {
			return fmt.Sprintf(T(l, "activity.err.dup"), p)
		}
		seen[key] = true
	}
	return ""
}

// activityListed reports whether process is on the list (case-insensitive).
func activityListed(watch []ActivityWatch, process string) bool {
	for _, w := range watch {
		if strings.EqualFold(strings.TrimSpace(w.Process), process) {
			return true
		}
	}
	return false
}

// activityFreeSlot is the lowest slot no row uses, or 0 when all five are
// taken.
func activityFreeSlot(watch []ActivityWatch) int {
	for s := 1; s <= activityMaxWatch; s++ {
		if !slices.ContainsFunc(watch, func(w ActivityWatch) bool { return w.Slot == s }) {
			return s
		}
	}
	return 0
}

// activitySlotChoices is what row i's slot selector offers: its own slot
// and every slot no other row uses, ascending.
func activitySlotChoices(watch []ActivityWatch, i int) []int {
	out := []int{}
	for s := 1; s <= activityMaxWatch; s++ {
		taken := false
		for j, w := range watch {
			if j != i && w.Slot == s {
				taken = true
				break
			}
		}
		if !taken {
			out = append(out, s)
		}
	}
	return out
}

// addActivityRow appends a blank row in the lowest free slot; ok is false
// when every slot is taken. The result is in slot order.
func addActivityRow(watch []ActivityWatch) ([]ActivityWatch, bool) {
	slot := activityFreeSlot(watch)
	if slot == 0 || len(watch) >= activityMaxWatch {
		return watch, false
	}
	return sortActivity(append(slices.Clone(watch), ActivityWatch{Slot: slot})), true
}

// addActivityProcess adds a row for a picked process — label from its
// name, in the lowest free slot — unless it is listed already or every
// slot is taken. ok reports whether a row was added; the result is in
// slot order.
func addActivityProcess(watch []ActivityWatch, process string) ([]ActivityWatch, bool) {
	process = strings.TrimSpace(process)
	slot := activityFreeSlot(watch)
	if process == "" || slot == 0 || len(watch) >= activityMaxWatch || activityListed(watch, process) {
		return watch, false
	}
	return sortActivity(append(slices.Clone(watch), ActivityWatch{
		Slot: slot, Process: process, Label: activityDefaultLabel(process),
	})), true
}

// setActivitySlot moves row i to slot when that slot is free (or already
// its own). It returns a new slice in slot order and whether anything
// changed.
func setActivitySlot(watch []ActivityWatch, i, slot int) ([]ActivityWatch, bool) {
	if i < 0 || i >= len(watch) || watch[i].Slot == slot || !slices.Contains(activitySlotChoices(watch, i), slot) {
		return watch, false
	}
	out := slices.Clone(watch)
	out[i].Slot = slot
	return sortActivity(out), true
}

// pickerCandidates is what the picker offers: the running names that are
// not on the list yet, narrowed to those containing query
// (case-insensitive).
func pickerCandidates(running []string, watch []ActivityWatch, query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	out := []string{}
	for _, name := range running {
		if activityListed(watch, name) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// --- Widgets -----------------------------------------------------------------

// activityBox holds the editor's widgets and the edited list.
type activityBox struct {
	toggle  *toggle
	rows    *fyne.Container
	addBtn  *widget.Button
	pickBtn *widget.Button
	// lockNote says why the list is locked: the service refused the local
	// session (C5). The detection toggle stays usable.
	lockNote *widget.Label
	// slotSize is the slot column's fixed size, so the selectors line up
	// with their column title.
	slotSize fyne.Size
	// watch is the edited list, in step with the rows on screen and kept
	// in slot order.
	watch []ActivityWatch
}

// form reads the editor into the pure model.
func (a *activityBox) form() ActivityConfig {
	if a == nil || a.toggle == nil {
		return ActivityConfig{Watch: []ActivityWatch{}}
	}
	return ActivityConfig{Enabled: a.toggle.Checked, Watch: slices.Clone(a.watch)}
}

// slotLabel is a slot's name in the selector: "감시 3", the same words as
// the SmartThings routine condition.
func (u *ui) slotLabel(slot int) string {
	return fmt.Sprintf(u.t("activity.slot"), slot)
}

// buildActivityBox builds the toggle and the watch-list editor. UI thread.
func (u *ui) buildActivityBox() fyne.CanvasObject {
	t := u.share
	a := &t.activity
	a.toggle = newToggle(u.t("activity.toggle"), func(bool) { u.refreshDirty() })
	a.rows = container.NewVBox()
	a.addBtn = widget.NewButtonWithIcon(u.t("activity.add"), theme.ContentAddIcon(), func() {
		if watch, ok := addActivityRow(a.watch); ok {
			a.watch = watch
			u.renderActivityRows()
			u.refreshDirty()
		}
	})
	a.pickBtn = widget.NewButtonWithIcon(u.t("activity.pick"), theme.SearchIcon(), func() { u.pickRunningProgram() })
	a.lockNote = widget.NewLabel(u.t("localonly.note"))
	a.lockNote.Wrapping = fyne.TextWrapWord
	a.lockNote.Importance = widget.WarningImportance
	a.lockNote.Hide()

	// Every selector is as wide as one showing the widest slot name.
	sample := widget.NewSelect([]string{u.slotLabel(activityMaxWatch)}, nil)
	sample.SetSelectedIndex(0)
	a.slotSize = sample.MinSize()
	u.renderActivityRows()

	bold := fyne.TextStyle{Bold: true}
	// The rows end in a delete button; an empty box of its size keeps the
	// column titles above their entries.
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(widget.NewButtonWithIcon("", theme.DeleteIcon(), nil).MinSize())
	header := container.NewBorder(nil, nil,
		u.activitySlotCell(widget.NewLabelWithStyle(u.t("activity.col.slot"), fyne.TextAlignLeading, bold)),
		gap,
		container.NewGridWithColumns(2,
			widget.NewLabelWithStyle(u.t("activity.col.process"), fyne.TextAlignLeading, bold),
			widget.NewLabelWithStyle(u.t("activity.col.label"), fyne.TextAlignLeading, bold),
		))

	return container.NewVBox(
		a.toggle,
		hint(u.t("activity.hint")),
		widget.NewLabelWithStyle(u.t("activity.watch"), fyne.TextAlignLeading, bold),
		a.lockNote,
		hint(u.t("activity.priority")),
		header,
		a.rows,
		container.NewHBox(a.addBtn, a.pickBtn, layout.NewSpacer()),
		hint(u.t("activity.watch.hint")),
	)
}

// activitySlotCell fixes o to the slot column's width.
func (u *ui) activitySlotCell(o fyne.CanvasObject) fyne.CanvasObject {
	return container.New(layout.NewGridWrapLayout(u.share.activity.slotSize), o)
}

// renderActivityRows redraws the editor rows from a.watch: per row a slot
// selector (its own slot and the free ones), the file name, the label and
// a delete button. Each row edits its own entry in place, so typing never
// rebuilds the list (and never steals the focus); adding, removing and
// changing a slot do. UI thread only.
func (u *ui) renderActivityRows() {
	t := u.share
	if t == nil || t.activity.rows == nil {
		return
	}
	a := &t.activity
	locked := u.editorsLocked()
	if locked {
		a.lockNote.Show()
	} else {
		a.lockNote.Hide()
	}
	a.rows.RemoveAll()
	if len(a.watch) == 0 {
		a.rows.Add(hint(u.t("activity.empty")))
	}
	for i := range a.watch {
		w := a.watch[i]
		choices := activitySlotChoices(a.watch, i)
		names := make([]string, len(choices))
		for k, s := range choices {
			names[k] = u.slotLabel(s)
		}
		slot := widget.NewSelect(names, nil)
		if k := slices.Index(choices, w.Slot); k >= 0 {
			slot.SetSelectedIndex(k)
		}
		// Set after the initial selection, which must not count as an edit.
		slot.OnChanged = func(string) {
			k := slot.SelectedIndex()
			if k < 0 || k >= len(choices) {
				return
			}
			if watch, ok := setActivitySlot(a.watch, i, choices[k]); ok {
				a.watch = watch
				u.renderActivityRows()
				u.refreshDirty()
			}
		}
		proc := widget.NewEntry()
		proc.SetPlaceHolder("steam.exe")
		proc.SetText(w.Process)
		proc.OnChanged = func(s string) {
			if i < len(a.watch) {
				a.watch[i].Process = s
				u.refreshDirty()
			}
		}
		label := widget.NewEntry()
		label.SetPlaceHolder(u.t("activity.ph.label"))
		label.SetText(w.Label)
		label.OnChanged = func(s string) {
			if i < len(a.watch) {
				a.watch[i].Label = s
				u.refreshDirty()
			}
		}
		del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
			if i < len(a.watch) {
				a.watch = slices.Delete(slices.Clone(a.watch), i, i+1)
				u.renderActivityRows()
				u.refreshDirty()
			}
		})
		if locked {
			slot.Disable()
			proc.Disable()
			label.Disable()
			del.Disable()
		}
		a.rows.Add(container.NewBorder(nil, nil, u.activitySlotCell(slot), del, container.NewGridWithColumns(2, proc, label)))
	}
	a.rows.Refresh()
	setEnabled(a.addBtn, len(a.watch) < activityMaxWatch && !locked)
	setEnabled(a.pickBtn, len(a.watch) < activityMaxWatch && !locked)
	// The rows changed height after the tab was laid out (see renderHubs).
	if u.share != nil && u.share.root != nil {
		u.share.root.Refresh()
	}
}

// fillActivityBox writes a into the editor. Called by fillShareTab, which
// runs with the change callbacks muted. UI thread only.
func (u *ui) fillActivityBox(a ActivityConfig) {
	t := u.share
	if t == nil || t.activity.toggle == nil {
		return
	}
	t.activity.toggle.SetChecked(a.Enabled)
	t.activity.watch = sortActivity(a.Watch)
	u.renderActivityRows()
}

// pickRunningProgram fetches the running program names off the UI thread
// and opens the picker with them. The names stay in this dialog.
func (u *ui) pickRunningProgram() {
	a := &u.share.activity
	busy := func(on bool) {
		// Back on afterwards only while a slot is free and the list is
		// not locked, the rule renderActivityRows applies.
		setEnabled(a.pickBtn, !on && len(a.watch) < activityMaxWatch && !u.editorsLocked())
	}
	list := func() ([]string, error) { return withLocalSession(u, u.client.RunningProcesses) }
	runAsync(busy, list, func(names []string, err error) {
		if errors.Is(err, errLocalOnly) {
			dialog.ShowError(errors.New(u.t("localonly.note")), u.win)
			return
		}
		if err != nil {
			u.markDisconnectedOnNetError(err)
			dialog.ShowError(fmt.Errorf(u.t("activity.pick.fail"), err), u.win)
			return
		}
		u.showProcessPicker(names)
	})
}

// showProcessPicker is the dialog: a search box over the names not yet on
// the list; picking one adds a row in the lowest free slot and closes it.
// UI thread only.
func (u *ui) showProcessPicker(running []string) {
	t := u.share
	if t == nil {
		return
	}
	a := &t.activity
	shown := pickerCandidates(running, a.watch, "")
	var dlg dialog.Dialog

	empty := hint(u.t("activity.pick.none"))
	list := widget.NewList(
		func() int { return len(shown) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < len(shown) {
				o.(*widget.Label).SetText(shown[id])
			}
		},
	)
	refresh := func() {
		if len(shown) == 0 {
			empty.Show()
		} else {
			empty.Hide()
		}
		list.UnselectAll()
		list.Refresh()
	}
	search := widget.NewEntry()
	search.SetPlaceHolder(u.t("activity.pick.search"))
	search.OnChanged = func(q string) {
		shown = pickerCandidates(running, a.watch, q)
		refresh()
	}
	list.OnSelected = func(id widget.ListItemID) {
		if id >= len(shown) {
			return
		}
		if watch, ok := addActivityProcess(a.watch, shown[id]); ok {
			a.watch = watch
			u.renderActivityRows()
			u.refreshDirty()
		}
		if dlg != nil {
			dlg.Hide()
		}
	}
	refresh()

	body := container.NewBorder(
		container.NewVBox(search, hint(u.t("activity.pick.hint")), empty),
		nil, nil, nil, list)
	dlg = dialog.NewCustom(u.t("activity.pick.title"), u.t("activity.pick.close"), body, u.win)
	dlg.Resize(fyne.NewSize(420, 480))
	dlg.Show()
	u.win.Canvas().Focus(search)
}
