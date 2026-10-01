package ratelimit

import (
	"sync"
	"time"
)

// Lockout locks a key (a source address) out for a while after too many
// failures in a row. Unlike Limiter it counts failures, not requests: the
// WebUI login and the legacy /{secret}/{command} URL share one, so a secret
// cannot be guessed at line rate on either (#120).
type Lockout struct {
	mu     sync.Mutex
	max    int
	lock   time.Duration
	onLock func(key string)
	keys   map[string]*lockoutEntry
}

type lockoutEntry struct {
	failures    int
	lockedUntil time.Time
}

// NewLockout locks a key for lock after max consecutive failures; onLock
// (may be nil) is told each time that happens.
func NewLockout(max int, lock time.Duration, onLock func(key string)) *Lockout {
	return &Lockout{max: max, lock: lock, onLock: onLock, keys: map[string]*lockoutEntry{}}
}

// Allow reports whether key may try now. A lock that has run out is lifted
// here, with its failure count.
func (l *Lockout) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.keys[key]
	if !ok {
		return true
	}
	if time.Now().Before(e.lockedUntil) {
		return false
	}
	if e.failures >= l.max {
		e.failures = 0
	}
	return true
}

// Fail counts one failure for key and locks it at the max-th.
func (l *Lockout) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.keys[key]
	if !ok {
		e = &lockoutEntry{}
		l.keys[key] = e
	}
	e.failures++
	if e.failures >= l.max {
		e.lockedUntil = time.Now().Add(l.lock)
		if l.onLock != nil {
			l.onLock(key)
		}
	}
}

// Reset forgets key's failures (a success).
func (l *Lockout) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

// Failing reports whether key has failures on record (tests).
func (l *Lockout) Failing(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.keys[key]
	return ok
}
