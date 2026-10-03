package devstate

import (
	"sync"
	"time"
)

// Refresh gates a background reading that several callers may ask for at
// once (a status read, a session change, the service start): one runs at
// a time, and a new one starts at most once per interval unless it is
// forced. The reading itself is the caller's; Refresh only says when.
type Refresh struct {
	mu      sync.Mutex
	running bool
	// again is a forced Begin that arrived while a reading ran.
	again bool
	// last is when the newest reading began.
	last time.Time
}

// Begin reports whether a reading may start at now: none is running and
// the last one began at least every ago (a clock that went back counts as
// long ago). force skips the interval — the thing read has changed, so the
// previous reading says nothing — and, while one runs, asks for one more
// once it ends (End). A true Begin must be followed by End.
func (r *Refresh) Begin(now time.Time, every time.Duration, force bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		if force {
			r.again = true
		}
		return false
	}
	if !force && !r.last.IsZero() {
		if d := now.Sub(r.last); d >= 0 && d < every {
			return false
		}
	}
	r.running, r.last = true, now
	return true
}

// End finishes the reading Begin allowed. again is true when a forced
// Begin arrived meanwhile: the reading is still running, the caller reads
// once more (as of now) and calls End again.
func (r *Refresh) End(now time.Time) (again bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.again {
		r.again, r.last = false, now
		return true
	}
	r.running = false
	return false
}

// Reset forgets everything (tests).
func (r *Refresh) Reset() {
	r.mu.Lock()
	r.running, r.again, r.last = false, false, time.Time{}
	r.mu.Unlock()
}
