// Package crashdump is debug mode's crash record (#133). A Go process that
// dies of an unrecovered panic or a fatal error — including a Windows
// exception in cgo or GL code, which the runtime reports as fatal — prints
// every goroutine's stack to stderr, and nobody sees the stderr of a
// service or a -H=windowsgui app. Enable points the runtime's extra crash
// output (runtime/debug.SetCrashOutput) at a file opened in advance, so
// that report survives the process.
//
// Each process that turns it on gets a file of its own:
//
//	<dir>\<prefix>-<yyyyMMdd-HHmmss>-<pid>.txt
//
// that starts with a short header (version, pid, session, start time).
// A process that ends normally — or turns debug mode off — removes its
// file again while it holds nothing but the header, so only crashes leave
// one behind. A process that was killed leaves its header-only file; the
// next Enable in the folder clears those away (Prune), and keeps the
// newest Keep real crash records of each prefix.
package crashdump

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// Keep is how many real crash records Prune leaves per prefix.
const Keep = 10

// headerEnd closes the header. Everything after it is the runtime's
// crash output; a file that ends right after it recorded no crash.
const headerEnd = "--- crash output follows ---\r\n"

// maxHeaderSize bounds the files Prune reads to tell a header-only file
// from a crash record: the header is a few hundred bytes, and a crash
// report with every goroutine's stack is never that short.
const maxHeaderSize = 4096

// ext is the crash files' extension.
const ext = ".txt"

// timeLayout is the file name's timestamp (yyyyMMdd-HHmmss, local time).
const timeLayout = "20060102-150405"

// reportedSuffix names the per-prefix list of crash records a process
// already announced (Unreported).
const reportedSuffix = ".reported"

// Options describe the process in the header and how the file is made.
type Options struct {
	// Version is the app version for the header ("v1.2.1", "dev").
	Version string
	// Create opens a new file at path for writing; nil is os.OpenFile
	// with O_EXCL. The service passes secureacl.CreatePrivateFile so its
	// records are SYSTEM and Administrators only.
	Create func(path string) (*os.File, error)
}

// state is the process's one active crash file.
var (
	mu     sync.Mutex
	active string // path of the open crash file, "" while off
)

// now and pid are replaced by the tests.
var (
	now = time.Now
	pid = os.Getpid
)

// Enable starts recording crashes in a new file under dir and returns its
// path. Header-only files of earlier processes and records beyond Keep
// are pruned first. Calling it again while on with the same dir and prefix
// does nothing and returns the current path; with another dir or prefix
// the current file is closed first, as by Disable.
func Enable(dir, prefix string, opt Options) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if dir == "" || prefix == "" || strings.ContainsAny(prefix, `-\/`) {
		return "", errors.New("crashdump: bad folder or prefix")
	}
	if active != "" {
		if filepath.Dir(active) == filepath.Clean(dir) && prefixOf(filepath.Base(active)) == prefix {
			return active, nil
		}
		disableLocked()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	prune(dir, Keep, "")

	start := now()
	path := filepath.Join(dir, fileName(prefix, start, pid()))
	create := opt.Create
	if create == nil {
		create = func(p string) (*os.File, error) {
			return os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		}
	}
	f, err := create(path)
	if err != nil {
		return "", err
	}
	// The runtime writes at the handle's offset, so the header goes first.
	if _, err := f.WriteString(header(prefix, opt.Version, start)); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	// SetCrashOutput keeps a duplicate of the handle; ours can go.
	err = debug.SetCrashOutput(f, debug.CrashOptions{})
	f.Close()
	if err != nil {
		os.Remove(path)
		return "", err
	}
	// Every goroutine's stack, not only the one that failed: the GUI's
	// crashes come from the Fyne event loop, the service's from handlers
	// running beside a dozen pollers.
	debug.SetTraceback("all")
	active = path
	return path, nil
}

// Disable stops recording and removes this process's file when it holds
// only the header. The traceback level goes back to the default (the
// GOTRACEBACK environment variable still wins, as always). A no-op while
// off; safe to call at every exit.
func Disable() {
	mu.Lock()
	defer mu.Unlock()
	disableLocked()
}

// disableLocked is Disable with mu held.
func disableLocked() {
	if active == "" {
		return
	}
	// This closes the runtime's duplicate handle; without it the file
	// could not be removed.
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	debug.SetTraceback("single")
	if headerOnly(active) {
		os.Remove(active)
	}
	active = ""
}

// Active is the path of this process's crash file, "" while off.
func Active() string {
	mu.Lock()
	defer mu.Unlock()
	return active
}

// Prune tidies dir: header-only files (processes that were killed, or are
// still running elsewhere — their open file cannot be removed, which is
// what keeps them) are removed, and of each prefix's real crash records
// only the newest keep stay. Files that do not follow the naming scheme
// are left alone, and so is this process's own file. Best effort; errors
// are ignored.
func Prune(dir string, keep int) {
	mu.Lock()
	defer mu.Unlock()
	prune(dir, keep, active)
}

// prune is Prune with mu held; skip is a path to leave alone.
func prune(dir string, keep int, skip string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	records := map[string][]fileInfo{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		prefix, ok := parseName(e.Name())
		if !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if path == skip {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() <= maxHeaderSize && headerOnly(path) {
			os.Remove(path)
			continue
		}
		records[prefix] = append(records[prefix], fileInfo{name: e.Name(), mod: info.ModTime()})
	}
	for _, list := range records {
		sortNewestFirst(list)
		for _, old := range list[min(keep, len(list)):] {
			os.Remove(filepath.Join(dir, old.name))
		}
	}
}

// Unreported lists the real crash records of prefix in dir that no
// earlier call announced, newest first, and remembers them in
// <dir>\<prefix>.reported so the next process does not repeat them.
// A process calls it once at start to log "previous crash recorded".
func Unreported(dir, prefix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	marker := filepath.Join(dir, prefix+reportedSuffix)
	seen := map[string]bool{}
	if data, err := os.ReadFile(marker); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				seen[line] = true
			}
		}
	}
	var all, fresh []fileInfo
	for _, e := range entries {
		p, ok := parseName(e.Name())
		if !ok || p != prefix || e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || headerOnly(path) {
			continue
		}
		fi := fileInfo{name: e.Name(), mod: info.ModTime()}
		all = append(all, fi)
		if !seen[e.Name()] {
			fresh = append(fresh, fi)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	// The list holds what is there now, so it never outgrows Keep for long.
	var b strings.Builder
	for _, fi := range all {
		b.WriteString(fi.name + "\n")
	}
	_ = os.WriteFile(marker, []byte(b.String()), 0o644)
	sortNewestFirst(fresh)
	out := make([]string, len(fresh))
	for i, fi := range fresh {
		out[i] = filepath.Join(dir, fi.name)
	}
	return out
}

// fileInfo is a crash record's name and modification time (when the
// runtime last wrote it, i.e. the crash).
type fileInfo struct {
	name string
	mod  time.Time
}

// sortNewestFirst orders by modification time, then by name (the start
// time) for files written in the same instant.
func sortNewestFirst(list []fileInfo) {
	slices.SortFunc(list, func(a, b fileInfo) int {
		if c := b.mod.Compare(a.mod); c != 0 {
			return c
		}
		return strings.Compare(b.name, a.name)
	})
}

// fileName is <prefix>-<yyyyMMdd-HHmmss>-<pid>.txt.
func fileName(prefix string, t time.Time, pid int) string {
	return fmt.Sprintf("%s-%s-%d%s", prefix, t.Format(timeLayout), pid, ext)
}

// parseName returns the prefix of a name that follows the scheme.
func parseName(name string) (prefix string, ok bool) {
	base, found := strings.CutSuffix(name, ext)
	if !found {
		return "", false
	}
	parts := strings.Split(base, "-")
	if len(parts) != 4 || parts[0] == "" || parts[3] == "" {
		return "", false
	}
	if _, err := time.Parse(timeLayout, parts[1]+"-"+parts[2]); err != nil {
		return "", false
	}
	for _, r := range parts[3] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return parts[0], true
}

// prefixOf is parseName's prefix, "" for a foreign name.
func prefixOf(name string) string {
	p, _ := parseName(name)
	return p
}

// header is what a crash file starts with.
func header(prefix, version string, start time.Time) string {
	if version == "" {
		version = "unknown"
	}
	exe, _ := os.Executable()
	var session uint32
	sessionText := "?"
	if windows.ProcessIdToSessionId(uint32(pid()), &session) == nil {
		sessionText = fmt.Sprint(session)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SmartThings PC Control crash record (%s)\r\n", prefix)
	fmt.Fprintf(&b, "version: %s\r\n", version)
	fmt.Fprintf(&b, "pid: %d, session: %s\r\n", pid(), sessionText)
	fmt.Fprintf(&b, "started: %s\r\n", start.Format(time.RFC3339))
	fmt.Fprintf(&b, "exe: %s\r\n", exe)
	b.WriteString(headerEnd)
	return b.String()
}

// headerOnly reports whether path is one of ours with nothing after the
// header. A file that cannot be read, or holds more, is not.
func headerOnly(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxHeaderSize {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	i := bytes.Index(data, []byte(headerEnd))
	return i >= 0 && len(bytes.TrimSpace(data[i+len(headerEnd):])) == 0
}
