package notify

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// holdRecheck caps how long the worker sleeps while events are held.
	// Quiet-hours settings are read live, so a window that was shortened
	// or switched off is noticed within this long even with no traffic.
	holdRecheck = time.Minute
	// digestItems is how many of the held events the digest lists (newest).
	digestItems = 3
)

// hold parks ev for the digest (design doc §5). q and now are what quiet
// decided on, so the digest can name the window that held its events.
func (b *Bus) hold(ev Event, q QuietHours, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.heldTotal == 0 {
		b.heldSince = now
		b.heldByQ = false
		// A mute's silence started when it was set, not at its first victim.
		if !b.mutedUntil.IsZero() && now.Before(b.mutedUntil) && b.muteFrom.Before(now) {
			b.heldSince = b.muteFrom
		}
	}
	if q.Active(now) {
		b.heldQuiet, b.heldByQ = q, true
	}
	b.heldTotal++
	if len(b.held) >= heldCapacity {
		b.held = append(b.held[:0], b.held[1:]...)
	}
	b.held = append(b.held, ev)
}

// heldCount is how many events the backlog stands for (dropped included).
func (b *Bus) heldCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.heldTotal
}

// dropHeld empties the backlog without a digest and returns its count.
func (b *Bus) dropHeld() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.heldTotal
	b.held, b.heldTotal, b.heldSince, b.heldByQ = nil, 0, time.Time{}, false
	return n
}

// heldDeadline is when the backlog should next be looked at: the end of
// the mute or quiet window that is holding it, capped at holdRecheck so
// live config changes are picked up. Zero when nothing is held.
func (b *Bus) heldDeadline(now time.Time) time.Time {
	b.mu.Lock()
	n, muted := b.heldTotal, b.mutedUntil
	b.mu.Unlock()
	if n == 0 {
		return time.Time{}
	}
	at := now.Add(holdRecheck)
	if !muted.IsZero() && now.Before(muted) {
		at = earliest(at, muted)
	}
	if b.opts.Quiet != nil {
		if end := b.opts.Quiet().NextEnd(now); !end.IsZero() {
			at = earliest(at, end)
		}
	}
	return at
}

// flushHeld releases the backlog: as one system.digest when
// QuietHours.Digest is on (the default when no quiet hours are wired),
// otherwise it is dropped. Worker-only, called by tick once neither quiet
// hours nor a mute is in force.
func (b *Bus) flushHeld(now time.Time) {
	b.mu.Lock()
	held, total, since, q, byQuiet := b.held, b.heldTotal, b.heldSince, b.heldQuiet, b.heldByQ
	b.held, b.heldTotal, b.heldSince, b.heldByQ = nil, 0, time.Time{}, false
	b.mu.Unlock()
	if total == 0 {
		return
	}
	period := since.Format("15:04") + "–" + now.Format("15:04")
	if byQuiet {
		period = q.Start + "–" + q.End
	}
	if b.opts.Quiet != nil && !b.opts.Quiet().Digest {
		b.logf("notify: %s over, %d held event(s) dropped (digest off)", period, total)
		return
	}
	b.logf("notify: %s over, sending digest of %d held event(s)", period, total)
	b.deliver(digestEvent(held, total, period, now))
}

// digestEvent builds the system.digest event: count is everything held
// (dropped ones included), items lists the newest digestItems events one
// per line as "[HH:MM] category.kind — key=value, ...". The Telegram
// template prints items verbatim under the summary line (values are
// HTML-escaped by the renderer).
func digestEvent(held []Event, total int, period string, now time.Time) Event {
	if len(held) > digestItems {
		held = held[len(held)-digestItems:]
	}
	lines := make([]string, 0, len(held))
	for _, e := range held {
		lines = append(lines, digestLine(e))
	}
	return Event{
		Category: "system",
		Kind:     "digest",
		At:       now,
		Fields: map[string]string{
			"count":  strconv.Itoa(total),
			"period": period,
			"items":  strings.Join(lines, "\n"),
		},
	}
}

func digestLine(e Event) string {
	var sb strings.Builder
	sb.WriteString("[")
	sb.WriteString(e.At.Format("15:04"))
	sb.WriteString("] ")
	sb.WriteString(e.Key())
	if len(e.Fields) == 0 {
		return sb.String()
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString(" — ")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(e.Fields[k])
	}
	return sb.String()
}
