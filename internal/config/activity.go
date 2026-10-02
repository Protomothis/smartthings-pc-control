package config

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
)

const (
	// ActivityMaxWatch caps the watch list (media-notify doc §11) and is
	// also the highest slot: the PC device's watch-list card has the five
	// routine conditions "감시 1".."감시 5".
	ActivityMaxWatch = 5
	// ActivityMinSlot is the first slot, the highest priority.
	ActivityMinSlot = 1
	// ActivityMaxLabel is the longest label, in characters.
	ActivityMaxLabel = 30
	// activityMaxProcess bounds a process file name (MAX_PATH).
	activityMaxProcess = 260
)

// ActivityConfig is the "activity" object in config.json (§11, #110,
// #123; the scanner is service/activity).
type ActivityConfig struct {
	// Enabled turns the scanner on. Off by default.
	Enabled bool `json:"enabled"`
	// Watch is the list of programs to report, at most ActivityMaxWatch,
	// each in its own slot. The slot is the priority (1 ranks highest) and
	// the SmartThings routine condition ("감시 1"..) the entry answers to.
	Watch []ActivityWatch `json:"watch"`
}

// ActivityWatch is one watched program. Slot is 1–ActivityMaxWatch and
// unique; Process is a file name only ("steam.exe"), matched
// case-insensitively; Label is the name it is shown by. A "kind" key left
// over from v1.2.0 development builds is ignored by the decoder and
// dropped on the next save; an entry without "slot" (the same builds) is
// given one on load (SanitizeActivity).
type ActivityWatch struct {
	Slot    int    `json:"slot"`
	Process string `json:"process"`
	Label   string `json:"label"`
}

// WithDefaults trims the entries, fills an empty label with the program
// name, sorts the list by slot (stable) and never returns a nil or aliased
// slice. It does not validate: see ValidateActivity / SanitizeActivity.
func (a ActivityConfig) WithDefaults() ActivityConfig {
	out := ActivityConfig{Enabled: a.Enabled, Watch: make([]ActivityWatch, 0, len(a.Watch))}
	for _, w := range a.Watch {
		out.Watch = append(out.Watch, w.Normalized())
	}
	SortWatch(out.Watch)
	return out
}

// SortWatch orders entries by slot in place, keeping the order of equal
// slots.
func SortWatch(watch []ActivityWatch) {
	slices.SortStableFunc(watch, func(a, b ActivityWatch) int { return cmp.Compare(a.Slot, b.Slot) })
}

// validSlot reports whether slot is one of the watch list's slots.
func validSlot(slot int) bool {
	return slot >= ActivityMinSlot && slot <= ActivityMaxWatch
}

// Normalized is one entry with the whitespace trimmed and the default
// label filled in.
func (w ActivityWatch) Normalized() ActivityWatch {
	w.Process = strings.TrimSpace(w.Process)
	w.Label = strings.TrimSpace(w.Label)
	if w.Label == "" && w.Process != "" {
		w.Label = activityDefaultLabel(w.Process)
	}
	return w
}

// ID is the entry's stable key in the status block: the process name,
// lower-cased. It survives label and slot edits.
func (w ActivityWatch) ID() string { return strings.ToLower(w.Process) }

// activityDefaultLabel is "steam" for "steam.exe": the label an entry gets
// when the user leaves it blank.
func activityDefaultLabel(process string) string {
	if len(process) > 4 && strings.EqualFold(process[len(process)-4:], ".exe") {
		process = process[:len(process)-4]
	}
	return truncateRunes(process, ActivityMaxLabel)
}

// truncateRunes cuts s to max runes, without an ellipsis.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// ValidateActivityWatch checks one (normalized) entry's process and label;
// the slot is the list's business (ValidateActivity). The app's picker
// (/api/processes) uses it to leave out names that could never be listed.
func ValidateActivityWatch(w ActivityWatch) error {
	p := w.Process
	switch {
	case p == "":
		return errors.New("process is required")
	case len(p) > activityMaxProcess:
		return fmt.Errorf("process %q is too long", truncate(p, 40))
	case strings.ContainsAny(p, `\/:*?"<>|`):
		// A file name only: a path would be a different, weaker match
		// (Toolhelp reports the bare name) and invites confusion.
		return fmt.Errorf("process %q must be a file name without a path", truncate(p, 40))
	case strings.IndexFunc(p, unicode.IsControl) >= 0:
		return fmt.Errorf("process %q contains control characters", truncate(p, 40))
	case len(p) <= 4 || !strings.EqualFold(p[len(p)-4:], ".exe"):
		return fmt.Errorf("process %q must end with .exe", truncate(p, 40))
	}
	if w.Label == "" {
		return fmt.Errorf("label for %q is required", p)
	}
	if utf8.RuneCountInString(w.Label) > ActivityMaxLabel {
		return fmt.Errorf("label for %q must be at most %d characters", p, ActivityMaxLabel)
	}
	if strings.IndexFunc(w.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("label for %q contains control characters", p)
	}
	return nil
}

// ValidateActivity is the save-time check (/api/config POST): an empty
// string when a is acceptable, otherwise the message the client shows. a
// is expected to be normalized (WithDefaults) already. Every entry needs
// a slot of its own: a POST is never given slots the way a load is.
func ValidateActivity(a ActivityConfig) string {
	if len(a.Watch) > ActivityMaxWatch {
		return fmt.Sprintf("activity.watch may hold at most %d programs (got %d)", ActivityMaxWatch, len(a.Watch))
	}
	seen := map[string]bool{}
	slots := map[int]bool{}
	for _, w := range a.Watch {
		if !validSlot(w.Slot) {
			return fmt.Sprintf("activity.watch: slot for %q must be %d-%d (got %d)",
				truncate(w.Process, 40), ActivityMinSlot, ActivityMaxWatch, w.Slot)
		}
		if slots[w.Slot] {
			return fmt.Sprintf("activity.watch: slot %d is used twice", w.Slot)
		}
		slots[w.Slot] = true
		if err := ValidateActivityWatch(w); err != nil {
			return "activity.watch: " + err.Error()
		}
		if seen[w.ID()] {
			return fmt.Sprintf("activity.watch: %q is listed twice", w.Process)
		}
		seen[w.ID()] = true
	}
	return ""
}

// SanitizeActivity is the load-time counterpart: config.json may have been
// edited by hand, and one bad entry must not cost the user the rest of the
// file. Invalid entries and repeated programs are dropped with a log line
// each. An entry whose slot is missing (a config.json from before slots),
// out of range or taken by an earlier entry gets the lowest free slot, in
// list order — so an old list becomes slots 1, 2, … in its order — and
// whatever finds no free slot is dropped, with one log line. The result is
// sorted by slot and always valid; the next save writes the slots.
func SanitizeActivity(a ActivityConfig) ActivityConfig {
	var kept []ActivityWatch
	seen := map[string]bool{}
	for i, w := range a.Watch {
		w = w.Normalized()
		if err := ValidateActivityWatch(w); err != nil {
			logx.Printf("WARNING: config.json activity.watch[%d] ignored: %v", i, err)
			continue
		}
		if seen[w.ID()] {
			logx.Printf("WARNING: config.json activity.watch[%d] ignored: %q is listed twice", i, w.Process)
			continue
		}
		seen[w.ID()] = true
		kept = append(kept, w)
	}

	// Entries with a usable slot keep it; of two claiming one, the first.
	used := map[int]bool{}
	needs := make([]bool, len(kept))
	unslotted, moved := 0, 0
	for i, w := range kept {
		switch {
		case w.Slot == 0:
			needs[i] = true
			unslotted++
		case !validSlot(w.Slot) || used[w.Slot]:
			needs[i] = true
			moved++
		default:
			used[w.Slot] = true
		}
	}
	// The others take the free slots in list order, while there are any.
	out := ActivityConfig{Enabled: a.Enabled, Watch: []ActivityWatch{}}
	over, next := 0, ActivityMinSlot
	for i, w := range kept {
		if needs[i] {
			for next <= ActivityMaxWatch && used[next] {
				next++
			}
			if next > ActivityMaxWatch {
				over++
				continue
			}
			w.Slot = next
			used[next] = true
		}
		out.Watch = append(out.Watch, w)
	}
	SortWatch(out.Watch)

	if unslotted > 0 {
		logx.Printf("Activity: config.json activity.watch had %d entries without a slot; slots given in list order", unslotted)
	}
	if moved > 0 {
		logx.Printf("WARNING: config.json activity.watch: %d entries had an invalid or repeated slot; moved to a free one", moved)
	}
	if over > 0 {
		logx.Printf("WARNING: config.json activity.watch holds more than %d programs; the last %d ignored", ActivityMaxWatch, over)
	}
	return out
}
