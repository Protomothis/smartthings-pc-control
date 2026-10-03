package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// SmartThings routines name a preset or a watched program by its slot
// ("프리셋 3", "감시 1"), not by what is in it. A save that puts something
// else in a slot that held something, or empties it, silently points every
// routine using that slot at the new thing — so the save asks first. A new
// entry in an empty slot changes no routine and is not asked about.

// slotChange is one slot whose owner a save changes.
type slotChange struct {
	// Watch is a watch slot ("감시 N"); otherwise a preset slot.
	Watch bool
	Slot  int
	// From is what the slot held, To what it will hold; "" is empty.
	From, To string
}

// samePresetOwner reports whether b is still a's preset: the same name
// (trimmed, any case) and the same target (any case, as Windows paths
// are). Type, arguments and spacing do not change who owns the slot.
func samePresetOwner(a, b Preset) bool {
	return presetNameKey(a.Name) == presetNameKey(b.Name) &&
		strings.EqualFold(strings.TrimSpace(a.Path), strings.TrimSpace(b.Path))
}

// presetSlotChanges lists the slots of old whose preset next replaces or
// drops (a preset moved to another slot leaves its old slot changed), in
// slot order.
func presetSlotChanges(old, next []Preset) []slotChange {
	bySlot := map[int]Preset{}
	for _, p := range next {
		bySlot[p.Slot] = p
	}
	var out []slotChange
	for _, o := range sortedPresets(old) {
		n, ok := bySlot[o.Slot]
		switch {
		case !ok:
			out = append(out, slotChange{Slot: o.Slot, From: strings.TrimSpace(o.Name)})
		case !samePresetOwner(o, n):
			from, to := strings.TrimSpace(o.Name), strings.TrimSpace(n.Name)
			if presetNameKey(from) == presetNameKey(to) {
				// Same name, other target: the target is the difference.
				from += " (" + strings.TrimSpace(o.Path) + ")"
				to += " (" + strings.TrimSpace(n.Path) + ")"
			}
			out = append(out, slotChange{Slot: o.Slot, From: from, To: to})
		}
	}
	return out
}

// watchSlotChanges lists the slots of old whose program next replaces or
// drops, in slot order. A program is its file name (any case); a new
// label alone keeps the slot's owner.
func watchSlotChanges(old, next []ActivityWatch) []slotChange {
	o := normalizeActivity(ActivityConfig{Watch: old}).Watch
	n := normalizeActivity(ActivityConfig{Watch: next}).Watch
	bySlot := map[int]ActivityWatch{}
	for _, w := range n {
		bySlot[w.Slot] = w
	}
	var out []slotChange
	for _, w := range o {
		nw, ok := bySlot[w.Slot]
		switch {
		case !ok:
			out = append(out, slotChange{Watch: true, Slot: w.Slot, From: w.Label})
		case !strings.EqualFold(w.Process, nw.Process):
			from, to := w.Label, nw.Label
			if strings.EqualFold(from, to) {
				from, to = w.Process, nw.Process
			}
			out = append(out, slotChange{Watch: true, Slot: w.Slot, From: from, To: to})
		}
	}
	return out
}

// configSlotChanges is every slot change saving next over old makes:
// the presets first, then the watch list.
func configSlotChanges(old, next Config) []slotChange {
	return append(presetSlotChanges(old.Presets, next.Presets),
		watchSlotChanges(old.Activity.Watch, next.Activity.Watch)...)
}

// slotChangeLine is one line of the question: "감시 1: Steam → OBS",
// "프리셋 3: 게임 모드 → (비어 있음)".
func slotChangeLine(l Lang, c slotChange) string {
	key := "slotchange.preset"
	if c.Watch {
		key = "slotchange.watch"
	}
	to := c.To
	if to == "" {
		to = T(l, "slotchange.empty")
	}
	return fmt.Sprintf(T(l, key), c.Slot, c.From, to)
}

// slotChangeBody is the whole question: the changes, then what they mean.
func slotChangeBody(l Lang, changes []slotChange) string {
	lines := make([]string, len(changes))
	for i, c := range changes {
		lines[i] = slotChangeLine(l, c)
	}
	return strings.Join(lines, "\n") + "\n\n" + T(l, "slotchange.body")
}

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
