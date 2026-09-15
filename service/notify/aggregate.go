package notify

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// aggregateWindow is how long repeats from one source are collected after
// its first event went out (design doc §6).
const aggregateWindow = 5 * time.Minute

// aggregated maps the Category.Kind values that are aggregated to the
// field that identifies the source; one window is kept per source.
var aggregated = map[string]string{
	"security.unauthorized": "from",
	"security.unknown_chat": "chat_id",
}

// window is one open aggregation window: the first event from a source
// passed at until-aggregateWindow, later ones are only counted.
type window struct {
	until time.Time
	count int   // occurrences in the window, the first event included
	more  bool  // at least one event after the first was absorbed
	last  Event // newest event; its Fields seed the summary
}

// aggregate is pipeline stage 2. For the kinds in aggregated, the first
// event from a source passes at once (with count "1") and opens a
// 5-minute window; further events from the same source are absorbed
// (returns false) and go out as one summary when the window closes — see
// closeWindows. Every other kind passes untouched.
func (b *Bus) aggregate(ev Event) (Event, bool) {
	field, ok := aggregated[ev.Key()]
	if !ok {
		return ev, true
	}
	now := b.now()
	n := eventCount(ev) // a backlog-merged event already stands for several
	id := windowID(ev.Key(), ev.Fields[field])
	if w, ok := b.windows[id]; ok {
		if now.Before(w.until) {
			w.count += n
			w.more = true
			w.last = ev
			return Event{}, false
		}
		// Expired but not yet ticked: summarise before starting over.
		delete(b.windows, id)
		b.summarise(w)
	}
	b.windows[id] = &window{until: now.Add(aggregateWindow), count: n, last: ev}
	return withWindowFields(ev, n), true
}

// closeWindows summarises every window whose deadline passed (or all of
// them when force is set, on Close) and forgets it, oldest window first.
// Windows that only ever saw their first event produce nothing: that
// event already went out.
func (b *Bus) closeWindows(now time.Time, force bool) {
	var due []string
	for id, w := range b.windows {
		if force || !now.Before(w.until) {
			due = append(due, id)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		wi, wj := b.windows[due[i]], b.windows[due[j]]
		if !wi.until.Equal(wj.until) {
			return wi.until.Before(wj.until)
		}
		return due[i] < due[j]
	})
	for _, id := range due {
		w := b.windows[id]
		delete(b.windows, id)
		b.summarise(w)
	}
}

// summarise sends the summary of a closed window through the remaining
// stages (quiet/mute, throttle, send). The summary keeps the newest
// event's fields (path, username, ...) with count set to the window total.
func (b *Bus) summarise(w *window) {
	if !w.more {
		return
	}
	b.pass(withWindowFields(w.last, w.count))
}

// withWindowFields returns ev with the aggregation fields set: count (as
// a decimal string), window ("5m", language-neutral: the templates print
// it verbatim) and window_sec ("300") for renderers that want to localise.
func withWindowFields(ev Event, count int) Event {
	ev.Fields = withFields(ev.Fields, map[string]string{
		"count":      strconv.Itoa(count),
		"window":     formatWindow(aggregateWindow),
		"window_sec": strconv.Itoa(int(aggregateWindow / time.Second)),
	})
	return ev
}

func windowID(key, source string) string { return key + "\x00" + source }

// formatWindow renders d compactly: "5m", "90s", "1h30m".
func formatWindow(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0 && d > time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, d%time.Hour/time.Minute)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", d/time.Second)
	}
}
