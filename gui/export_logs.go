package gui

// The service log without a toolkit (#134, #140, #165): where service.log
// lives and how its tail is read from disk, how a line splits into
// coloured parts, what level a line is, and how far a new tail moved up.
// The Fyne logs tab (logs_*.go) and the Gio logs page (ui/pages_logs.go)
// both use it.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ServiceLogPath is service.log next to the exe — the same location the
// service uses.
func ServiceLogPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "service.log"), nil
}

// ReadServiceLogTail is the last max lines of the log at path, split like
// /api/logs does.
func ReadServiceLogTail(path string, max int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n"))
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines, nil
}

// LogsFileNote is the line above a log read from disk: where it came
// from, or why reading it failed.
func LogsFileNote(l Lang, path string, err error) string {
	switch {
	case err == nil:
		return T(l, "logs.file.shown")
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf(T(l, "logs.file.denied"), path)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf(T(l, "logs.file.missing"), path)
	}
	return fmt.Sprintf(T(l, "logs.file.error"), err)
}

// LogsShift is how many lines dropped off the top between old and lines:
// the smallest k for which lines starts with old[k:] (old itself counts,
// at k 0). It is 0 when lines does not continue old at all. A view keeps
// its place by it through the usual poll, where a few lines come in at the
// bottom and as many go out at the top of the tail.
func LogsShift(old, lines []string) int {
	for k := range old {
		if n := len(old) - k; n <= len(lines) && slices.Equal(old[k:], lines[:n]) {
			return k
		}
	}
	return 0
}

// --- colours ---

// LogRole is the colour of a part of a log line, named by what it is; each
// toolkit maps it to its theme (dark and light alike).
type LogRole int

const (
	LogText  LogRole = iota // the normal text colour (the time, a plain message)
	LogDim                  // dimmed: the date
	LogTag                  // the accent: a leading "[debug]"
	LogWarn                 // the warning colour: a leading "WARNING:"
	LogError                // the error colour: a message that reads like an error
)

// LogPart is a run of a log line in one colour.
type LogPart struct {
	Text string
	Role LogRole
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

// LogSegments splits a service log line into coloured parts:
//
//   - the date, dim;
//   - the time, in the normal text colour: the dim date in front of it is
//     enough to set the time apart from the message;
//   - a leading "[tag]" (LogTag) and a leading "WARNING:" (LogWarn), in
//     either order, each at most once;
//   - the message, in LogText, or in LogError when the line reads like an
//     error (logErrorRe).
//
// A line without the timestamp (the rest of a multi-line panic stack, an
// older format, a blank line) is all message: only the error colour
// applies to it. Concatenating the parts' text gives the line back.
func LogSegments(line string) []LogPart {
	date, clock, rest, ok := logStamp(line)
	if !ok {
		return []LogPart{{line, logMessageRole(line)}}
	}
	parts := []LogPart{{date, LogDim}, {clock, LogText}}
	return append(parts, logMessageParts(rest)...)
}

// LogCells splits a line into a log view's two columns (#134). The stamp
// is the timestamp as "MM/DD HH:MM:SS": the year (and microseconds, if
// any) left out to give the message the width, while a filter still
// matches the whole line. Its date is dim and its time in the text
// colour, as in LogSegments. msg is the rest of the line, coloured as in
// LogSegments but without the space that followed the timestamp. A line
// without a timestamp has no stamp and is all message.
func LogCells(line string) (stamp, msg []LogPart) {
	date, clock, rest, ok := logStamp(line)
	if !ok {
		return nil, LogSegments(line)
	}
	stamp = []LogPart{
		{date[len("2006/"):], LogDim},
		{clock[:len("15:04:05")], LogText},
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
func logMessageParts(rest string) []LogPart {
	role := logMessageRole(rest)
	var parts []LogPart
	// "[debug] …", "WARNING: …" and "WARNING: [debug] …" all occur.
	var tagged, warned bool
	for {
		if loc := logTagRe.FindStringIndex(rest); loc != nil && !tagged {
			parts, rest = appendLeading(parts, rest, loc[1], LogTag)
			tagged = true
			continue
		}
		if loc := logWarnRe.FindStringIndex(rest); loc != nil && !warned {
			parts, rest = appendLeading(parts, rest, loc[1], LogWarn)
			warned = true
			continue
		}
		break
	}
	if rest != "" {
		parts = append(parts, LogPart{rest, role})
	}
	return parts
}

// appendLeading appends rest[:end] as one part in role, without the
// whitespace the token regexps match after it (that stays with the next
// part), and returns what is left of rest.
func appendLeading(parts []LogPart, rest string, end int, role LogRole) ([]LogPart, string) {
	for end > 0 && isLogSpace(rest[end-1]) {
		end--
	}
	return append(parts, LogPart{rest[:end], role}), rest[end:]
}

// isLogSpace is the whitespace \s matches in the token regexps.
func isLogSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// logMessageRole is LogError for a line that reads like an error, else
// LogText. text is what follows the timestamp (or the whole line).
func logMessageRole(text string) LogRole {
	if logErrorRe.MatchString(text) {
		return LogError
	}
	return LogText
}

// --- levels ---

// LogLevel is a set of the kinds a line is, read from the same parts that
// colour it: a line can be a debug line that reports an error.
type LogLevel uint8

const (
	LogLevelError LogLevel = 1 << iota // the message reads like an error
	LogLevelWarn                       // a leading WARNING
	LogLevelDebug                      // a leading [debug]
)

// LogLineLevel is the kinds of one line, from its message parts
// (LogCells).
func LogLineLevel(msg []LogPart) LogLevel {
	var lv LogLevel
	for _, p := range msg {
		switch p.Role {
		case LogError:
			lv |= LogLevelError
		case LogWarn:
			lv |= LogLevelWarn
		case LogTag:
			if strings.EqualFold(strings.TrimSpace(p.Text), "[debug]") {
				lv |= LogLevelDebug
			}
		}
	}
	return lv
}
