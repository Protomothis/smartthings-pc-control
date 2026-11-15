package gui

// Syntax colours for the logs tab (#134): each service log line is split
// into parts (export_logs.go) whose roles map to theme colour names here,
// so dark and light mode follow the theme by themselves.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// logPart is a run of a log line in one theme colour.
type logPart struct {
	text  string
	color fyne.ThemeColorName
}

// logRoleColors is the theme colour of each LogRole.
var logRoleColors = map[LogRole]fyne.ThemeColorName{
	LogText:  theme.ColorNameForeground,
	LogDim:   theme.ColorNamePlaceHolder,
	LogTag:   theme.ColorNamePrimary,
	LogWarn:  theme.ColorNameWarning,
	LogError: theme.ColorNameError,
}

// themedParts gives parts their theme colours.
func themedParts(parts []LogPart) []logPart {
	if parts == nil {
		return nil
	}
	out := make([]logPart, len(parts))
	for i, p := range parts {
		out[i] = logPart{p.Text, logRoleColors[p.Role]}
	}
	return out
}

// logSegments is LogSegments in theme colours.
func logSegments(line string) []logPart { return themedParts(LogSegments(line)) }

// logCells is LogCells in theme colours.
func logCells(line string) (stamp, msg []logPart) {
	s, m := LogCells(line)
	return themedParts(s), themedParts(m)
}

// logRichSegments turns the parts of one cell into RichText segments that
// make one paragraph: every part is inline but the last, which ends it
// (RichText wraps a long paragraph inside the cell). Monospace, like the
// label the view started as. An empty cell is one empty segment, so it is
// as tall as a line of monospace text, like the cell beside it.
func logRichSegments(parts []logPart) []widget.RichTextSegment {
	if len(parts) == 0 {
		return []widget.RichTextSegment{logTextSegment("", theme.ColorNameForeground, false)}
	}
	segs := make([]widget.RichTextSegment, len(parts))
	for i, p := range parts {
		segs[i] = logTextSegment(p.text, p.color, i < len(parts)-1)
	}
	return segs
}

// logTextSegment is one monospace run of the logs view.
func logTextSegment(text string, color fyne.ThemeColorName, inline bool) *widget.TextSegment {
	return &widget.TextSegment{Text: text, Style: widget.RichTextStyle{
		ColorName: color,
		Inline:    inline,
		SizeName:  theme.SizeNameText,
		TextStyle: fyne.TextStyle{Monospace: true},
	}}
}
