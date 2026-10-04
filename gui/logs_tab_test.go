package gui

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Each service log line splits into coloured parts that give the line
// back when joined (#134).
func TestLogSegments(t *testing.T) {
	const (
		ph   = theme.ColorNamePlaceHolder
		fg   = theme.ColorNameForeground
		tag  = theme.ColorNamePrimary
		warn = theme.ColorNameWarning
		bad  = theme.ColorNameError
	)
	long := strings.Repeat("가나다 abc ", 600)
	cases := []struct {
		name string
		line string
		want []logPart
	}{
		{"tag", `2026/10/04 13:53:00 [debug] debug mode on: crashes are recorded in C:\PC Control\crash (x.txt)`,
			[]logPart{{"2026/10/04 ", ph}, {"13:53:00", fg}, {" [debug]", tag}, {` debug mode on: crashes are recorded in C:\PC Control\crash (x.txt)`, fg}}},
		{"warning", "2026/10/04 13:52:40 WARNING: No secret configured. Anyone on your network can control this PC.",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:40", fg}, {" WARNING:", warn}, {" No secret configured. Anyone on your network can control this PC.", fg}}},
		{"warning without colon", "2026/10/04 13:52:40 WARNING something odd",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:40", fg}, {" WARNING", warn}, {" something odd", fg}}},
		{"warning then tag", "2026/10/04 13:52:40 WARNING: [debug] debug mode is on, but crashes cannot be recorded",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:40", fg}, {" WARNING:", warn}, {" [debug]", tag}, {" debug mode is on, but crashes cannot be recorded", fg}}},
		{"warning that failed", `2026/10/04 13:52:40 WARNING: C:\x not written: open failed`,
			[]logPart{{"2026/10/04 ", ph}, {"13:52:40", fg}, {" WARNING:", warn}, {` C:\x not written: open failed`, bad}}},
		{"plain", "2026/10/04 13:52:38 SSDP: discovery responder stopped",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" SSDP: discovery responder stopped", fg}}},
		{"failed", "2026/10/04 13:52:38 SSDP: reply to 10.0.0.2 failed: i/o timeout",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" SSDP: reply to 10.0.0.2 failed: i/o timeout", bad}}},
		{"error:", "2026/10/04 13:52:38 HTTP server error: listen tcp :5001: bind",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" HTTP server error: listen tcp :5001: bind", bad}}},
		{"ERROR", "2026/10/04 13:52:38 ERROR losing the plot",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" ERROR losing the plot", bad}}},
		{"http panic", "2026/10/04 13:52:38 http: panic serving 127.0.0.1:5555: runtime error: index out of range",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" http: panic serving 127.0.0.1:5555: runtime error: index out of range", bad}}},
		{"tag and error", "2026/10/04 13:52:38 [debug] previous crash recorded, writing failed",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" [debug]", tag}, {" previous crash recorded, writing failed", bad}}},
		{"error inside words", "2026/10/04 13:52:38 Stats: errors=0 error_count=0 failover ok, 3 failures",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" Stats: errors=0 error_count=0 failover ok, 3 failures", fg}}},
		{"warning mid-line", "2026/10/04 13:52:38 Config: WARNING: not at the start",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" Config: WARNING: not at the start", fg}}},
		{"WARNINGS is not the token", "2026/10/04 13:52:38 WARNINGS: 2",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" WARNINGS: 2", fg}}},
		{"bracket with spaces is no tag", "2026/10/04 13:52:38 [not a tag] x",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" [not a tag] x", fg}}},
		{"microseconds", "2026/10/04 13:52:38.123456 Listening on port 5001",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38.123456", fg}, {" Listening on port 5001", fg}}},
		{"korean", "2026/10/04 13:52:38 SSDP 검색은 항상 켜져 있습니다",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" SSDP 검색은 항상 켜져 있습니다", fg}}},
		{"korean warning", "2026/10/04 13:52:38 WARNING: config.json 파싱 실패 (기본값 사용)",
			[]logPart{{"2026/10/04 ", ph}, {"13:52:38", fg}, {" WARNING:", warn}, {" config.json 파싱 실패 (기본값 사용)", fg}}},
		{"only timestamp", "2026/10/04 13:53:00",
			[]logPart{{"2026/10/04 ", ph}, {"13:53:00", fg}}},
		{"timestamp and a space", "2026/10/04 13:53:00 ",
			[]logPart{{"2026/10/04 ", ph}, {"13:53:00", fg}, {" ", fg}}},
		{"only a tag", "2026/10/04 13:53:00 [debug]",
			[]logPart{{"2026/10/04 ", ph}, {"13:53:00", fg}, {" [debug]", tag}}},
		{"blank", "", []logPart{{"", fg}}},
		{"no timestamp", "goroutine 12 [running]:", []logPart{{"goroutine 12 [running]:", fg}}},
		{"stack frame", "\tC:/dev/pc/service/http.go:123 +0x45", []logPart{{"\tC:/dev/pc/service/http.go:123 +0x45", fg}}},
		{"stack panic", "panic: runtime error: invalid memory address", []logPart{{"panic: runtime error: invalid memory address", bad}}},
		{"tag without timestamp", "[debug] no timestamp here", []logPart{{"[debug] no timestamp here", fg}}},
		{"warning without timestamp", "WARNING: no timestamp here", []logPart{{"WARNING: no timestamp here", fg}}},
		{"short time", "2026/10/04 13:53 not quite", []logPart{{"2026/10/04 13:53 not quite", fg}}},
		{"glued time", "2026/10/04 13:53:00x", []logPart{{"2026/10/04 13:53:00x", fg}}},
		{"very long", "2026/10/04 13:53:00 " + long,
			[]logPart{{"2026/10/04 ", ph}, {"13:53:00", fg}, {" " + long, fg}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logSegments(c.line)
			if !slices.Equal(got, c.want) {
				t.Errorf("logSegments(%.60q)\n got %v\nwant %v", c.line, got, c.want)
			}
			var joined strings.Builder
			for _, p := range got {
				joined.WriteString(p.text)
			}
			if joined.String() != c.line {
				t.Errorf("parts join to %.60q, want the line back", joined.String())
			}
		})
	}
}

// The two columns of a row: the timestamp as MM/DD HH:MM:SS, or nothing
// for a line without one, and the message without the space after the
// timestamp, coloured as logSegments colours it.
func TestLogCells(t *testing.T) {
	const (
		ph  = theme.ColorNamePlaceHolder
		fg  = theme.ColorNameForeground
		tag = theme.ColorNamePrimary
		bad = theme.ColorNameError
	)
	cases := []struct {
		name  string
		line  string
		stamp string // the left cell's text
		msg   []logPart
	}{
		{"tag", "2026/10/04 13:53:00 [debug] debug mode on",
			"10/04 13:53:00", []logPart{{"[debug]", tag}, {" debug mode on", fg}}},
		{"error", "2026/10/04 13:52:38 SSDP: reply failed: i/o timeout",
			"10/04 13:52:38", []logPart{{"SSDP: reply failed: i/o timeout", bad}}},
		{"microseconds", "2026/10/04 13:52:38.123456 Listening on port 5001",
			"10/04 13:52:38", []logPart{{"Listening on port 5001", fg}}},
		{"two spaces", "2026/10/04 13:52:38  indented",
			"10/04 13:52:38", []logPart{{" indented", fg}}},
		{"only timestamp", "2026/10/04 13:53:00", "10/04 13:53:00", nil},
		{"timestamp and a space", "2026/10/04 13:53:00 ", "10/04 13:53:00", nil},
		{"no timestamp", "goroutine 12 [running]:", "", []logPart{{"goroutine 12 [running]:", fg}}},
		{"stack frame", "\tC:/dev/pc/service/http.go:123 +0x45", "", []logPart{{"\tC:/dev/pc/service/http.go:123 +0x45", fg}}},
		{"stack panic", "panic: runtime error: invalid memory address", "", []logPart{{"panic: runtime error: invalid memory address", bad}}},
		{"blank", "", "", []logPart{{"", fg}}},
		{"short time", "2026/10/04 13:53 not quite", "", []logPart{{"2026/10/04 13:53 not quite", fg}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stamp, msg := logCells(c.line)
			var text strings.Builder
			for _, p := range stamp {
				text.WriteString(p.text)
			}
			if text.String() != c.stamp {
				t.Errorf("stamp cell %q, want %q", text.String(), c.stamp)
			}
			if c.stamp != "" && (len(stamp) != 2 || stamp[0].color != ph || stamp[1].color != fg) {
				t.Errorf("stamp parts %v, want the dim date and the time", stamp)
			}
			if !slices.Equal(msg, c.msg) {
				t.Errorf("message cell\n got %v\nwant %v", msg, c.msg)
			}
		})
	}
}

// A cell is one paragraph: every part inline but the last, all of them
// monospace. An empty cell still has a line, so the row keeps its height.
func TestLogRichSegmentsOneParagraph(t *testing.T) {
	stamp, msg := logCells("2026/10/04 13:53:00 [debug] a")
	for _, parts := range [][]logPart{stamp, msg, nil} {
		segs := logRichSegments(parts)
		if want := max(len(parts), 1); len(segs) != want {
			t.Fatalf("%d segments for %v, want %d", len(segs), parts, want)
		}
		for i, s := range segs {
			ts := s.(*widget.TextSegment)
			if !ts.Style.TextStyle.Monospace {
				t.Errorf("segment %q is not monospace", ts.Text)
			}
			if last := i == len(segs)-1; ts.Inline() == last {
				t.Errorf("segment %d of %v: inline %v, want only the last to end the paragraph", i, parts, ts.Inline())
			}
		}
	}
}

// logsShift finds how far the old lines moved up.
func TestLogsShift(t *testing.T) {
	l := logLinesN(6)
	cases := []struct {
		name       string
		old, lines []string
		want       int
	}{
		{"same", l[:4], l[:4], 0},
		{"appended", l[:4], l, 0},
		{"slid by one", l[:5], l[1:6], 1},
		{"slid by two", l[:4], l[2:6], 2},
		{"unrelated", l[:3], []string{"x", "y"}, 0},
		{"from nothing", nil, l, 0},
		{"to a subset", l, l[3:4], 0},
	}
	for _, c := range cases {
		if got := logsShift(c.old, c.lines); got != c.want {
			t.Errorf("%s: logsShift = %d, want %d", c.name, got, c.want)
		}
	}
}

// logsAPI serves log lines to the window; the rest of the service is a
// client that reaches nothing.
type logsAPI struct {
	serviceAPI
	mu    sync.Mutex
	lines []string
	calls int
}

func (f *logsAPI) Logs() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return slices.Clone(f.lines), nil
}

func (f *logsAPI) set(lines []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = lines
}

func (f *logsAPI) fetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// logLinesN is n service log lines, far more than the view's height.
func logLinesN(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026/10/04 13:%02d:%02d SSDP: answered 10.0.0.%d (ST upnp:rootdevice)", i/60, i%60, i)
	}
	return lines
}

// newLogsUI is the window on a service with lines in its log, connected,
// sized, and on the logs tab (which loads them).
func newLogsUI(t *testing.T, lines []string) (*ui, *logsAPI) {
	t.Helper()
	svc := &logsAPI{serviceAPI: NewClient(1), lines: lines}
	u := newTestUI(t, LangEn, svc)
	u.win.Resize(fyne.NewSize(640, 400))
	u.connected.Store(true)
	u.applyConnected(true)
	u.tabs.SelectIndex(tabLogs)
	if u.curTab != tabLogs {
		t.Fatal("the logs tab did not come to the front")
	}
	return u, svc
}

// logsBottom is the offset that shows the newest line.
func logsBottom(u *ui) float32 {
	return fyne.Max(0, u.logsScroll.Content.MinSize().Height-u.logsScroll.Size().Height)
}

// assertLogsAtBottom fails unless the view sits on the newest line.
func assertLogsAtBottom(t *testing.T, u *ui, when string) {
	t.Helper()
	if got, want := u.logsScroll.Offset.Y, logsBottom(u); got != want || want <= 0 {
		t.Errorf("%s: offset %v, want the bottom %v (> 0)", when, got, want)
	}
}

// userScroll is a mouse wheel turn over the view; dy > 0 scrolls up.
func userScroll(u *ui, dy float32) {
	u.logsScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: dy}})
}

// Opening the tab loads the lines, coloured, and shows the newest.
func TestLogsTabShowsTheTail(t *testing.T) {
	u, _ := newLogsUI(t, logLinesN(60))
	if len(u.logsShown) != 60 || u.logsShownMsg != "" {
		t.Fatalf("view shows %d lines (message %q), want 60", len(u.logsShown), u.logsShownMsg)
	}
	if len(u.logsRowsLayout.rows) != 60 || len(u.logsRows.Objects) != 3*60-1 || !u.logsRows.Visible() || u.logsMsg.Visible() {
		t.Fatalf("%d rows of %d objects, rows shown %v, message shown %v; want 60 rows",
			len(u.logsRowsLayout.rows), len(u.logsRows.Objects), u.logsRows.Visible(), u.logsMsg.Visible())
	}
	first := u.logsRowsLayout.rows[0].stamp.Segments[0].(*widget.TextSegment)
	if first.Text != "10/04 " || first.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Errorf("first segment %q in %q, want the dim date", first.Text, first.Style.ColorName)
	}
	assertLogsAtBottom(t, u, "after opening the tab")
	if !u.logsAuto.Checked || !u.logsAutoOn.Load() {
		t.Error("following the tail turned auto refresh off")
	}
}

// Lines that arrived while another tab was in front show at the bottom
// when the logs tab comes back, even when they were rendered before the
// view was ever laid out (a stale content size).
func TestLogsTabFirstShowScrollsOnceLaidOut(t *testing.T) {
	svc := &logsAPI{serviceAPI: NewClient(1), lines: logLinesN(80)}
	u := newTestUI(t, LangEn, svc)
	u.win.Resize(fyne.NewSize(640, 400))
	u.connected.Store(true)
	u.applyConnected(true)
	u.loadLogs() // rendered on a hidden, never laid out tab
	if len(u.logsShown) != 80 {
		t.Fatalf("view shows %d lines, want 80", len(u.logsShown))
	}
	u.tabs.SelectIndex(tabLogs)
	assertLogsAtBottom(t, u, "first show")

	// Away and back with more lines meanwhile.
	u.tabs.SelectIndex(tabCommands)
	svc.set(logLinesN(100))
	u.tabs.SelectIndex(tabLogs)
	if len(u.logsShown) != 100 {
		t.Fatalf("view shows %d lines after coming back, want 100", len(u.logsShown))
	}
	assertLogsAtBottom(t, u, "shown again")
}

// msgSegments is the first message segment of each row: a row that was
// not redrawn keeps it.
func msgSegments(rows []*logRow) []widget.RichTextSegment {
	segs := make([]widget.RichTextSegment, len(rows))
	for i, r := range rows {
		segs[i] = r.msg.Segments[0]
	}
	return segs
}

// A poll that brings the same lines leaves the view alone. New lines keep
// the rows: the ones whose line stays are not redrawn, also when the lines
// slide up (the oldest dropping off the top of the service's 100), and
// the view follows the new lines.
func TestLogsUnchangedPollDoesNotRebuild(t *testing.T) {
	u, svc := newLogsUI(t, logLinesN(60))
	rows := slices.Clone(u.logsRowsLayout.rows)
	segs := msgSegments(rows)
	u.loadLogs()
	if !slices.Equal(u.logsRowsLayout.rows, rows) || !slices.Equal(msgSegments(rows), segs) {
		t.Error("identical lines rebuilt the view")
	}

	// One line more at the bottom: a row more, the others untouched.
	svc.set(logLinesN(61))
	u.loadLogs()
	got := u.logsRowsLayout.rows
	if len(got) != 61 || len(u.logsShown) != 61 || got[60].line != logLinesN(61)[60] {
		t.Fatalf("view shows %d rows, the last %q; want the new line in row 61", len(got), got[len(got)-1].line)
	}
	if !slices.Equal(got[:60], rows) || !slices.Equal(msgSegments(got[:60]), segs) {
		t.Error("a new line at the bottom redrew the rows above it")
	}
	assertLogsAtBottom(t, u, "after new lines")

	// The oldest line drops off the top as a new one comes in: the rows
	// move up with their lines, and the top row comes back at the bottom
	// with the new one.
	added := got[60]
	svc.set(logLinesN(62)[1:])
	u.loadLogs()
	got = u.logsRowsLayout.rows
	if len(got) != 61 || got[60] != rows[0] || got[60].line != logLinesN(62)[61] {
		t.Fatalf("the slid view ends in %q, want the new line in the row that left the top", got[len(got)-1].line)
	}
	if !slices.Equal(got[:59], rows[1:]) || got[59] != added || !slices.Equal(msgSegments(got[:59]), segs[1:]) {
		t.Error("sliding the lines up redrew rows that kept their line")
	}
	for i, r := range got {
		if r.line != u.logsShown[i] {
			t.Fatalf("row %d shows %q, want %q", i, r.line, u.logsShown[i])
		}
	}
	assertLogsAtBottom(t, u, "after the lines slid up")
	if !u.logsAuto.Checked {
		t.Error("our own scroll to the new lines turned auto refresh off")
	}
}

// The filter still narrows the lines, keeps the view at the bottom and
// says so, dimmed, when nothing matches.
func TestLogsFilter(t *testing.T) {
	u, _ := newLogsUI(t, logLinesN(60))
	u.logsFilter.SetText("10.0.0.7 ")
	if len(u.logsShown) != 1 || !strings.Contains(u.logsShown[0], "10.0.0.7 ") {
		t.Errorf("filter shows %q", u.logsShown)
	}
	// The filter sees the year the view leaves out.
	u.logsFilter.SetText("2026/10/04 13:00:1")
	if len(u.logsShown) != 10 { // 13:00:10–19
		t.Errorf("filter on the full timestamp shows %d lines, want 10", len(u.logsShown))
	}
	u.logsFilter.SetText("10.0.0.1")
	if len(u.logsShown) != 11 || len(u.logsRowsLayout.rows) != 11 { // 1, 10–19
		t.Errorf("filter shows %d lines in %d rows, want 11", len(u.logsShown), len(u.logsRowsLayout.rows))
	}
	u.logsFilter.SetText("no such line")
	if u.logsShownMsg != "logs.nomatch" || len(u.logsMsg.Segments) != 1 {
		t.Fatalf("no match shows %q", u.logsShownMsg)
	}
	if !u.logsMsg.Visible() || u.logsRows.Visible() {
		t.Error("the no-match message does not stand in for the rows")
	}
	msg := u.logsMsg.Segments[0].(*widget.TextSegment)
	if msg.Text != u.t("logs.nomatch") || msg.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Errorf("no-match message %q in %q", msg.Text, msg.Style.ColorName)
	}
	u.logsFilter.SetText("")
	if len(u.logsShown) != 60 || !u.logsRows.Visible() || u.logsMsg.Visible() {
		t.Errorf("clearing the filter shows %d lines (rows shown %v)", len(u.logsShown), u.logsRows.Visible())
	}
	for i, r := range u.logsRowsLayout.rows {
		if r.line != u.logsShown[i] {
			t.Fatalf("row %d shows %q, want %q", i, r.line, u.logsShown[i])
		}
	}
	assertLogsAtBottom(t, u, "after clearing the filter")
	if !u.logsAuto.Checked {
		t.Error("filtering turned auto refresh off")
	}
}

// An empty log shows the dimmed empty message.
func TestLogsEmpty(t *testing.T) {
	u, _ := newLogsUI(t, nil)
	if u.logsShownMsg != "logs.empty" {
		t.Fatalf("empty log shows %q", u.logsShownMsg)
	}
	if !u.logsMsg.Visible() || u.logsRows.Visible() {
		t.Error("the empty message does not stand in for the rows")
	}
	msg := u.logsMsg.Segments[0].(*widget.TextSegment)
	if msg.Text != u.t("logs.empty") || msg.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Errorf("empty message %q in %q", msg.Text, msg.Style.ColorName)
	}
}

// Two columns: every message starts at the same x, past the timestamp
// column, and a long line wraps inside the message column, the next row
// starting under it. A line without a timestamp leaves the left cell
// empty. Rows are a line spacing apart, the separator between them.
func TestLogsColumns(t *testing.T) {
	long := "2026/10/04 13:53:00 WARNING: " + strings.Repeat("a long message that wraps ", 20)
	lines := []string{"2026/10/04 13:52:59 short", long, "goroutine 1 [running]:", "", "2026/10/04 13:53:01 after"}
	u, _ := newLogsUI(t, lines)
	m := newLogRowsMetrics()
	rows := u.logsRowsLayout.rows
	if len(rows) != len(lines) {
		t.Fatalf("%d rows, want %d", len(rows), len(lines))
	}
	for i, want := range []string{"10/04 13:52:59", "10/04 13:53:00", "", "", "10/04 13:53:01"} {
		if got := rows[i].stamp.String(); got != want {
			t.Errorf("row %d's left cell %q, want %q", i, got, want)
		}
	}
	if got := rows[1].msg.String(); got != strings.TrimPrefix(long, "2026/10/04 13:53:00 ") {
		t.Errorf("long row's message %.40q…, want the line after the timestamp", got)
	}
	msgW := u.logsRows.Size().Width - m.stampX
	if stampW := fyne.MeasureText(logStampSample, theme.Size(theme.SizeNameText), fyne.TextStyle{Monospace: true}).Width; m.stampX != m.pad+stampW || msgW <= 0 {
		t.Fatalf("message column at %v (%v wide), want it past the stamp's %v", m.stampX, msgW, stampW)
	}
	lineH := rows[0].msg.MinSize().Height - 2*m.pad
	for i, r := range rows {
		if r.msg.Position().X != m.stampX || r.msg.Size().Width != msgW {
			t.Errorf("row %d's message at x %v, %v wide; want %v, %v", i, r.msg.Position().X, r.msg.Size().Width, m.stampX, msgW)
		}
		if r.stamp.Position() != fyne.NewPos(0, r.msg.Position().Y) {
			t.Errorf("row %d's stamp at %v, want at the left edge level with the message", i, r.stamp.Position())
		}
		if i == 0 {
			continue
		}
		prev := rows[i-1]
		prevEnd := prev.msg.Position().Y + prev.msg.Size().Height - m.pad
		top := r.msg.Position().Y + m.pad
		if top-prevEnd != m.gap {
			t.Errorf("row %d starts %v below the text above, want the line spacing %v", i, top-prevEnd, m.gap)
		}
		if y := r.sep.Position().Y; y < prevEnd || y+r.sep.Size().Height > top || r.sep.Size().Width != u.logsRows.Size().Width {
			t.Errorf("row %d's separator at %v (%v), want across the gap %v–%v", i, y, r.sep.Size(), prevEnd, top)
		}
	}
	if h := rows[1].msg.Size().Height - 2*m.pad; h < 3*lineH {
		t.Errorf("the long line is %v tall, want it wrapped over several lines of %v", h, lineH)
	}
	for _, i := range []int{0, 2, 3, 4} {
		if h := rows[i].msg.Size().Height - 2*m.pad; h != lineH {
			t.Errorf("row %d is %v tall, want one line, %v", i, h, lineH)
		}
	}
	if slices.Contains(u.logsRows.Objects, fyne.CanvasObject(rows[0].sep)) {
		t.Error("a separator above the first row")
	}
}

// The rows take no more room than the paragraphs of the single RichText
// the view used to be: a row is as tall as its text, and rows are the
// theme's line spacing apart, as paragraphs were.
func TestLogsRowsAsCompactAsBefore(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026/10/04 13:00:%02d line %d", i, i)
	}
	u, _ := newLogsUI(t, lines)
	var segs []widget.RichTextSegment
	for _, line := range lines {
		segs = append(segs, logRichSegments(logSegments(line))...)
	}
	before := widget.NewRichText(segs...)
	before.Wrapping = fyne.TextWrapBreak
	before.Resize(fyne.NewSize(u.logsRows.Size().Width, 10))
	if got, want := u.logsRows.MinSize().Height, before.MinSize().Height; got != want {
		t.Errorf("30 rows are %v tall, want %v as one RichText", got, want)
	}
}

// Lines long enough to wrap still open at the bottom, also when they were
// rendered before the tab was ever laid out, and the view stays on the
// newest line when the window gets wider or narrower (which re-wraps every
// row) and new lines come in.
func TestLogsWrappedLinesFollowTheTail(t *testing.T) {
	long := func(n int) []string {
		lines := logLinesN(n)
		for i := range lines {
			lines[i] += strings.Repeat(" and some more", 5+i%20)
		}
		return lines
	}
	svc := &logsAPI{serviceAPI: NewClient(1), lines: long(80)}
	u := newTestUI(t, LangEn, svc)
	u.win.Resize(fyne.NewSize(640, 400))
	u.connected.Store(true)
	u.applyConnected(true)
	u.loadLogs() // rendered on a hidden, never laid out tab
	u.tabs.SelectIndex(tabLogs)
	assertLogsAtBottom(t, u, "first show")
	m := newLogRowsMetrics()
	last := u.logsRowsLayout.rows[79]
	if h := last.msg.Size().Height - 2*m.pad; h < 2*(u.logsRowsLayout.rows[0].msg.MinSize().Height-2*m.pad) {
		t.Fatalf("the last row is %v tall: the test lines do not wrap", h)
	}
	for i, size := range []fyne.Size{fyne.NewSize(900, 400), fyne.NewSize(560, 450)} {
		u.win.Resize(size)
		svc.set(long(81 + i))
		u.loadLogs()
		assertLogsAtBottom(t, u, fmt.Sprintf("new lines after a resize to %v", size))
		if want := u.logsRows.Size().Width - m.stampX; last.msg.Size().Width != want {
			t.Errorf("a row is %v wide at window %v, want the message column's %v", last.msg.Size().Width, size, want)
		}
	}
	// A few lines that fit the view unwrapped and overflow it wrapped.
	few := long(6)
	for i := range few {
		few[i] += strings.Repeat(" wrapping on", 40)
	}
	svc.set(few)
	u.loadLogs()
	assertLogsAtBottom(t, u, "a few long lines")
	if !u.logsAuto.Checked {
		t.Error("following the wrapped lines turned auto refresh off")
	}
}

// Our scrolls and the clamping of a resize leave auto refresh on.
func TestLogsProgrammaticScrollKeepsAutoRefresh(t *testing.T) {
	u, _ := newLogsUI(t, logLinesN(60))
	u.logsToBottom()
	u.logsScroll.ScrollToBottom()
	// A taller window clamps the offset (Scroll.refreshBars), outside
	// our guard: it lands on the bottom, so it is not the user.
	before := u.logsScroll.Offset.Y
	u.win.Resize(fyne.NewSize(640, 600))
	if u.logsScroll.Offset.Y >= before {
		t.Fatalf("the taller window did not clamp the offset (%v → %v)", before, u.logsScroll.Offset.Y)
	}
	// A shorter one leaves the offset where it was.
	u.win.Resize(fyne.NewSize(640, 350))
	if !u.logsAuto.Checked || !u.logsAutoOn.Load() {
		t.Fatal("a programmatic scroll or a resize turned auto refresh off")
	}
	// The next refresh puts the shorter view back on the bottom.
	u.loadLogs()
	assertLogsAtBottom(t, u, "after a resize and a refresh")
}

// Scrolling up turns auto refresh off, through the check: the poll stops
// and the view stays where the user put it.
func TestLogsUserScrollUpTurnsAutoRefreshOff(t *testing.T) {
	u, svc := newLogsUI(t, logLinesN(60))
	u.visible.Store(true)
	if !u.logsPollWanted() {
		t.Fatal("logs tab in front with auto refresh on: want polling")
	}
	userScroll(u, 40)
	if u.logsAuto.Checked || u.logsAutoOn.Load() {
		t.Fatal("scrolling up left auto refresh on")
	}
	if u.logsPollWanted() {
		t.Error("polling still wanted after scrolling up")
	}
	at := u.logsScroll.Offset.Y
	svc.set(logLinesN(61))
	u.loadLogs() // the Refresh button
	if u.logsScroll.Offset.Y != at {
		t.Errorf("a refresh moved the view from %v to %v while the user reads", at, u.logsScroll.Offset.Y)
	}
}

// Scrolling down, or up and back down to the bottom, keeps it on.
func TestLogsUserScrollDownKeepsAutoRefresh(t *testing.T) {
	u, _ := newLogsUI(t, logLinesN(60))
	userScroll(u, -40) // already at the bottom: nothing moves
	// A shorter window leaves the view above the bottom; scrolling down
	// from there is not a scroll up.
	u.win.Resize(fyne.NewSize(640, 300))
	if u.logsScroll.Offset.Y >= logsBottom(u)-10 {
		t.Fatalf("the shorter window left the view at %v, want well above the bottom %v", u.logsScroll.Offset.Y, logsBottom(u))
	}
	userScroll(u, -10)
	if !u.logsAuto.Checked {
		t.Fatal("scrolling down turned auto refresh off")
	}
	userScroll(u, -1000)
	if !u.logsAuto.Checked {
		t.Fatal("scrolling down to the bottom turned auto refresh off")
	}
	assertLogsAtBottom(t, u, "scrolled to the end")
}

// Checking auto refresh again jumps to the newest line and fetches at once.
func TestLogsRecheckJumpsToBottom(t *testing.T) {
	u, svc := newLogsUI(t, logLinesN(60))
	userScroll(u, 200)
	if u.logsAuto.Checked {
		t.Fatal("scrolling up left auto refresh on")
	}
	svc.set(logLinesN(70))
	fetches := svc.fetches()
	u.logsAuto.SetChecked(true)
	if !u.logsAutoOn.Load() {
		t.Error("checking auto refresh left the poll flag off")
	}
	if svc.fetches() != fetches+1 || len(u.logsShown) != 70 {
		t.Errorf("checking fetched %d times and shows %d lines, want 1 and 70", svc.fetches()-fetches, len(u.logsShown))
	}
	assertLogsAtBottom(t, u, "after checking auto refresh")
	if !u.logsAuto.Checked {
		t.Error("the jump to the bottom turned auto refresh off again")
	}
}
