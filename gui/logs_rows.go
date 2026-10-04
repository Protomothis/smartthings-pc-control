package gui

// The rows of the logs tab (#134): one row per log line, the timestamp in
// a fixed-width column on the left and the message wrapping in its own
// column on the right, so a long line hangs under its message instead of
// running back under the timestamps.

import (
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// logStampSample is the widest timestamp the left column shows, for its
// width: the monospace font gives every "MM/DD HH:MM:SS" the same.
const logStampSample = "00/00 00:00:00"

// logRow is the widgets of one row of the logs view. Rows are pooled and
// kept across updates (ui.logRowPool); each remembers the line it shows,
// so a row that keeps its line is not redrawn.
type logRow struct {
	sep   *widget.Separator // above the row; not shown above the first
	stamp *widget.RichText  // the timestamp, empty for a line without one
	msg   *widget.RichText  // the rest of the line, wrapping
	line  string
	set   bool // line is shown (a new row shows nothing yet)
}

func newLogRow() *logRow {
	r := &logRow{
		sep:   widget.NewSeparator(),
		stamp: widget.NewRichText(),
		msg:   widget.NewRichText(),
	}
	r.msg.Wrapping = fyne.TextWrapBreak
	return r
}

// show puts line in the row, unless it shows that line already.
func (r *logRow) show(line string) {
	if r.set && r.line == line {
		return
	}
	r.line, r.set = line, true
	stamp, msg := logCells(line)
	r.stamp.Segments = logRichSegments(stamp)
	r.stamp.Refresh()
	r.msg.Segments = logRichSegments(msg)
	r.msg.Refresh()
}

// logsShift is how many lines dropped off the top between old and lines:
// the smallest k for which lines starts with old[k:] (old itself counts,
// at k 0). It is 0 when lines does not continue old at all. Rows rotate by
// it, so they keep their lines through the usual poll, where a few lines
// come in at the bottom and as many go out at the top of the 100.
func logsShift(old, lines []string) int {
	for k := range old {
		if n := len(old) - k; n <= len(lines) && slices.Equal(old[k:], lines[:n]) {
			return k
		}
	}
	return 0
}

// logRowsLayout stacks rows (the container's objects are their widgets;
// the layout goes by rows): the stamp cells in a column as wide as
// logStampSample, the message cells beside them, each row as tall as its
// wrapped message, with a separator line in the theme's line spacing
// between rows.
//
// A RichText wraps at the width it was last given and pads its text by
// the inner padding on every side. The layout gives each message its width
// before it measures its height, so one pass places the rows; and it
// overlaps the vertical padding of neighbouring rows, so a row takes the
// height of its text and the rows sit as close as the paragraphs of the
// single RichText the view used to be. MinSize sums the rows' heights at
// the widths they were last laid out at: right once the rows have been
// laid out at the view's width, which logsToBottom and the canvas see to.
type logRowsLayout struct {
	rows []*logRow
}

// logRowsMetrics are the theme sizes the layout works with.
type logRowsMetrics struct {
	pad    float32 // RichText's inner padding, on each side of its text
	gap    float32 // between rows
	sep    float32 // separator thickness
	stampX float32 // where the message column starts
}

func newLogRowsMetrics() logRowsMetrics {
	pad := theme.Size(theme.SizeNameInnerPadding)
	text := fyne.MeasureText(logStampSample, theme.Size(theme.SizeNameText), fyne.TextStyle{Monospace: true})
	return logRowsMetrics{
		pad: pad,
		gap: theme.Size(theme.SizeNameLineSpacing),
		sep: theme.Size(theme.SizeNameSeparatorThickness),
		// The stamp's left padding and text; the message's own left
		// padding then sets the two columns apart.
		stampX: pad + text.Width,
	}
}

// rowHeight is the height of r's text: its taller cell without the
// padding.
func (m logRowsMetrics) rowHeight(r *logRow) float32 {
	return fyne.Max(r.stamp.MinSize().Height, r.msg.MinSize().Height) - 2*m.pad
}

func (l *logRowsLayout) Layout(_ []fyne.CanvasObject, size fyne.Size) {
	m := newLogRowsMetrics()
	msgW := fyne.Max(0, size.Width-m.stampX)
	y := m.pad
	for i, r := range l.rows {
		if i > 0 {
			r.sep.Move(fyne.NewPos(0, y+(m.gap-m.sep)/2))
			r.sep.Resize(fyne.NewSize(size.Width, m.sep))
			y += m.gap
		}
		// Width first: the message re-wraps to it, and only then does its
		// MinSize give the height at that width.
		want := fyne.NewSize(msgW, r.msg.MinSize().Height)
		r.msg.Resize(want)
		if h := r.msg.MinSize().Height; h != want.Height {
			r.msg.Resize(fyne.NewSize(msgW, h))
		}
		r.msg.Move(fyne.NewPos(m.stampX, y-m.pad))
		r.stamp.Resize(fyne.NewSize(m.stampX+m.pad, r.stamp.MinSize().Height))
		r.stamp.Move(fyne.NewPos(0, y-m.pad))
		y += m.rowHeight(r)
	}
}

func (l *logRowsLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	m := newLogRowsMetrics()
	width := m.stampX + 2*m.pad
	if len(l.rows) == 0 {
		return fyne.NewSize(width, 0)
	}
	h := 2*m.pad + m.gap*float32(len(l.rows)-1)
	for _, r := range l.rows {
		h += m.rowHeight(r)
	}
	return fyne.NewSize(width, h)
}
