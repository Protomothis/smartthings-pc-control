package notify

import (
	"strconv"
	"strings"
	"time"
)

// QuietHours is telegram.quiet_hours in config.json (design doc §5). While
// active, events other than the always-pass ones are held for a digest
// instead of being sent (issue #59 builds the digest on Bus.held).
type QuietHours struct {
	Enabled        bool   `json:"enabled"`
	Start          string `json:"start"`           // "22:00", local time
	End            string `json:"end"`             // "07:00"; may be earlier than Start (crosses midnight)
	SecurityBypass bool   `json:"security_bypass"` // default true: security.* is sent anyway
	Digest         bool   `json:"digest"`          // default true: one summary when the window ends
}

// Active reports whether now falls inside the quiet window. It is false
// when quiet hours are disabled, when either bound is malformed, or when
// Start equals End (an empty window). Start is inclusive, End exclusive, so
// 22:00–07:00 is active from 22:00:00 up to 06:59:59 and handles the
// midnight crossing.
func (q QuietHours) Active(now time.Time) bool {
	if !q.Enabled {
		return false
	}
	start, ok := parseClock(q.Start)
	if !ok {
		return false
	}
	end, ok := parseClock(q.End)
	if !ok || start == end {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	if start < end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}

// NextEnd returns the moment the current quiet window closes, for
// scheduling the digest (issue #59). It is the zero time when the window
// is not active at now.
func (q QuietHours) NextEnd(now time.Time) time.Time {
	if !q.Active(now) {
		return time.Time{}
	}
	end, _ := parseClock(q.End)
	t := time.Date(now.Year(), now.Month(), now.Day(), end/60, end%60, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// parseClock turns "HH:MM" into minutes since midnight.
func parseClock(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hour, err := strconv.Atoi(h)
	if err != nil || hour < 0 || hour > 23 {
		return 0, false
	}
	min, err := strconv.Atoi(m)
	if err != nil || min < 0 || min > 59 {
		return 0, false
	}
	return hour*60 + min, true
}
