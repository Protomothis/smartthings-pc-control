package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
)

const (
	// ActivityMaxWatch caps the watch list (media-notify doc §11): one
	// SmartThings child device per entry.
	ActivityMaxWatch = 10
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
	// Watch is the list of programs to report, at most ActivityMaxWatch.
	// Its order is the priority: the first entry ranks highest.
	Watch []ActivityWatch `json:"watch"`
}

// ActivityWatch is one watched program. Process is a file name only
// ("steam.exe"), matched case-insensitively; Label is the name it is shown
// by. A "kind" key left over from v1.2.0 development builds is ignored by
// the decoder and dropped on the next save.
type ActivityWatch struct {
	Process string `json:"process"`
	Label   string `json:"label"`
}

// WithDefaults trims the entries, fills an empty label with the program
// name, and never returns a nil or aliased slice. It keeps the order and
// does not validate: see ValidateActivity / SanitizeActivity.
func (a ActivityConfig) WithDefaults() ActivityConfig {
	out := ActivityConfig{Enabled: a.Enabled, Watch: make([]ActivityWatch, 0, len(a.Watch))}
	for _, w := range a.Watch {
		out.Watch = append(out.Watch, w.Normalized())
	}
	return out
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
// lower-cased. It survives label edits and reordering.
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

// ValidateActivityWatch checks one (normalized) entry. The app's picker
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
// is expected to be normalized (WithDefaults) already.
func ValidateActivity(a ActivityConfig) string {
	if len(a.Watch) > ActivityMaxWatch {
		return fmt.Sprintf("activity.watch may hold at most %d programs (got %d)", ActivityMaxWatch, len(a.Watch))
	}
	seen := map[string]bool{}
	for _, w := range a.Watch {
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
// file. Invalid entries and duplicates are dropped with a log line each,
// the order of the rest is kept, and anything past the cap is cut with one
// log line; the result is always valid.
func SanitizeActivity(a ActivityConfig) ActivityConfig {
	a = a.WithDefaults()
	out := ActivityConfig{Enabled: a.Enabled, Watch: []ActivityWatch{}}
	seen := map[string]bool{}
	over := 0
	for i, w := range a.Watch {
		if err := ValidateActivityWatch(w); err != nil {
			logx.Printf("WARNING: config.json activity.watch[%d] ignored: %v", i, err)
			continue
		}
		if seen[w.ID()] {
			logx.Printf("WARNING: config.json activity.watch[%d] ignored: %q is listed twice", i, w.Process)
			continue
		}
		if len(out.Watch) >= ActivityMaxWatch {
			over++
			continue
		}
		seen[w.ID()] = true
		out.Watch = append(out.Watch, w)
	}
	if over > 0 {
		logx.Printf("WARNING: config.json activity.watch holds more than %d programs; the last %d ignored", ActivityMaxWatch, over)
	}
	return out
}
