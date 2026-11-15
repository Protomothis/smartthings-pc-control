package ui

import (
	"slices"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

func TestParseLogRowsLevels(t *testing.T) {
	lines := []string{
		"2026/11/15 09:00:00 Listening on port 5001",
		"2026/11/15 09:00:01 [debug] SSDP answered",
		"2026/11/15 09:00:02 WARNING: no secret",
		"2026/11/15 09:00:03 http: panic serving 127.0.0.1: runtime error",
		"goroutine 41 [running]:",
		"\tC:/dev/pc/routes.go:123 +0x45",
		"2026/11/15 09:00:04 back to normal",
	}
	rows, counts := parseLogRows(lines)
	if counts != [4]int{7, 1, 1, 1} {
		t.Fatalf("counts = %v", counts)
	}
	// the stack takes the panic's level; the next stamped line does not
	for i, want := range []gui.LogLevel{0, gui.LogLevelDebug, gui.LogLevelWarn, gui.LogLevelError, gui.LogLevelError, gui.LogLevelError, 0} {
		if rows[i].lv != want {
			t.Errorf("row %d level %b, want %b", i, rows[i].lv, want)
		}
	}
	if got := rows[5].msg[0].Text; got != "    C:/dev/pc/routes.go:123 +0x45" {
		t.Errorf("tab not expanded: %q", got)
	}
	if got := filterLogRows(rows, "", gui.LogLevelError); !slices.Equal(got, []int{3, 4, 5}) {
		t.Errorf("errors: %v", got)
	}
	if got := filterLogRows(rows, "ssdp", 0); !slices.Equal(got, []int{1}) {
		t.Errorf("text (case-insensitive): %v", got)
	}
	if got := filterLogRows(rows, "ssdp", gui.LogLevelWarn); len(got) != 0 {
		t.Errorf("both must match: %v", got)
	}
}

func TestLogsSyncFollowsAndKeepsPlace(t *testing.T) {
	p := newLogsPage().(*logsPage)
	a := []string{"a", "b", "c", "d"}
	p.sync(a, false, "", levelAll)
	if !p.jump || len(p.view) != 4 {
		t.Fatalf("first read: jump %v view %v", p.jump, p.view)
	}
	// unchanged lines (the same slice): nothing to do
	p.jump = false
	p.sync(a, false, "", levelAll)
	if p.jump {
		t.Fatal("an unchanged read moved the view")
	}
	// following: a new read goes to the newest line
	b := []string{"b", "c", "d", "e"}
	p.sync(b, false, "", levelAll)
	if !p.jump {
		t.Fatal("following did not follow")
	}
	// scrolled up: the view moves up with the lines that dropped off
	p.jump, p.away = false, true
	p.list.Position.First = 2
	c := []string{"d", "e", "f", "g"}
	p.sync(c, false, "", levelAll)
	if p.jump || p.list.Position.First != 0 {
		t.Fatalf("away: jump %v first %d", p.jump, p.list.Position.First)
	}
	// a new filter starts at the newest match
	p.sync(c, false, "e", levelAll)
	if !p.jump || !slices.Equal(p.view, []int{1}) {
		t.Fatalf("filter: jump %v view %v", p.jump, p.view)
	}
}

func TestLogsPausedStopsPolling(t *testing.T) {
	p := newLogsPage().(*logsPage)
	if !p.wantsLogs() {
		t.Fatal("live page does not want the log")
	}
	p.paused = true
	if p.wantsLogs() {
		t.Fatal("paused page still polls")
	}
	u := sampleUI(gui.LangKo, false)
	p.shown(u, snapshot{conn: connLost})
	if p.paused || !p.jump {
		t.Fatal("opening the page should start live at the newest line")
	}
}

// Without the service the page reads service.log itself (#140) and shows
// the note; with it, the service's lines.
func TestLogsFromDiskWhenServiceGone(t *testing.T) {
	saved := logsFromDisk
	defer func() { logsFromDisk = saved }()
	disk := []string{"2026/11/15 09:00:00 from disk"}
	logsFromDisk = func(gui.Lang) ([]string, string, bool) { return disk, "note", true }

	u := sampleUI(gui.LangKo, false)
	u.api = logsAPI{lines: []string{"x"}}
	u.update(func(s *snapshot) { s.conn = connLost })
	p := u.pages[pageLogs].(*logsPage)
	u.open(pageLogs)
	frames(u, winW, winH, 1) // starts the read
	deadline := time.Now().Add(2 * time.Second)
	for !p.fileSet && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		u.runTasks()
	}
	if !p.fileSet || p.fileNote != "note" || !p.fileOK {
		t.Fatalf("file not read: set %v note %q", p.fileSet, p.fileNote)
	}
	frames(u, winW, winH, 1)
	if !p.srcFile || !slices.Equal(p.src, disk) {
		t.Fatalf("view shows %v (file %v)", p.src, p.srcFile)
	}

	lines := []string{"2026/11/15 09:00:00 from the service"}
	u.update(func(s *snapshot) { s.conn, s.logs, s.logsSet, s.logsErr = connOK, lines, true, nil })
	frames(u, winW, winH, 1)
	if p.srcFile || !slices.Equal(p.src, lines) {
		t.Fatalf("connected view shows %v (file %v)", p.src, p.srcFile)
	}
	if !fromFile(snapshot{conn: connOK, logsErr: errFake}) {
		t.Fatal("a failed /api/logs should fall back to the file")
	}
}

// The body fills the window under the top bar, so the log card scrolls
// and the page does not.
func TestLogsFillsWindow(t *testing.T) {
	u := sampleUI(gui.LangKo, false)
	lines := sampleLogLines(500)
	u.api = logsAPI{lines: lines}
	u.update(func(s *snapshot) { s.logs, s.logsSet = lines, true })
	p := u.pages[pageLogs].(*logsPage)
	u.open(pageLogs)
	frames(u, winW, winH, 3)
	first := p.list.Position.Count
	if pos := u.pageList.Position; pos.OffsetLast != 0 || pos.Offset != 0 {
		t.Fatalf("page overflows its window: %+v", pos)
	}
	if p.away {
		t.Fatal("not at the newest line")
	}
	frames(u, winW, winH+200, 3)
	if pos := u.pageList.Position; pos.OffsetLast != 0 || pos.Offset != 0 {
		t.Fatalf("taller window: page overflows: %+v", pos)
	}
	if n := p.list.Position.Count; n <= first {
		t.Fatalf("taller window shows %d rows (was %d)", n, first)
	}
	// Shorter than the least body height: the page scrolls instead.
	frames(u, winW, 300, 3)
	if pos := u.pageList.Position; pos.OffsetLast >= 0 {
		t.Fatalf("short window: page does not scroll: %+v", pos)
	}
}
