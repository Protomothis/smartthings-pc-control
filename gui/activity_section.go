package gui

import (
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

// Running-app detection (#110, media-notify doc §11) in the network tab's
// SmartThings section, next to the session-info opt-in: the on/off toggle
// and the watch-list editor. It is part of the section's form (stFormState)
// and saves with it.
//
// [실행 중인 프로그램에서 고르기] asks the service for the running .exe
// names (GET /api/processes, loopback only) and shows them in a dialog.
// That list exists only inside the dialog: nothing but the picked name is
// kept, and it only goes anywhere once the user saves it as an entry.

// Limits shared with the service (service/activity.go).
const (
	activityMaxWatch = 20
	activityMaxLabel = 30
)

// activityKinds are the kinds in priority order, as the service ranks them.
var activityKinds = []string{"game", "stream", "media", "work", "other"}

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

// normalizeActivity trims the rows, drops rows left completely blank,
// fills an empty kind/label the way the service does, and never returns a
// nil list (null would mean "keep the stored list").
func normalizeActivity(a ActivityConfig) ActivityConfig {
	out := ActivityConfig{Enabled: a.Enabled, Watch: []ActivityWatch{}}
	for _, w := range a.Watch {
		w.Process = strings.TrimSpace(w.Process)
		w.Label = strings.TrimSpace(w.Label)
		w.Kind = strings.ToLower(strings.TrimSpace(w.Kind))
		if w.Process == "" && w.Label == "" {
			continue
		}
		if w.Kind == "" {
			w.Kind = "other"
		}
		if w.Label == "" {
			w.Label = activityDefaultLabel(w.Process)
		}
		out.Watch = append(out.Watch, w)
	}
	return out
}

// activityEqual compares two configs after normalisation.
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
	for i, w := range a.Watch {
		p := w.Process
		switch {
		case p == "":
			return fmt.Sprintf(T(l, "activity.err.process"), i+1)
		case strings.ContainsAny(p, `\/:*?"<>|`) || strings.IndexFunc(p, unicode.IsControl) >= 0:
			return fmt.Sprintf(T(l, "activity.err.path"), p)
		case len(p) <= 4 || !strings.EqualFold(p[len(p)-4:], ".exe"):
			return fmt.Sprintf(T(l, "activity.err.exe"), p)
		case utf8.RuneCountInString(w.Label) > activityMaxLabel || strings.IndexFunc(w.Label, unicode.IsControl) >= 0:
			return fmt.Sprintf(T(l, "activity.err.label"), p, activityMaxLabel)
		case !slices.Contains(activityKinds, w.Kind):
			return fmt.Sprintf(T(l, "activity.err.kind"), p)
		}
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

// addActivityProcess appends a row for a picked process — label from its
// name, kind "other" for the user to change — unless it is listed already
// or the list is full. ok reports whether a row was added.
func addActivityProcess(watch []ActivityWatch, process string) ([]ActivityWatch, bool) {
	process = strings.TrimSpace(process)
	if process == "" || len(watch) >= activityMaxWatch || activityListed(watch, process) {
		return watch, false
	}
	return append(slices.Clone(watch), ActivityWatch{
		Process: process, Label: activityDefaultLabel(process), Kind: "other",
	}), true
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

// activityKindLabels are the kind dropdown's options, in activityKinds order.
func activityKindLabels(l Lang) []string {
	out := make([]string, len(activityKinds))
	for i, k := range activityKinds {
		out[i] = T(l, "activity.kind."+k)
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
	// watch is the edited list, in step with the rows on screen.
	watch []ActivityWatch
}

// form reads the editor into the pure model.
func (a *activityBox) form() ActivityConfig {
	if a == nil || a.toggle == nil {
		return ActivityConfig{Watch: []ActivityWatch{}}
	}
	return ActivityConfig{Enabled: a.toggle.Checked, Watch: slices.Clone(a.watch)}
}

// buildActivityBox builds the toggle and the watch-list editor. UI thread.
func (u *ui) buildActivityBox() fyne.CanvasObject {
	t := u.st
	a := &t.activity
	a.toggle = newToggle(u.t("activity.toggle"), func(bool) { u.updateSTSaveState() })
	a.rows = container.NewVBox()
	a.addBtn = widget.NewButtonWithIcon(u.t("activity.add"), theme.ContentAddIcon(), func() {
		if len(a.watch) >= activityMaxWatch {
			return
		}
		a.watch = append(a.watch, ActivityWatch{Kind: "other"})
		u.renderActivityRows()
		u.updateSTSaveState()
	})
	a.pickBtn = widget.NewButtonWithIcon(u.t("activity.pick"), theme.SearchIcon(), func() { u.pickRunningProgram() })
	u.renderActivityRows()

	bold := fyne.TextStyle{Bold: true}
	// The rows end in a delete button; an empty box of its size keeps the
	// column titles above their entries.
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(widget.NewButtonWithIcon("", theme.DeleteIcon(), nil).MinSize())
	header := container.NewBorder(nil, nil, nil, gap,
		container.NewGridWithColumns(3,
			widget.NewLabelWithStyle(u.t("activity.col.process"), fyne.TextAlignLeading, bold),
			widget.NewLabelWithStyle(u.t("activity.col.label"), fyne.TextAlignLeading, bold),
			widget.NewLabelWithStyle(u.t("activity.col.kind"), fyne.TextAlignLeading, bold),
		))

	return container.NewVBox(
		a.toggle,
		hint(u.t("activity.hint")),
		widget.NewLabelWithStyle(u.t("activity.watch"), fyne.TextAlignLeading, bold),
		header,
		a.rows,
		container.NewHBox(a.addBtn, a.pickBtn, layout.NewSpacer()),
		hint(u.t("activity.watch.hint")),
	)
}

// renderActivityRows redraws the editor rows from a.watch. Each row edits
// its own entry in place, so typing never rebuilds the list (and never
// steals the focus); adding and removing do. UI thread only.
func (u *ui) renderActivityRows() {
	t := u.st
	if t == nil || t.activity.rows == nil {
		return
	}
	a := &t.activity
	a.rows.RemoveAll()
	if len(a.watch) == 0 {
		a.rows.Add(hint(u.t("activity.empty")))
	}
	kindLabels := activityKindLabels(u.lang)
	for i := range a.watch {
		w := a.watch[i]
		proc := widget.NewEntry()
		proc.SetPlaceHolder("steam.exe")
		proc.SetText(w.Process)
		proc.OnChanged = func(s string) {
			if i < len(a.watch) {
				a.watch[i].Process = s
				u.updateSTSaveState()
			}
		}
		label := widget.NewEntry()
		label.SetPlaceHolder(u.t("activity.ph.label"))
		label.SetText(w.Label)
		label.OnChanged = func(s string) {
			if i < len(a.watch) {
				a.watch[i].Label = s
				u.updateSTSaveState()
			}
		}
		kind := widget.NewSelect(kindLabels, nil)
		if k := slices.Index(activityKinds, w.Kind); k >= 0 {
			kind.SetSelectedIndex(k)
		} else {
			kind.SetSelectedIndex(len(activityKinds) - 1) // other
		}
		kind.OnChanged = func(string) {
			if k := kind.SelectedIndex(); k >= 0 && i < len(a.watch) {
				a.watch[i].Kind = activityKinds[k]
				u.updateSTSaveState()
			}
		}
		del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
			if i < len(a.watch) {
				a.watch = slices.Delete(slices.Clone(a.watch), i, i+1)
				u.renderActivityRows()
				u.updateSTSaveState()
			}
		})
		a.rows.Add(container.NewBorder(nil, nil, nil, del, container.NewGridWithColumns(3, proc, label, kind)))
	}
	a.rows.Refresh()
	if len(a.watch) >= activityMaxWatch {
		a.addBtn.Disable()
		a.pickBtn.Disable()
	} else {
		a.addBtn.Enable()
		a.pickBtn.Enable()
	}
	// The rows changed height after the tab was laid out (see renderHubs).
	if u.networkRoot != nil {
		u.networkRoot.Refresh()
	}
}

// fillActivityBox writes a into the editor. Called by fillSTSection, which
// holds the filling guard. UI thread only.
func (u *ui) fillActivityBox(a ActivityConfig) {
	t := u.st
	if t == nil || t.activity.toggle == nil {
		return
	}
	t.activity.toggle.SetChecked(a.Enabled)
	t.activity.watch = slices.Clone(a.Watch)
	u.renderActivityRows()
}

// pickRunningProgram fetches the running program names off the UI thread
// and opens the picker with them. The names stay in this dialog.
func (u *ui) pickRunningProgram() {
	go func() {
		names, err := u.client.RunningProcesses()
		fyne.Do(func() {
			if err != nil {
				u.markDisconnectedOnNetError(err)
				dialog.ShowError(fmt.Errorf(u.t("activity.pick.fail"), err), u.win)
				return
			}
			u.showProcessPicker(names)
		})
	}()
}

// showProcessPicker is the dialog: a search box over the names not yet on
// the list; picking one adds a row and closes it. UI thread only.
func (u *ui) showProcessPicker(running []string) {
	t := u.st
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
			u.updateSTSaveState()
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
