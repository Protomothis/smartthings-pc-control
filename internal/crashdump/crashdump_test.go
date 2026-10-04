package crashdump

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeRecord puts a file named for prefix, start and pid into dir: the
// header alone, or the header and a fake crash report. Its modification
// time is mod.
func writeRecord(t *testing.T, dir, prefix string, start time.Time, pid int, crashed bool, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, fileName(prefix, start, pid))
	body := header(prefix, "test", start)
	if crashed {
		body += "panic: boom\r\n\r\ngoroutine 1 [running]:\r\nmain.main()\r\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

// names lists dir's files, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestParseName(t *testing.T) {
	for name, want := range map[string]string{
		"gui-20261004-101500-1234.txt":        "gui",
		"service-20261004-101500-8.txt":       "service",
		"useraction-20261004-101500-4321.txt": "useraction",
		"gui-20261004-101500-1234.log":        "",
		"gui-2026-101500-1234.txt":            "",
		"gui-20261004-101500-12a.txt":         "",
		"gui-20261004-101500-.txt":            "",
		"-20261004-101500-1.txt":              "",
		"gui.reported":                        "",
		"notes.txt":                           "",
	} {
		if got := prefixOf(name); got != want {
			t.Errorf("prefixOf(%q) = %q, want %q", name, got, want)
		}
	}
	start := time.Date(2026, 10, 4, 9, 5, 7, 0, time.Local)
	if got := fileName("gui", start, 42); got != "gui-20261004-090507-42.txt" {
		t.Errorf("fileName = %q", got)
	}
}

// A file holding the header alone is told apart from a crash record.
func TestHeaderOnly(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	empty := writeRecord(t, dir, "gui", start, 1, false, start)
	crashed := writeRecord(t, dir, "gui", start, 2, true, start)
	if !headerOnly(empty) {
		t.Error("a header-only file reads as a crash")
	}
	if headerOnly(crashed) {
		t.Error("a crash record reads as header-only")
	}
	foreign := filepath.Join(dir, "gui-20261004-101500-3.txt")
	os.WriteFile(foreign, []byte("hello"), 0o644)
	if headerOnly(foreign) || headerOnly(filepath.Join(dir, "missing.txt")) {
		t.Error("a foreign or missing file reads as header-only")
	}
	if h := header("gui", "v9.9.9", start); !strings.Contains(h, "version: v9.9.9") ||
		!strings.Contains(h, fmt.Sprintf("pid: %d", os.Getpid())) || !strings.HasSuffix(h, headerEnd) {
		t.Errorf("header = %q", h)
	}
}

// Prune drops the header-only files and keeps the newest records of each
// prefix by when they were written, leaving foreign files alone.
func TestPruneKeepsTheNewestRecordsPerPrefix(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	// Five gui records. Start times run backwards against crash times, so
	// the order must come from the modification time, not the name.
	var gui []string
	for i := range 5 {
		gui = append(gui, filepath.Base(writeRecord(t, dir, "gui", base.Add(-time.Duration(i)*time.Hour), 100+i, true, base.Add(time.Duration(i)*time.Minute))))
	}
	svc := filepath.Base(writeRecord(t, dir, "service", base, 7, true, base))
	writeRecord(t, dir, "gui", base, 200, false, base)
	writeRecord(t, dir, "useraction", base, 201, false, base)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "gui.reported"), []byte(""), 0o644)

	Prune(dir, 3)
	// gui[4] crashed last, then gui[3] and gui[2].
	want := []string{gui[2], gui[3], gui[4], "gui.reported", "readme.txt", svc}
	slices.Sort(want)
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Errorf("after Prune:\n got %v\nwant %v", got, want)
	}
	Prune(filepath.Join(dir, "missing"), 3) // no folder: nothing to do
}

// Enable writes the header into a new file and arms the runtime; Disable
// removes the file while nothing was recorded, and keeps one that holds a
// crash.
func TestEnableDisable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "crash")
	other := filepath.Join(t.TempDir(), "crash")
	// Registered after the folders, so it runs before they are removed
	// (the runtime's handle would keep the file open).
	t.Cleanup(Disable)
	// Every Enable is a second later: two files of one pid in the same
	// second would share a name.
	clock := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	now = func() time.Time { clock = clock.Add(time.Second); return clock }
	t.Cleanup(func() { now = time.Now })
	// A killed process's leftover goes on the next Enable.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := writeRecord(t, dir, "gui", time.Now().Add(-time.Hour), 99999, false, time.Now().Add(-time.Hour))

	path, err := Enable(dir, "gui", Options{Version: "v1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if Active() != path || filepath.Dir(path) != dir || prefixOf(filepath.Base(path)) != "gui" {
		t.Fatalf("Enable = %q (active %q)", path, Active())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the header-only leftover survived Enable")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "version: v1.2.3") || !headerOnly(path) {
		t.Fatalf("crash file = %q, %v", data, err)
	}
	// Again with the same folder and prefix: the same file.
	if again, err := Enable(dir, "gui", Options{}); err != nil || again != path {
		t.Errorf("second Enable = %q, %v; want %q", again, err, path)
	}
	// Prune leaves the live file alone although it holds only the header.
	Prune(dir, Keep)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Prune removed the active file: %v", err)
	}

	Disable()
	if Active() != "" {
		t.Error("still active after Disable")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Disable left the header-only file behind")
	}
	Disable() // a second time is a no-op

	// A file the runtime wrote to stays.
	path, err = Enable(dir, "gui", Options{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("fatal error: unexpected signal\r\n")
	f.Close()
	Disable()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Disable removed a crash record: %v", err)
	}

	// Switching folders closes the first file.
	first, err := Enable(dir, "gui", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(other, "gui", Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Error("switching folders left the first file behind")
	}

	for _, bad := range [][2]string{{"", "gui"}, {dir, ""}, {dir, "a-b"}, {dir, `a\b`}} {
		if _, err := Enable(bad[0], bad[1], Options{}); err == nil {
			t.Errorf("Enable(%q, %q) accepted", bad[0], bad[1])
		}
	}
}

// The Create option makes the file (the service's private one).
func TestEnableUsesCreate(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(Disable)
	var made string
	_, err := Enable(dir, "service", Options{Create: func(p string) (*os.File, error) {
		made = p
		return os.Create(p)
	}})
	if err != nil || made == "" || made != Active() {
		t.Fatalf("Create saw %q, active %q, err %v", made, Active(), err)
	}
}

// Unreported announces each crash record once, newest first, and never a
// header-only file.
func TestUnreported(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	a := writeRecord(t, dir, "gui", base, 1, true, base)
	b := writeRecord(t, dir, "gui", base, 2, true, base.Add(time.Minute))
	writeRecord(t, dir, "gui", base, 3, false, base.Add(2*time.Minute))
	writeRecord(t, dir, "service", base, 4, true, base)

	if got := Unreported(dir, "gui"); !slices.Equal(got, []string{b, a}) {
		t.Errorf("first look = %v, want %v", got, []string{b, a})
	}
	if got := Unreported(dir, "gui"); got != nil {
		t.Errorf("second look = %v, want nothing", got)
	}
	c := writeRecord(t, dir, "gui", base, 5, true, base.Add(3*time.Minute))
	if got := Unreported(dir, "gui"); !slices.Equal(got, []string{c}) {
		t.Errorf("after a new crash = %v, want %v", got, []string{c})
	}
	if got := Unreported(dir, "service"); len(got) != 1 {
		t.Errorf("service = %v, want its own record", got)
	}
	if got := Unreported(filepath.Join(dir, "missing"), "gui"); got != nil {
		t.Errorf("no folder = %v", got)
	}
}
