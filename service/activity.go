package service

// Running-app detection, opt-in (docs/design/media-notify.md §11, #110,
// #123).
//
// The user lists the programs worth reporting — "steam.exe, label it
// Steam" — in priority order, and the service reports, for each of them
// only, whether it is running. Nothing else about the process list leaves
// the scanner:
//
//   - A scan compares every running process's file name (case-insensitive)
//     against the watch list and keeps only the matching watch entries. The
//     names of other processes are neither stored, logged nor sent.
//   - What goes out (status, push, Telegram) is built from the watch list
//     alone: each entry's id (its own process name, lower-cased — a name
//     the user put on the list), its label and a running flag. A program
//     that is not on the list cannot reach the result.
//   - The one place the full list of names is handed out is GET
//     /api/processes, which feeds the desktop app's "pick from running
//     programs" dialog. It answers loopback callers with a valid session
//     only, so the list stays on this PC.
//
// While activity.enabled is off the scanner does not look at processes at
// all, and the status block says {enabled:false, apps:[], top:""}.

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
	// activityMaxWatch caps the watch list (§11): one SmartThings child
	// device per entry.
	activityMaxWatch = 10
	// activityMaxLabel is the longest label, in characters.
	activityMaxLabel = 30
	// activityMaxProcess bounds a process file name (MAX_PATH).
	activityMaxProcess = 260
	// activityScanInterval is how often the process list is read (§11).
	activityScanInterval = 10 * time.Second
	// processListMax bounds /api/processes; a PC has a few hundred.
	processListMax = 2000
)

// ActivityConfig is the "activity" object in config.json (§11).
type ActivityConfig struct {
	// Enabled turns the scanner on. Off by default.
	Enabled bool `json:"enabled"`
	// Watch is the list of programs to report, at most activityMaxWatch.
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

// ---- config ----------------------------------------------------------------

// withDefaults trims the entries, fills an empty label with the program
// name, and never returns a nil or aliased slice. It keeps the order and
// does not validate: see validateActivity / sanitizeActivity.
func (a ActivityConfig) withDefaults() ActivityConfig {
	out := ActivityConfig{Enabled: a.Enabled, Watch: make([]ActivityWatch, 0, len(a.Watch))}
	for _, w := range a.Watch {
		out.Watch = append(out.Watch, w.normalized())
	}
	return out
}

// normalized is one entry with the whitespace trimmed and the default
// label filled in.
func (w ActivityWatch) normalized() ActivityWatch {
	w.Process = strings.TrimSpace(w.Process)
	w.Label = strings.TrimSpace(w.Label)
	if w.Label == "" && w.Process != "" {
		w.Label = activityDefaultLabel(w.Process)
	}
	return w
}

// id is the entry's stable key in the status block: the process name,
// lower-cased. It survives label edits and reordering.
func (w ActivityWatch) id() string { return strings.ToLower(w.Process) }

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
		if seen[w.id()] {
			return fmt.Sprintf("activity.watch: %q is listed twice", w.Process)
		}
		seen[w.id()] = true
	}
	return ""
}

// sanitizeActivity is the load-time counterpart: config.json may have been
// edited by hand, and one bad entry must not cost the user the rest of the
// file. Invalid entries and duplicates are dropped with a log line each,
// the order of the rest is kept, and anything past the cap is cut with one
// log line; the result is always valid.
func sanitizeActivity(a ActivityConfig) ActivityConfig {
	a = a.withDefaults()
	out := ActivityConfig{Enabled: a.Enabled, Watch: []ActivityWatch{}}
	seen := map[string]bool{}
	over := 0
	for i, w := range a.Watch {
		if err := validateActivityWatch(w); err != nil {
			logMsg("WARNING: config.json activity.watch[%d] ignored: %v", i, err)
			continue
		}
		if seen[w.id()] {
			logMsg("WARNING: config.json activity.watch[%d] ignored: %q is listed twice", i, w.Process)
			continue
		}
		if len(out.Watch) >= activityMaxWatch {
			over++
			continue
		}
		seen[w.id()] = true
		out.Watch = append(out.Watch, w)
	}
	if over > 0 {
		logMsg("WARNING: config.json activity.watch holds more than %d programs; the last %d ignored", activityMaxWatch, over)
	}
	return out
}

// ---- matcher ---------------------------------------------------------------

// stActivity is the §11 status block, also the activity.changed push data.
type stActivity struct {
	Enabled bool `json:"enabled"`
	// Apps has one entry per watch entry, in priority order; never null.
	Apps []stActivityApp `json:"apps"`
	// Top is the id of the highest-priority running app, "" when none.
	Top string `json:"top"`
}

// stActivityApp is one watched program in the status block.
type stActivityApp struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Running bool   `json:"running"`
}

// activityOff is the block while the option is off.
func activityOff() stActivity {
	return stActivity{Enabled: false, Apps: []stActivityApp{}, Top: ""}
}

// activityListed is the block for watch with nothing marked running: the
// answer until a scan has looked at the list.
func activityListed(watch []ActivityWatch) stActivity {
	return matchActivity(watch, nil)
}

// matchActivity compares the running process names against watch. Every
// entry becomes one app, in list order, running when at least one process
// has its file name (case-insensitive); top is the first running one.
// Nothing from running that is not on the list can reach the result: only
// watch entries are ever copied.
func matchActivity(watch []ActivityWatch, running []string) stActivity {
	out := stActivity{Enabled: true, Apps: make([]stActivityApp, 0, len(watch))}
	wanted := make(map[string]bool, len(watch))
	for _, w := range watch {
		wanted[w.id()] = true
	}
	// Only names on the list are remembered, so the set never holds the
	// rest of the process list either.
	present := map[string]bool{}
	for _, name := range running {
		if key := strings.ToLower(name); wanted[key] {
			present[key] = true
		}
	}
	for _, w := range watch {
		app := stActivityApp{ID: w.id(), Label: w.Label, Running: present[w.id()]}
		if app.Running && out.Top == "" {
			out.Top = app.ID
		}
		out.Apps = append(out.Apps, app)
	}
	return out
}

// equal compares two blocks, apps in order.
func (a stActivity) equal(b stActivity) bool {
	return a.Enabled == b.Enabled && a.Top == b.Top && slices.Equal(a.Apps, b.Apps)
}

// clone copies the block so a caller cannot alias the scanner's slice.
func (a stActivity) clone() stActivity {
	a.Apps = slices.Clone(a.Apps)
	if a.Apps == nil {
		a.Apps = []stActivityApp{}
	}
	return a
}

// topLabel is the label of the top app and how many others run.
func (a stActivity) topLabel() (label string, others int) {
	for _, app := range a.Apps {
		if !app.Running {
			continue
		}
		if app.ID == a.Top && label == "" {
			label = app.Label
			continue
		}
		others++
	}
	return label, others
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
// cache no longer counts, so a removed entry disappears from the status at
// once rather than on the next tick.
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

// activitySig identifies what a scan was made for: the switch, and the
// list with its order and labels.
func activitySig(a ActivityConfig) string {
	if !a.Enabled {
		return "off"
	}
	var b strings.Builder
	b.WriteString("on")
	for _, w := range a.Watch {
		fmt.Fprintf(&b, "\x00%s\x01%s", w.id(), w.Label)
	}
	return b.String()
}

// scan reads the process list for cfg (none at all while it is off),
// stores the result and reports it with whether it differs from the
// previous one — an app started or stopped, the list, its order or a label
// changed, or the option was switched. The very first result only sets the
// baseline (changed is false), so starting the service never invents a
// transition.
//
// When the process list cannot be read, a scan for an unchanged config
// keeps the last result: what runs is unknown, and reporting everything as
// stopped would fire "stopped" routines for programs that are still open.
// For a new config there is no last result, so the apps are listed as not
// running.
func (s *activityScanner) scan(cfg ActivityConfig) (stActivity, bool) {
	sig := activitySig(cfg)
	next := activityOff()
	if cfg.Enabled {
		names, err := processLister()
		s.mu.Lock()
		wasFailing := s.failing
		s.failing = err != nil
		keep := s.known && s.sig == sig
		last := s.last.clone()
		s.mu.Unlock()
		switch {
		case err == nil:
			next = matchActivity(cfg.Watch, names)
		case keep:
			next = last
		default:
			next = activityListed(cfg.Watch)
		}
		if err != nil && !wasFailing {
			logMsg("Activity: process list unavailable: %v", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.known && !s.last.equal(next)
	s.last = next
	s.sig = sig
	s.known = true
	return next.clone(), changed
}

// current is the block for cfg from the last scan. It never scans itself —
// status and push bodies are built on request paths and must stay cheap —
// and it only trusts a scan made for this very config: while the option is
// off it reports off, and right after an edit it lists the apps as not
// running until the scanner (kicked by the save) has looked again.
func (s *activityScanner) current(cfg ActivityConfig) stActivity {
	if !cfg.Enabled {
		return activityOff()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known || s.sig != activitySig(cfg) {
		return activityListed(cfg.Watch)
	}
	return s.last.clone()
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

// activityTick runs one scan and reports a change as activity.changed. The
// push data is the status block itself, filled in at delivery (stPushBody),
// so the event carries no fields of its own.
func activityTick(cfg ActivityConfig) {
	next, changed := activityScan.scan(cfg)
	if !changed {
		return
	}
	logMsg("Activity: %s", activityLogLine(next))
	emitDevice("activity", "changed", nil)
}

// activityLogLine describes a block by the users' labels only.
func activityLogLine(a stActivity) string {
	if !a.Enabled {
		return "off"
	}
	var running []string
	for _, app := range a.Apps {
		if app.Running {
			running = append(running, app.Label)
		}
	}
	if len(running) == 0 {
		return fmt.Sprintf("nothing running (%d watched)", len(a.Apps))
	}
	return fmt.Sprintf("running %s (%d watched)", strings.Join(running, ", "), len(a.Apps))
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
		if validateActivityWatch(ActivityWatch{Process: n, Label: "x"}) != nil {
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
