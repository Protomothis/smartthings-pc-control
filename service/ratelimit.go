package service

import (
	"sync"
	"time"
)

// rateLimiter allows at most max events per key in any sliding window of
// the given length (#127: one type for what were four — the /st/v1 bucket,
// the SSDP answer interval, the local login and the PC notification
// limits). Keys are source addresses or "" for a single global limit.
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
}

// maxLimiterKeys is when allow sweeps out keys that went quiet: probing
// sources come and go, and the map must not grow with every one of them.
const maxLimiterKeys = 256

func newRateLimiter(max int, window time.Duration, now func() time.Time) *rateLimiter {
	return &rateLimiter{max: max, window: window, now: now, hits: map[string][]time.Time{}}
}

// allow records one event for key, or reports how long until the oldest
// one in the window expires.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	if len(l.hits) > maxLimiterKeys {
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

// reset forgets every key (tests).
func (l *rateLimiter) reset() {
	l.mu.Lock()
	l.hits = map[string][]time.Time{}
	l.mu.Unlock()
}
