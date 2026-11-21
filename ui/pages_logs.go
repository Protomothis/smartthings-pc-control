package ui

// 로그: the service log, newest at the bottom. While the page is in front
// it is read every 3s — from the service (the poller, logsWanter), or,
// while the service does not answer, from service.log on disk (#140), with
// a banner saying so. A filter field and level chips narrow it; the view
// follows the newest line unless the user scrolled up (a pill brings it
// back); 일시정지 stops the reading. The file can be opened or shown in
// Explorer.
//
// The page fills the window under the top bar: the log card takes what
// the controls leave, and its rows are a virtualised list, so a few
// thousand lines cost what the visible ones cost.

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

const (
	// logsFileTail is how many lines of service.log the page reads from
	// disk: more than the service's 100, since that is when the log is
	// needed most.
	logsFileTail = 2000
	// logsMinH is the least height of the page body (dp); a shorter
	// window scrolls the page instead.
	logsMinH unit.Dp = 380
	// logsCompactW is the log card width (dp) under which a row shows the
	// time without the date.
	logsCompactW unit.Dp = 520
	// logsFace is the rows' typeface; Hangul falls back to the UI font.
	logsFace = "Consolas, Malgun Gothic"
	// logsDateGap sets the date apart from the time.
	logsDateGap unit.Dp = 6
)

// Level chips, in order.
const (
	levelAll = iota
	levelError
	levelWarn
	levelDebug
)

// levelMasks is the gui.LogLevel each chip shows (0: every line).
var levelMasks = [...]gui.LogLevel{0, gui.LogLevelError, gui.LogLevelWarn, gui.LogLevelDebug}

// logsFromDisk reads the tail of service.log: its lines and the banner
// text. ok is false when it could not be read (the banner says why). The
// tests replace it.
var logsFromDisk = func(l gui.Lang) (lines []string, note string, ok bool) {
	path, err := gui.ServiceLogPath()
	if err == nil {
		lines, err = gui.ReadServiceLogTail(path, logsFileTail)
	}
	return lines, gui.LogsFileNote(l, path, err), err == nil
}

// logRow is one parsed line.
type logRow struct {
	line  string
	lower string        // for the filter
	stamp []gui.LogPart // date and time, or none
	msg   []gui.LogPart // tabs expanded for drawing
	own   gui.LogLevel  // the line's own kinds (the chips count these)
	lv    gui.LogLevel  // own, plus the line above's for a line without a timestamp
}

// parseLogRows parses lines. A line without a timestamp (a panic's stack,
// the rest of a multi-line message) belongs to the line above it, so it
// takes that line's level too: 오류 shows a panic with its stack.
func parseLogRows(lines []string) ([]logRow, [len(levelMasks)]int) {
	rows := make([]logRow, len(lines))
	var counts [len(levelMasks)]int
	var prev gui.LogLevel
	for i, l := range lines {
		stamp, msg := gui.LogCells(l)
		for j := range msg {
			msg[j].Text = strings.ReplaceAll(msg[j].Text, "\t", "    ")
		}
		own := gui.LogLineLevel(msg)
		lv := own
		if stamp == nil {
			lv |= prev
		}
		prev = lv
		rows[i] = logRow{line: l, lower: strings.ToLower(l), stamp: stamp, msg: msg, own: own, lv: lv}
		counts[levelAll]++
		for c := levelError; c < len(levelMasks); c++ {
			if own&levelMasks[c] != 0 {
				counts[c]++
			}
		}
	}
	return rows, counts
}

// filterLogRows is the indices of rows that contain query (lower case,
// "" for any) and are of a kind in mask (0 for any).
func filterLogRows(rows []logRow, query string, mask gui.LogLevel) []int {
	out := make([]int, 0, len(rows))
	for i, r := range rows {
		if mask != 0 && r.lv&mask == 0 {
			continue
		}
		if query != "" && !strings.Contains(r.lower, query) {
			continue
		}
		out = append(out, i)
	}
	return out
}

type logsPage struct {
	filter   widget.Editor
	clear    widget.Clickable
	levels   chipGroup
	level    int
	live     widget.Clickable
	paused   bool
	refresh  widget.Clickable
	openFile widget.Clickable
	openDir  widget.Clickable
	report   widget.Clickable // 문제 보내기 (#175)
	toEnd    widget.Clickable
	list     widget.List

	// What is parsed: the source lines (compared by identity: a new read
	// is a new slice), the rows, and the filtered view of them.
	src      []string
	srcFile  bool
	rows     []logRow
	counts   [len(levelMasks)]int
	query    string
	viewLv   int
	view     []int
	viewText []string // the lines of view, to keep the place through a poll

	// service.log read from disk (#140). Window goroutine only.
	file     []string
	fileNote string
	fileOK   bool
	fileSet  bool
	fileBusy bool
	fileAt   time.Time

	away  bool // the user scrolled up from the newest line
	jump  bool // go to the newest line at the next layout
	listW int  // the list's width last frame: rows re-wrap when it changes
}

func newLogsPage() screen {
	p := &logsPage{jump: true}
	p.list.Axis = layout.Vertical
	p.filter.SingleLine = true
	return p
}

func (p *logsPage) title(u *UI) string { return u.t("tab.logs") }

// shown starts live at the newest line and reads the log at once.
func (p *logsPage) shown(u *UI, s snapshot) {
	p.paused, p.jump, p.away = false, true, false
	p.fileAt = time.Time{}
	if s.conn == connOK {
		go u.loadLogs()
	}
}

// wantsLogs: the poller reads the log every 3s unless paused.
func (p *logsPage) wantsLogs() bool { return !p.paused }

// minBodyHeight makes the page fill the window (viewportFiller).
func (p *logsPage) minBodyHeight() unit.Dp { return logsMinH }

// fromFile is whether the page shows service.log from disk: the service
// is not there, or did not give its log.
func fromFile(s snapshot) bool { return s.conn != connOK || s.logsErr != nil }

// loadFile reads service.log off the window goroutine.
func (p *logsPage) loadFile(u *UI) {
	if p.fileBusy {
		return
	}
	p.fileBusy, p.fileAt = true, time.Now()
	l := u.language()
	go func() {
		lines, note, ok := logsFromDisk(l)
		u.onWindow(func() {
			p.file, p.fileNote, p.fileOK, p.fileSet, p.fileBusy = lines, note, ok, true, false
		})
	}()
}

// reload reads the log now, from wherever it comes from.
func (p *logsPage) reload(u *UI, s snapshot) {
	if fromFile(s) {
		p.loadFile(u)
		return
	}
	go u.loadLogs()
}

// sync parses lines when they are new and filters them when the filter
// changed. While the user reads further up, the view keeps its place:
// the lines that dropped off the top move it up by as many.
func (p *logsPage) sync(lines []string, file bool, query string, level int) {
	same := file == p.srcFile && len(lines) == len(p.src) && (len(lines) == 0 || &lines[0] == &p.src[0])
	if same && p.rows != nil && query == p.query && level == p.viewLv && p.view != nil {
		return
	}
	if !same || p.rows == nil {
		p.src, p.srcFile = lines, file
		p.rows, p.counts = parseLogRows(lines)
	}
	filterChanged := query != p.query || level != p.viewLv
	p.query, p.viewLv = query, level
	old := p.viewText
	p.view = filterLogRows(p.rows, query, levelMasks[level])
	p.viewText = make([]string, len(p.view))
	for i, r := range p.view {
		p.viewText[i] = p.rows[r].line
	}
	switch {
	case filterChanged:
		p.jump = true
	case p.away:
		if k := gui.LogsShift(old, p.viewText); k > 0 {
			pos := &p.list.Position
			pos.First -= k
			if pos.First < 0 {
				pos.First, pos.Offset = 0, 0
			}
		}
	default:
		p.jump = true // following: stay on the newest line
	}
}

func (p *logsPage) body(gtx C, u *UI, s snapshot) D {
	th := u.th
	if i := p.levels.Clicked(gtx, len(levelMasks)); i >= 0 {
		p.level = i
	}
	if p.clear.Clicked(gtx) {
		p.filter.SetText("")
	}
	if p.live.Clicked(gtx) {
		p.paused = !p.paused
		if !p.paused {
			p.jump = true
			p.reload(u, s)
		}
	}
	if p.refresh.Clicked(gtx) {
		p.reload(u, s)
	}
	if p.openFile.Clicked(gtx) {
		u.openServiceLog(false)
	}
	if p.report.Clicked(gtx) {
		u.open(pageReport)
	}
	if p.openDir.Clicked(gtx) {
		u.openServiceLog(true)
	}
	if p.toEnd.Clicked(gtx) {
		p.jump = true
	}

	file := fromFile(s)
	if file && !p.paused && !p.fileBusy {
		if !gtx.Now.Before(p.fileAt.Add(logsEvery * time.Second)) {
			p.loadFile(u)
		} else {
			gtx.Execute(op.InvalidateCmd{At: p.fileAt.Add(logsEvery * time.Second)})
		}
	}
	lines, loaded := s.logs, s.logsSet
	if file {
		lines, loaded = p.file, p.fileSet
	}
	p.sync(lines, file, strings.ToLower(strings.TrimSpace(p.filter.Text())), p.level)

	// The window gives the body the viewport's height (viewportFiller).
	h := gtx.Constraints.Max.Y
	gtx.Constraints.Min.Y = h

	var children []layout.FlexChild
	if file && p.fileSet {
		t := toneWarn
		if !p.fileOK {
			t = toneBad
		}
		children = append(children,
			layout.Rigid(func(gtx C) D { return banner(gtx, th, t, p.fileNote) }),
			vspace(gapS))
	}
	children = append(children,
		layout.Rigid(func(gtx C) D { return p.controls(gtx, u) }),
		vspace(gapS),
		layout.Flexed(1, func(gtx C) D { return p.logCard(gtx, u, loaded) }),
		vspace(gapS),
		layout.Rigid(func(gtx C) D { return p.reportLine(gtx, u) }),
	)
	layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	return D{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// controls is the filter card: the search field, the level chips and the
// live switch.
func (p *logsPage) controls(gtx C, u *UI) D {
	th := u.th
	return surface(gtx, colCard, radiusL, func(gtx C) D {
		return layout.UniformInset(gapM).Layout(gtx, func(gtx C) D {
			return column(gtx,
				layout.Rigid(func(gtx C) D { return searchField(gtx, u.th, &p.filter, &p.clear, u.t("logs.filter")) }),
				vspace(gapS),
				layout.Rigid(func(gtx C) D {
					labels := []string{
						p.levelLabel(u.t("ui.logs.level.all"), 0),
						p.levelLabel(u.t("ui.logs.level.error"), p.counts[levelError]),
						p.levelLabel(u.t("ui.logs.level.warn"), p.counts[levelWarn]),
						p.levelLabel(u.t("ui.logs.level.debug"), p.counts[levelDebug]),
					}
					return layout.Flex{Alignment: layout.Start}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D { return p.levels.Layout(gtx, th, labels, p.level) }),
						hspace(gapXS),
						layout.Rigid(func(gtx C) D {
							return liveButton(gtx, u.th, &p.live, !p.paused, u.t("ui.logs.live"), u.t("ui.logs.paused"))
						}),
					)
				}),
			)
		})
	})
}

// reportLine is the way to 문제 보내기 under the log (#175): whoever reads
// the log is likely looking at a problem. It is a card like the filter card
// above, so the button stands out from the page instead of reading as a
// link; the button has the light accent of the home card's [실행].
func (p *logsPage) reportLine(gtx C, u *UI) D {
	th := u.th
	return surface(gtx, colCard, radiusL, func(gtx C) D {
		return layout.Inset{Top: gapS, Bottom: gapS, Left: gapM, Right: gapS}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "bug", 18, colSub) }),
				hspace(8),
				layout.Flexed(1, label(th, textS, u.t("ui.report.fromlogs"), colSub, font.Normal).Layout),
				hspace(gapXS),
				layout.Rigid(func(gtx C) D {
					b := smallButton(th, &p.report, u.t("ui.report.title"), colAccent)
					b.Background = colAccentDim
					return b.Layout(gtx)
				}),
			)
		})
	})
}

func (p *logsPage) levelLabel(name string, n int) string {
	if n == 0 {
		return name
	}
	return fmt.Sprintf("%s %d", name, n)
}

// logCard is the log itself: a header with the count and the file
// actions over the rows, which fill the rest of the card.
func (p *logsPage) logCard(gtx C, u *UI, loaded bool) D {
	th := u.th
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	return surface(gtx, colCard, radiusL, func(gtx C) D {
		gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
		return column(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 10, Bottom: 10, Left: gapM, Right: 10}.Layout(gtx, func(gtx C) D {
					return row(gtx,
						layout.Flexed(1, line(th, textS, p.countText(u), colSub, font.Medium).Layout),
						hspace(gapXS),
						layout.Rigid(smallButton(th, &p.refresh, u.t("logs.refresh"), colText).Layout),
						hspace(gapXS),
						layout.Rigid(smallButton(th, &p.openFile, u.t("ui.logs.openfile"), colText).Layout),
						hspace(gapXS),
						layout.Rigid(smallButton(th, &p.openDir, u.t("logs.openfolder"), colText).Layout),
					)
				})
			}),
			layout.Rigid(divider),
			layout.Flexed(1, func(gtx C) D { return p.rowsView(gtx, u, loaded) }),
		)
	})
}

func (p *logsPage) countText(u *UI) string {
	switch {
	case len(p.rows) == 0:
		return ""
	case len(p.view) != len(p.rows):
		return fmt.Sprintf(u.t("ui.logs.count.filtered"), len(p.rows), len(p.view))
	}
	return fmt.Sprintf(u.t("ui.logs.count"), len(p.rows))
}

// rowsView is the virtualised list of the filtered rows, or the message
// in their place, with the 최신 로그로 pill over it while scrolled up.
func (p *logsPage) rowsView(gtx C, u *UI, loaded bool) D {
	th := u.th
	gtx.Constraints.Min = gtx.Constraints.Max
	var msg string
	switch {
	case !loaded:
		msg = u.t("ui.logs.loading")
	case len(p.rows) == 0:
		msg = u.t("logs.empty")
	case len(p.view) == 0:
		msg = u.t("logs.nomatch")
	}
	if msg != "" {
		p.away = false
		return layout.Center.Layout(gtx, label(th, textM, msg, colSub, font.Normal).Layout)
	}

	compact := gtx.Constraints.Max.X < gtx.Dp(logsCompactW)
	stampW := p.stampWidth(gtx, th, compact)
	if w := gtx.Constraints.Max.X; w != p.listW {
		if p.listW != 0 && !p.away {
			p.jump = true // re-wrapped rows: find the end again
		}
		p.listW = w
	}
	if p.jump {
		p.list.Position = layout.Position{First: len(p.view)} // laid out back from the end
		p.jump = false
	}
	return layout.Stack{Alignment: layout.S}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
				l := material.List(th, &p.list)
				d := l.Layout(gtx, len(p.view), func(gtx C, i int) D {
					return p.rowView(gtx, th, &p.rows[p.view[i]], stampW, compact)
				})
				p.away = p.list.Position.BeforeEnd
				return d
			})
		}),
		layout.Stacked(func(gtx C) D {
			if !p.away {
				return D{}
			}
			return layout.Inset{Bottom: gapS}.Layout(gtx, func(gtx C) D { return floatingPill(gtx, u.th, &p.toEnd, "todown", u.t("ui.logs.toend")) })
		}),
	)
}

// stampWidth is the width of the time column: the widest stamp in the
// monospace face.
func (p *logsPage) stampWidth(gtx C, th *material.Theme, compact bool) int {
	m := op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints.Min = image.Point{}
	w := p.mono(th, "00:00:00", colText).Layout(cg).Size.X
	if !compact {
		w += p.mono(th, "00/00", colText).Layout(cg).Size.X + gtx.Dp(logsDateGap)
	}
	m.Stop()
	return w + 1 // rounding: never wrap the time
}

// mono is a label in the log face.
func (p *logsPage) mono(th *material.Theme, txt string, col color.NRGBA) material.LabelStyle {
	l := label(th, textS, txt, col, font.Normal)
	l.Font.Typeface = logsFace
	return l
}

// logRoleColor is the palette colour of a part of a line (gui.LogRole).
func logRoleColor(r gui.LogRole) color.NRGBA {
	switch r {
	case gui.LogDim:
		return colSub
	case gui.LogTag:
		return colAccent
	case gui.LogWarn:
		return colWarn
	case gui.LogError:
		return colDanger
	}
	return colText
}

// rowView draws one line: the stamp in its column, the leading tokens
// ([debug], WARNING:) and the message wrapping beside them. A line that
// reads like an error sits on a faint red tint.
func (p *logsPage) rowView(gtx C, th *material.Theme, r *logRow, stampW int, compact bool) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	content := func(gtx C) D {
		return layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}.Layout(gtx, func(gtx C) D {
			children := make([]layout.FlexChild, 0, 2+2*len(r.msg))
			children = append(children,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = stampW, stampW
					if len(r.stamp) < 2 {
						return D{Size: image.Pt(stampW, 0)}
					}
					clock := p.mono(th, r.stamp[1].Text, logRoleColor(r.stamp[1].Role))
					clock.MaxLines = 1
					if compact {
						return clock.Layout(gtx)
					}
					date := p.mono(th, strings.TrimSpace(r.stamp[0].Text), logRoleColor(r.stamp[0].Role))
					date.MaxLines = 1
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(date.Layout),
						hspace(logsDateGap),
						layout.Rigid(clock.Layout),
					)
				}),
				hspace(10),
			)
			for i, part := range r.msg {
				txt := part.Text
				if i > 0 {
					txt = strings.TrimPrefix(txt, " ")
					children = append(children, hspace(6))
				}
				lb := p.mono(th, txt, logRoleColor(part.Role))
				if part.Role == gui.LogTag || part.Role == gui.LogWarn {
					lb.Font.Weight = font.SemiBold
				}
				if i < len(r.msg)-1 {
					lb.MaxLines = 1
					children = append(children, layout.Rigid(lb.Layout))
				} else {
					children = append(children, layout.Flexed(1, lb.Layout))
				}
			}
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, children...)
		})
	}
	return layout.Inset{Left: 6, Right: 6}.Layout(gtx, func(gtx C) D {
		if r.own&gui.LogLevelError == 0 {
			return content(gtx)
		}
		return surface(gtx, mix(colCard, colDanger, 0.08), radiusS, content)
	})
}

// openServiceLog opens service.log with its program, or shows it selected
// in Explorer; a missing file is a toast.
func (u *UI) openServiceLog(folder bool) {
	path, err := gui.ServiceLogPath()
	if err == nil {
		_, err = os.Stat(path)
	}
	if err != nil {
		u.toast.show(fmt.Sprintf(u.t("logs.notfound"), path), true, time.Now())
		return
	}
	open := openPath
	if folder {
		open = revealPath
	}
	u.act(func() error { return open(path) }, "", nil)
}
