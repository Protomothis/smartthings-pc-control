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
	m := logStampRe.FindStringSubmatchIndex(line)
	if m == nil {
		return []logPart{{line, logMessageColor(line)}}
	}
	date, clock := line[m[2]:m[3]], line[m[4]:m[5]]
	rest := line[m[5]:]
	parts := []logPart{
		{date, theme.ColorNamePlaceHolder},
		{clock, theme.ColorNameForeground},
	}
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
		parts = append(parts, logPart{rest, logMessageColor(line[m[5]:])})
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

// logRichSegments turns lines into RichText segments, one paragraph per
// line: within a line the parts are inline segments, and the last one is
// not, which is what ends the row (RichText wraps a long line inside its
// paragraph). Monospace, like the label this replaced.
func logRichSegments(lines []string) []widget.RichTextSegment {
	segs := make([]widget.RichTextSegment, 0, len(lines)*4)
	for _, line := range lines {
		parts := logSegments(line)
		for i, p := range parts {
			segs = append(segs, logTextSegment(p.text, p.color, i < len(parts)-1))
		}
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
