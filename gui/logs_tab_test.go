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

// One paragraph per line: every part but a line's last is inline, all of
// them monospace.
func TestLogRichSegmentsOneParagraphPerLine(t *testing.T) {
	segs := logRichSegments([]string{"2026/10/04 13:53:00 [debug] a", "", "plain"})
	var breaks int
	for _, s := range segs {
		ts := s.(*widget.TextSegment)
		if !ts.Style.TextStyle.Monospace {
			t.Errorf("segment %q is not monospace", ts.Text)
		}
		if !ts.Inline() {
			breaks++
		}
	}
	if len(segs) != 6 || breaks != 3 {
		t.Errorf("%d segments with %d line ends, want 6 and 3", len(segs), breaks)
	}
	for _, i := range []int{3, 4, 5} {
		if segs[i].Inline() {
			t.Errorf("segment %d (%q) does not end its line", i, segs[i].Textual())
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
	first := u.logsText.Segments[0].(*widget.TextSegment)
	if first.Text != "2026/10/04 " || first.Style.ColorName != theme.ColorNamePlaceHolder {
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

// A poll that brings the same lines leaves the view alone; new lines
// rebuild it and it follows them.
func TestLogsUnchangedPollDoesNotRebuild(t *testing.T) {
	u, svc := newLogsUI(t, logLinesN(60))
	before := u.logsText.Segments[0]
	u.loadLogs()
	if u.logsText.Segments[0] != before {
		t.Error("identical lines rebuilt the view")
	}
	svc.set(logLinesN(61))
	u.loadLogs()
	if u.logsText.Segments[0] == before || len(u.logsShown) != 61 {
		t.Error("new lines did not reach the view")
	}
	assertLogsAtBottom(t, u, "after new lines")
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
	u.logsFilter.SetText("10.0.0.1")
	if len(u.logsShown) != 11 { // 1, 10–19
		t.Errorf("filter shows %d lines, want 11", len(u.logsShown))
	}
	u.logsFilter.SetText("no such line")
	if u.logsShownMsg != "logs.nomatch" || len(u.logsText.Segments) != 1 {
		t.Fatalf("no match shows %q", u.logsShownMsg)
	}
	msg := u.logsText.Segments[0].(*widget.TextSegment)
	if msg.Text != u.t("logs.nomatch") || msg.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Errorf("no-match message %q in %q", msg.Text, msg.Style.ColorName)
	}
	u.logsFilter.SetText("")
	if len(u.logsShown) != 60 {
		t.Errorf("clearing the filter shows %d lines", len(u.logsShown))
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
	msg := u.logsText.Segments[0].(*widget.TextSegment)
	if msg.Text != u.t("logs.empty") || msg.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Errorf("empty message %q in %q", msg.Text, msg.Style.ColorName)
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
