package gui

// Syntax colours for the logs tab (#134): each service log line is split
// into parts that carry a theme colour name, so dark and light mode follow
// the theme by themselves.

import (
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// logPart is a run of a log line in one colour.
type logPart struct {
	text  string
	color fyne.ThemeColorName
}

var (
	// logStampRe is log.LstdFlags' "2006/01/02 15:04:05" (with optional
	// microseconds), followed by a space or the end of the line.
	logStampRe = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} )(\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?)(?:\s|$)`)
	// logTagRe is a "[debug]"-style tag at the start of the message.
	logTagRe = regexp.MustCompile(`^\s*\[[^\[\]\s]{1,24}\](?:\s|$)`)
	// logWarnRe is a leading "WARNING" or "WARNING:" token.
	logWarnRe = regexp.MustCompile(`^\s*WARNING:?(?:\s|$)`)
	// logErrorRe marks a line as an error: the word "error" (which covers
	// "error:"), "panic" and what starts with it ("panicked"), and the word
	// "failed". Whole words, so "errors=0" or "failover" stay plain.
	logErrorRe = regexp.MustCompile(`(?i)\berror\b|\bpanic|\bfailed\b`)
)

// logSegments splits a service log line into coloured parts:
//
//   - the date, dim (PlaceHolder);
//   - the time, in the normal text colour: the theme has no named colour
//     between PlaceHolder and Foreground, and the dim date in front of it
//     is enough to set the time apart from the message;
//   - a leading "[tag]" (Primary) and a leading "WARNING:" (Warning), in
//     either order, each at most once;
//   - the message, in Foreground, or in Error when the line reads like an
//     error (logErrorRe).
//
// A line without the timestamp (the rest of a multi-line panic stack, an
// older format, a blank line) is all message: only the error colour
// applies to it. Concatenating the parts' text gives the line back.
func logSegments(line string) []logPart {
	date, clock, rest, ok := logStamp(line)
	if !ok {
		return []logPart{{line, logMessageColor(line)}}
	}
	parts := []logPart{
		{date, theme.ColorNamePlaceHolder},
		{clock, theme.ColorNameForeground},
	}
	return append(parts, logMessageParts(rest)...)
}

// logCells splits a line into the logs view's two columns (#134). The
// stamp is the timestamp as "MM/DD HH:MM:SS": the year (and microseconds,
// if any) left out to give the message the width, while the filter still
// matches the whole line. Its date is dim and its time in the text
// colour, as in logSegments. msg is the rest of the line, coloured as in
// logSegments but without the space that followed the timestamp. A line
// without a timestamp has no stamp and is all message.
func logCells(line string) (stamp, msg []logPart) {
	date, clock, rest, ok := logStamp(line)
	if !ok {
		return nil, logSegments(line)
	}
	stamp = []logPart{
		{date[len("2006/"):], theme.ColorNamePlaceHolder},
		{clock[:len("15:04:05")], theme.ColorNameForeground},
	}
	if rest != "" && isLogSpace(rest[0]) {
		rest = rest[1:]
	}
	return stamp, logMessageParts(rest)
}

// logStamp splits a line that starts with a timestamp into the date (with
// the space after it), the time, and the rest of the line, which starts
// with the separating whitespace unless the time ends the line. ok is
// false for a line without a timestamp.
func logStamp(line string) (date, clock, rest string, ok bool) {
	m := logStampRe.FindStringSubmatchIndex(line)
	if m == nil {
		return "", "", line, false
	}
	return line[m[2]:m[3]], line[m[4]:m[5]], line[m[5]:], true
}

// logMessageParts colours what follows the timestamp: the leading tokens
// in their colours, then the rest of the message in one colour, picked on
// the whole of it.
func logMessageParts(rest string) []logPart {
	color := logMessageColor(rest)
	var parts []logPart
	// "[debug] …", "WARNING: …" and "WARNING: [debug] …" all occur.
	var tagged, warned bool
	for {
		if loc := logTagRe.FindStringIndex(rest); loc != nil && !tagged {
			parts, rest = appendLeading(parts, rest, loc[1], theme.ColorNamePrimary)
			tagged = true
			continue
		}
		if loc := logWarnRe.FindStringIndex(rest); loc != nil && !warned {
			parts, rest = appendLeading(parts, rest, loc[1], theme.ColorNameWarning)
			warned = true
			continue
		}
		break
	}
	if rest != "" {
		parts = append(parts, logPart{rest, color})
	}
	return parts
}

// appendLeading appends rest[:end] as one part in color, without the
// whitespace the token regexps match after it (that stays with the next
// part), and returns what is left of rest.
func appendLeading(parts []logPart, rest string, end int, color fyne.ThemeColorName) ([]logPart, string) {
	for end > 0 && isLogSpace(rest[end-1]) {
		end--
	}
	return append(parts, logPart{rest[:end], color}), rest[end:]
}

// isLogSpace is the whitespace \s matches in the token regexps.
func isLogSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// logMessageColor is Error for a line that reads like an error, else
// Foreground. text is what follows the timestamp (or the whole line).
func logMessageColor(text string) fyne.ThemeColorName {
	if logErrorRe.MatchString(text) {
		return theme.ColorNameError
	}
	return theme.ColorNameForeground
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
