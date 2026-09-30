package service

// Running-app detection, opt-in (docs/design/media-notify.md §11, #110).
//
// The user lists the programs worth reporting — "steam.exe is a game, label
// it Steam" — and the service reports only which of those are running, as
// a kind and the user's labels. Nothing else about the process list leaves
// the scanner:
//
//   - A scan compares every running process's file name (case-insensitive)
//     against the watch list and keeps only the matching watch entries. The
//     names of other processes are neither stored, logged nor sent.
//   - What goes out (status, push, Telegram) is the entry's kind and label,
//     never a process name, so even a watched program is reported by the
//     name the user chose.
//   - The one place the full list of names is handed out is GET
//     /api/processes, which feeds the desktop app's "pick from running
//     programs" dialog. It answers loopback callers with a valid session
//     only, so the list stays on this PC.
//
// While activity.enabled is off the scanner does not look at processes at
// all, and the status block says {enabled:false, kind:"none", labels:[]}.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// activityMaxWatch caps the watch list (§11).
	activityMaxWatch = 20
	// activityMaxLabel is the longest label, in characters.
	activityMaxLabel = 30
	// activityMaxProcess bounds a process file name (MAX_PATH).
	activityMaxProcess = 260
	// activityScanInterval is how often the process list is read (§11).
	activityScanInterval = 10 * time.Second
	// activityKindNone is the kind reported when nothing watched runs.
	activityKindNone = "none"
	// processListMax bounds /api/processes; a PC has a few hundred.
	processListMax = 2000
)

// activityKinds are the accepted kinds, highest priority first: when
// several watched programs run, the status reports the first kind here.
var activityKinds = []string{"game", "stream", "media", "work", "other"}

// ActivityConfig is the "activity" object in config.json (§11).
type ActivityConfig struct {
	// Enabled turns the scanner on. Off by default.
	Enabled bool `json:"enabled"`
	// Watch is the list of programs to report, at most activityMaxWatch.
	Watch []ActivityWatch `json:"watch"`
}

// ActivityWatch is one watched program. Process is a file name only
// ("steam.exe"), matched case-insensitively; Label is what is reported
// instead of it; Kind is one of activityKinds.
type ActivityWatch struct {
	Process string `json:"process"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
}

// ---- config ----------------------------------------------------------------

// withDefaults trims the entries, fills an empty kind with "other" and an
// empty label with the program name, and never returns a nil or aliased
// slice. It does not validate: see validateActivity / sanitizeActivity.
func (a ActivityConfig) withDefaults() ActivityConfig {
	out := ActivityConfig{Enabled: a.Enabled, Watch: make([]ActivityWatch, 0, len(a.Watch))}
	for _, w := range a.Watch {
		out.Watch = append(out.Watch, w.normalized())
	}
	return out
}

// normalized is one entry with the whitespace trimmed and the defaults
// filled in.
func (w ActivityWatch) normalized() ActivityWatch {
	w.Process = strings.TrimSpace(w.Process)
	w.Label = strings.TrimSpace(w.Label)
	w.Kind = strings.ToLower(strings.TrimSpace(w.Kind))
	if w.Kind == "" {
		w.Kind = "other"
	}
	if w.Label == "" && w.Process != "" {
		w.Label = activityDefaultLabel(w.Process)
	}
	return w
}

// activityDefaultLabel is "steam" for "steam.exe": the label an entry gets
// when the user leaves it blank.
func activityDefaultLabel(process string) string {
	if len(process) > 4 && strings.EqualFold(process[len(process)-4:], ".exe") {
		process = process[:len(process)-4]
	}
	return truncateRunes(process, activityMaxLabel)
}

// truncateRunes cuts s to max runes, without an ellipsis.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// validateActivityWatch checks one (normalized) entry.
func validateActivityWatch(w ActivityWatch) error {
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
	if utf8.RuneCountInString(w.Label) > activityMaxLabel {
		return fmt.Errorf("label for %q must be at most %d characters", p, activityMaxLabel)
	}
	if strings.IndexFunc(w.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("label for %q contains control characters", p)
	}
	if !slices.Contains(activityKinds, w.Kind) {
		return fmt.Errorf("kind for %q must be one of %s", p, strings.Join(activityKinds, ", "))
	}
	return nil
}

// validateActivity is the save-time check (/api/config POST): an empty
// string when a is acceptable, otherwise the message the client shows. a
// is expected to be normalized (withDefaults) already.
func validateActivity(a ActivityConfig) string {
	if len(a.Watch) > activityMaxWatch {
		return fmt.Sprintf("activity.watch may hold at most %d programs (got %d)", activityMaxWatch, len(a.Watch))
	}
	seen := map[string]bool{}
	for _, w := range a.Watch {
		if err := validateActivityWatch(w); err != nil {
			return "activity.watch: " + err.Error()
		}
		key := strings.ToLower(w.Process)
		if seen[key] {
			return fmt.Sprintf("activity.watch: %q is listed twice", w.Process)
		}
		seen[key] = true
	}
	return ""
}

// sanitizeActivity is the load-time counterpart: config.json may have been
// edited by hand, and one bad entry must not cost the user the rest of the
// file. Invalid entries, duplicates and anything past the cap are dropped,
// each with a log line; the result is always valid.
func sanitizeActivity(a ActivityConfig) ActivityConfig {
	a = a.withDefaults()
	out := ActivityConfig{Enabled: a.Enabled, Watch: []ActivityWatch{}}
	seen := map[string]bool{}
	for i, w := range a.Watch {
		if err := validateActivityWatch(w); err != nil {
			logMsg("WARNING: config.json activity.watch[%d] ignored: %v", i, err)
			continue
		}
		key := strings.ToLower(w.Process)
		if seen[key] {
			logMsg("WARNING: config.json activity.watch[%d] ignored: %q is listed twice", i, w.Process)
			continue
		}
		if len(out.Watch) >= activityMaxWatch {
			logMsg("WARNING: config.json activity.watch[%d] ignored: at most %d programs", i, activityMaxWatch)
			continue
		}
		seen[key] = true
		out.Watch = append(out.Watch, w)
	}
	return out
}

// ---- matcher ---------------------------------------------------------------

// stActivity is the §11 status block.
type stActivity struct {
	Enabled bool     `json:"enabled"`
	Kind    string   `json:"kind"`
	Labels  []string `json:"labels"`
}

// activityOff is the block while the option is off.
func activityOff() stActivity {
	return stActivity{Enabled: false, Kind: activityKindNone, Labels: []string{}}
}

// activityPriority is the rank of kind (0 = highest); unknown kinds sort
// last.
func activityPriority(kind string) int {
	if i := slices.Index(activityKinds, kind); i >= 0 {
		return i
	}
	return len(activityKinds)
}

// matchActivity compares the running process names against watch. The
// result's kind is the highest-priority kind among the running entries (or
// "none"), and its labels are theirs, ordered by kind priority and then by
// watch-list order, without duplicates. Nothing from running that is not
// on the list can reach the result: only watch entries are ever copied.
func matchActivity(watch []ActivityWatch, running []string) stActivity {
	out := stActivity{Enabled: true, Kind: activityKindNone, Labels: []string{}}
	if len(watch) == 0 || len(running) == 0 {
		return out
	}
	wanted := make(map[string]bool, len(watch))
	for _, w := range watch {
		wanted[strings.ToLower(w.Process)] = true
	}
	// Only names on the list are remembered, so the set never holds the
	// rest of the process list either.
	present := map[string]bool{}
	for _, name := range running {
		if key := strings.ToLower(name); wanted[key] {
			present[key] = true
		}
	}
	var active []ActivityWatch
	for _, w := range watch {
		if present[strings.ToLower(w.Process)] {
			active = append(active, w)
		}
	}
	if len(active) == 0 {
		return out
	}
	slices.SortStableFunc(active, func(a, b ActivityWatch) int {
		return activityPriority(a.Kind) - activityPriority(b.Kind)
	})
	out.Kind = active[0].Kind
	for _, w := range active {
		if !slices.Contains(out.Labels, w.Label) {
			out.Labels = append(out.Labels, w.Label)
		}
	}
	return out
}

// equal compares two blocks, labels in order.
func (a stActivity) equal(b stActivity) bool {
	return a.Enabled == b.Enabled && a.Kind == b.Kind && slices.Equal(a.Labels, b.Labels)
}

// ---- process list ----------------------------------------------------------

// processLister returns the file names of the running processes. It is a
// variable so the tests can drive the scanner without Toolhelp.
var processLister = toolhelpProcessNames

// toolhelpProcessNames reads every process in every session with one
// Toolhelp snapshot. The service runs as LocalSystem, so it sees the user
// session's programs as well as its own. The names only live in the
// returned slice; the callers decide what, if anything, to keep.
func toolhelpProcessNames() ([]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return nil, fmt.Errorf("Process32First: %w", err)
	}
	names := make([]string, 0, 256)
	for {
		names = append(names, windows.UTF16ToString(e.ExeFile[:]))
		if err := windows.Process32Next(snap, &e); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("Process32Next: %w", err)
		}
	}
	return names, nil
}

// ---- scanner ---------------------------------------------------------------

// activityScanner holds the last scan and the configuration it was made
// for. sig ties the cached result to the watch list: after an edit the
// cache no longer counts, so a removed entry's label disappears from the
// status at once rather than on the next tick.
type activityScanner struct {
	mu    sync.Mutex
	last  stActivity
	sig   string
	known bool
	// failing remembers that the last scan failed, so a persistent error
	// is logged once rather than every 10 seconds.
	failing bool
}

var activityScan = &activityScanner{}

// activityKick wakes the scanner after a config save, so enabling the
// option or editing the list shows up in the status without waiting for
// the next tick.
var activityKick = make(chan struct{}, 1)

// kickActivityScan asks for an early scan; it never blocks.
func kickActivityScan() {
	select {
	case activityKick <- struct{}{}:
	default:
	}
}

// activitySig identifies what a scan was made for.
func activitySig(a ActivityConfig) string {
	if !a.Enabled {
		return "off"
	}
	var b strings.Builder
	b.WriteString("on")
	for _, w := range a.Watch {
		fmt.Fprintf(&b, "\x00%s\x01%s\x01%s", strings.ToLower(w.Process), w.Label, w.Kind)
	}
	return b.String()
}

// scan reads the process list for cfg (none at all while it is off),
// stores the result and reports it with whether it differs from the
// previous one. The very first result only sets the baseline (changed is
// false), so starting the service never invents a transition.
func (s *activityScanner) scan(cfg ActivityConfig) (stActivity, bool) {
	next := activityOff()
	if cfg.Enabled {
		names, err := processLister()
		s.mu.Lock()
		wasFailing := s.failing
		s.failing = err != nil
		s.mu.Unlock()
		if err != nil {
			if !wasFailing {
				logMsg("Activity: process list unavailable: %v", err)
			}
			// What runs is unknown, so nothing is reported as running.
			next = stActivity{Enabled: true, Kind: activityKindNone, Labels: []string{}}
		} else {
			next = matchActivity(cfg.Watch, names)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.known && !s.last.equal(next)
	s.last = next
	s.sig = activitySig(cfg)
	s.known = true
	return next, changed
}

// current is the block for cfg from the last scan. It never scans itself —
// status and push bodies are built on request paths and must stay cheap —
// and it only trusts a scan made for this very config: while the option is
// off it reports off, and right after an edit it reports "none" until the
// scanner has looked again.
func (s *activityScanner) current(cfg ActivityConfig) stActivity {
	if !cfg.Enabled {
		return activityOff()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known || s.sig != activitySig(cfg) {
		return stActivity{Enabled: true, Kind: activityKindNone, Labels: []string{}}
	}
	out := s.last
	out.Labels = slices.Clone(s.last.Labels)
	return out
}

// reset forgets the last scan (tests).
func (s *activityScanner) reset() {
	s.mu.Lock()
	s.last, s.sig, s.known, s.failing = stActivity{}, "", false, false
	s.mu.Unlock()
}

// stActivityStatus is the status block for the live config.
func stActivityStatus(cfg Config) stActivity {
	return activityScan.current(cfg.Activity)
}

// activityTick runs one scan and reports a change as activity.changed.
func activityTick(cfg ActivityConfig) {
	next, changed := activityScan.scan(cfg)
	if !changed {
		return
	}
	logMsg("Activity: %s", activityLogLine(next))
	emitDevice("activity", "changed", activityFields(next))
}

// activityLogLine describes a block by kind and labels only.
func activityLogLine(a stActivity) string {
	if !a.Enabled {
		return "off"
	}
	if len(a.Labels) == 0 {
		return a.Kind
	}
	return a.Kind + " (" + strings.Join(a.Labels, ", ") + ")"
}

// activityFields is the activity.changed event data. The full block rides
// along in the push body's status anyway; these are for a reader of the
// event alone.
func activityFields(a stActivity) map[string]string {
	enabled := "false"
	if a.Enabled {
		enabled = "true"
	}
	return map[string]string{
		"enabled": enabled,
		"kind":    a.Kind,
		"labels":  strings.Join(a.Labels, ", "),
	}
}

// watchActivity scans every activityScanInterval, and early after a save,
// until stop closes. While the option is off a tick costs a config read:
// scan does not touch the process list then.
func watchActivity(stop <-chan struct{}) {
	t := time.NewTicker(activityScanInterval)
	defer t.Stop()
	activityTick(getConfig().Activity)
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		case <-activityKick:
		}
		activityTick(getConfig().Activity)
	}
}

// ---- /api/processes (the app's picker) -------------------------------------

// runningProcessNames is the picker list: the unique .exe names, sorted
// case-insensitively. Names that could not go on the watch list anyway
// ("System", "Registry", "[System Process]") are left out.
func runningProcessNames() ([]string, error) {
	names, err := processLister()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if validateActivityWatch(ActivityWatch{Process: n, Label: "x", Kind: "other"}) != nil {
			continue
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
		if len(out) >= processListMax {
			break
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	return out, nil
}

// isLoopbackRequest reports whether r came from this PC.
func isLoopbackRequest(r *http.Request) bool {
	ip := net.ParseIP(remoteHost(r.RemoteAddr))
	return ip != nil && ip.IsLoopback()
}

// handleProcessesAPI serves GET /api/processes for the desktop app's
// "pick from running programs" dialog. Besides the usual session check it
// refuses anything but a loopback caller: the WebUI may be open to the LAN
// (webui_remote), and the process list is meant for this PC's screen only.
// Nothing is logged about the names.
func handleProcessesAPI(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r, getConfig().Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r) {
		writeAPIError(w, http.StatusForbidden, "The process list is only available on this PC.")
		return
	}
	names, err := runningProcessNames()
	if err != nil {
		logMsg("Activity: process list for the app unavailable: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "The process list is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"processes": names})
}
