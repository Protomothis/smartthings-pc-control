package gui

// The sharing screen's toolkit-free model (#128, #110, #123), shared by the
// Fyne tab (share_tab.go, activity_section.go) and the Gio page
// (ui/pages_sharing.go): the form as plain values, the watch-list rules
// and the exported surface the Gio page uses.

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits shared with the service (internal/config/activity.go): five
// entries, which is also the highest slot.
const (
	activityMaxWatch = 5
	activityMaxLabel = 30
)

// --- Pure form model (unit-tested) -----------------------------------------

// shareFormState is the tab's contents as plain values.
type shareFormState struct {
	ExposeSession     bool
	ExposeSessionUser bool
	NowPlaying        bool
	// Activity is the running-app detection toggle and watch list.
	Activity ActivityConfig
}

// shareStateFromConfig is what the tab shows for cfg. The watch list is
// copied so editing it never writes into the baseline.
func shareStateFromConfig(cfg Config) shareFormState {
	return shareFormState{
		ExposeSession:     cfg.SmartThings.ExposeSession,
		ExposeSessionUser: cfg.SmartThings.ExposeSessionUser,
		NowPlaying:        cfg.Media.NowPlaying,
		Activity:          cloneActivity(cfg.Activity),
	}
}

// effectiveUser is the value saved for expose_session_user: the toggle
// keeps its position while session exposure is off (like the telegram
// tab's category masters), but off gates it.
func (s shareFormState) effectiveUser() bool {
	return s.ExposeSession && s.ExposeSessionUser
}

// applyTo returns base with the tab's fields written over it — only those:
// the rest of smartthings and media belong to the SmartThings and settings
// tabs.
func (s shareFormState) applyTo(base Config) Config {
	cfg := base
	cfg.SmartThings.ExposeSession = s.ExposeSession
	cfg.SmartThings.ExposeSessionUser = s.effectiveUser()
	cfg.Media.NowPlaying = s.NowPlaying
	cfg.Activity = normalizeActivity(s.Activity)
	return cfg
}

// dirty reports whether saving would change base.
func (s shareFormState) dirty(base Config) bool {
	return s.ExposeSession != base.SmartThings.ExposeSession ||
		s.effectiveUser() != base.SmartThings.ExposeSessionUser ||
		s.NowPlaying != base.Media.NowPlaying ||
		!activityEqual(s.Activity, base.Activity)
}

// --- Watch list (activity) ---------------------------------------------------

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

// --- Exported for the Gio page ------------------------------------------------

// ActivityMaxWatch is the most watched programs, which is also the highest
// slot.
const ActivityMaxWatch = activityMaxWatch

// ShareForm is the sharing screen as plain values: session exposure (and
// the user name under it), now playing, and running-app detection with its
// watch list.
type ShareForm = shareFormState

// NewShareForm is what the screen shows for cfg; the watch list is a copy.
func NewShareForm(cfg Config) ShareForm { return shareStateFromConfig(cfg) }

// ApplyTo writes the form's fields over cfg and checks the watch list the
// way the service will, so the reason comes in l.
func (s shareFormState) ApplyTo(cfg *Config, l Lang) error {
	*cfg = s.applyTo(*cfg)
	if problem := activityProblem(l, s.Activity); problem != "" {
		return errors.New(problem)
	}
	return nil
}

// Dirty reports whether saving would change base.
func (s shareFormState) Dirty(base Config) bool { return s.dirty(base) }

// SortActivity is watch in slot order (a copy).
func SortActivity(watch []ActivityWatch) []ActivityWatch { return sortActivity(watch) }

// ActivitySlotChoices is what row i's slot choice offers: its own slot and
// the free ones, ascending.
func ActivitySlotChoices(watch []ActivityWatch, i int) []int { return activitySlotChoices(watch, i) }

// SetActivitySlot moves row i to slot when it is free; see setActivitySlot.
func SetActivitySlot(watch []ActivityWatch, i, slot int) ([]ActivityWatch, bool) {
	return setActivitySlot(watch, i, slot)
}

// AddActivityRow appends a blank row in the lowest free slot.
func AddActivityRow(watch []ActivityWatch) ([]ActivityWatch, bool) { return addActivityRow(watch) }

// AddActivityProcess adds a row for a picked running program.
func AddActivityProcess(watch []ActivityWatch, process string) ([]ActivityWatch, bool) {
	return addActivityProcess(watch, process)
}

// PickerCandidates is what the running-program picker offers for query.
func PickerCandidates(running []string, watch []ActivityWatch, query string) []string {
	return pickerCandidates(running, watch, query)
}
