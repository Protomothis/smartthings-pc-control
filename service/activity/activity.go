// Package activity is the opt-in running-app detection
// (docs/design/media-notify.md §11, #110, #123).
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
//     programs" dialog (PickerNames). It answers loopback callers with a
//     valid session only, so the list stays on this PC.
//
// While activity.enabled is off the scanner does not look at processes at
// all, and the status block says {enabled:false, apps:[], top:""}. The
// watch list itself (ActivityConfig, its limits and rules) is
// internal/config.
package activity

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/status"

	"golang.org/x/sys/windows"
)

// pickerMax bounds /api/processes; a PC has a few hundred.
const pickerMax = 2000

// ---- matcher ---------------------------------------------------------------

// Off is the block while the option is off.
func Off() status.Activity {
	return status.Activity{Enabled: false, Apps: []status.ActivityApp{}, Top: ""}
}

// Listed is the block for watch with nothing marked running: the answer
// until a scan has looked at the list.
func Listed(watch []config.ActivityWatch) status.Activity {
	return Match(watch, nil)
}

// Match compares the running process names against watch. Every entry
// becomes one app, in list order, running when at least one process has
// its file name (case-insensitive); top is the first running one. Nothing
// from running that is not on the list can reach the result: only watch
// entries are ever copied.
func Match(watch []config.ActivityWatch, running []string) status.Activity {
	out := status.Activity{Enabled: true, Apps: make([]status.ActivityApp, 0, len(watch))}
	wanted := make(map[string]bool, len(watch))
	for _, w := range watch {
		wanted[w.ID()] = true
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
		app := status.ActivityApp{ID: w.ID(), Label: w.Label, Running: present[w.ID()]}
		if app.Running && out.Top == "" {
			out.Top = app.ID
		}
		out.Apps = append(out.Apps, app)
	}
	return out
}

// LogLine describes a block by the users' labels only.
func LogLine(a status.Activity) string {
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

// ---- process list ----------------------------------------------------------

// ToolhelpNames reads every process in every session with one Toolhelp
// snapshot. The service runs as LocalSystem, so it sees the user session's
// programs as well as its own. The names only live in the returned slice;
// the callers decide what, if anything, to keep.
func ToolhelpNames() ([]string, error) {
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

// PickerNames is the app's picker list (/api/processes): the unique .exe
// names, sorted case-insensitively. Names that could not go on the watch
// list anyway ("System", "Registry", "[System Process]") are left out.
func PickerNames(names []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if config.ValidateActivityWatch(config.ActivityWatch{Process: n, Label: "x"}) != nil {
			continue
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
		if len(out) >= pickerMax {
			break
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	return out
}

// ---- scanner ---------------------------------------------------------------

// Scanner holds the last scan and the configuration it was made for. sig
// ties the cached result to the watch list: after an edit the cache no
// longer counts, so a removed entry disappears from the status at once
// rather than on the next tick.
type Scanner struct {
	// List reads the running processes' file names (ToolhelpNames in the
	// service; the tests feed their own).
	List func() ([]string, error)

	mu    sync.Mutex
	last  status.Activity
	sig   string
	known bool
	// failing remembers that the last scan failed, so a persistent error
	// is logged once rather than every 10 seconds.
	failing bool
}

// sigOf identifies what a scan was made for: the switch, and the list with
// its order and labels.
func sigOf(a config.ActivityConfig) string {
	if !a.Enabled {
		return "off"
	}
	var b strings.Builder
	b.WriteString("on")
	for _, w := range a.Watch {
		fmt.Fprintf(&b, "\x00%s\x01%s", w.ID(), w.Label)
	}
	return b.String()
}

// Scan reads the process list for cfg (none at all while it is off),
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
func (s *Scanner) Scan(cfg config.ActivityConfig) (status.Activity, bool) {
	sig := sigOf(cfg)
	next := Off()
	if cfg.Enabled {
		names, err := s.List()
		s.mu.Lock()
		wasFailing := s.failing
		s.failing = err != nil
		keep := s.known && s.sig == sig
		last := s.last.Clone()
		s.mu.Unlock()
		switch {
		case err == nil:
			next = Match(cfg.Watch, names)
		case keep:
			next = last
		default:
			next = Listed(cfg.Watch)
		}
		if err != nil && !wasFailing {
			logx.Printf("Activity: process list unavailable: %v", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.known && !s.last.Equal(next)
	s.last = next
	s.sig = sig
	s.known = true
	return next.Clone(), changed
}

// Current is the block for cfg from the last scan. It never scans itself —
// status and push bodies are built on request paths and must stay cheap —
// and it only trusts a scan made for this very config: while the option is
// off it reports off, and right after an edit it lists the apps as not
// running until the scanner (kicked by the save) has looked again.
func (s *Scanner) Current(cfg config.ActivityConfig) status.Activity {
	if !cfg.Enabled {
		return Off()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known || s.sig != sigOf(cfg) {
		return Listed(cfg.Watch)
	}
	return s.last.Clone()
}

// Reset forgets the last scan (tests).
func (s *Scanner) Reset() {
	s.mu.Lock()
	s.last, s.sig, s.known, s.failing = status.Activity{}, "", false, false
	s.mu.Unlock()
}
