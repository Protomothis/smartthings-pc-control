// Package ratelimit is the one sliding-window limiter the service uses
// (#127: one type for what were four — the /st/v1 bucket, the SSDP answer
// interval, the local login and the PC notification limits).
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows at most max events per key in any sliding window of the
// given length. Keys are source addresses or "" for a single global limit.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
}

// MaxKeys is when Allow sweeps out keys that went quiet: probing sources
// come and go, and the map must not grow with every one of them.
const MaxKeys = 256

// New returns a limiter of max events per window, read against now.
func New(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, hits: map[string][]time.Time{}}
}

// Allow records one event for key, or reports how long until the oldest
// one in the window expires.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	if len(l.hits) > MaxKeys {
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false, kept[0].Sub(cutoff)
	}
	l.hits[key] = append(kept, now)
	return true, 0
}

// Reset forgets every key (tests).
func (l *Limiter) Reset() {
	l.mu.Lock()
	l.hits = map[string][]time.Time{}
	l.mu.Unlock()
}

// keys is how many keys are held (tests).
func (l *Limiter) keys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}
