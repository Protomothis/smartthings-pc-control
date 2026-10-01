// Package devstate holds what the service knows about the PC's devices
// between readings: the default playback device, the media session, the
// idle time, the display, the battery and which user session the
// commands act on. Every store is safe for concurrent use and knows
// nothing about HTTP, the hub or Telegram; the service decides what a
// change sets off.
package devstate

import (
	"sync"
	"time"
)

// Sample is the newest reading of one value and when it was taken.
// Readings arrive from more than one source (a heartbeat, a command's
// reply) and can race, so an older reading never overwrites a newer one.
type Sample[T comparable] struct {
	mu sync.Mutex
	v  T
	at time.Time
}

// Note stores v taken at at, unless the stored reading is newer (equal
// times store: the later call wins). stored says whether it was kept,
// changed whether that altered the value — the first reading is a
// baseline, not a change.
func (s *Sample[T]) Note(v T, at time.Time) (stored, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && at.Before(s.at) {
		return false, false
	}
	changed = !s.at.IsZero() && s.v != v
	s.v, s.at = v, at
	return true, changed
}

// Set stores v as of at whatever is stored: for a source that is the only
// writer and stamps its readings itself.
func (s *Sample[T]) Set(v T, at time.Time) {
	s.mu.Lock()
	s.v, s.at = v, at
	s.mu.Unlock()
}

// Last returns the newest reading; ok is false before the first one.
func (s *Sample[T]) Last() (v T, at time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v, s.at, !s.at.IsZero()
}

// Fresh is Last for a reading not older than ttl at now.
func (s *Sample[T]) Fresh(now time.Time, ttl time.Duration) (v T, at time.Time, ok bool) {
	v, at, ok = s.Last()
	if !ok || now.Sub(at) > ttl {
		var zero T
		return zero, time.Time{}, false
	}
	return v, at, true
}

// Reset forgets the reading.
func (s *Sample[T]) Reset() {
	s.mu.Lock()
	var zero T
	s.v, s.at = zero, time.Time{}
	s.mu.Unlock()
}

// Value is a plain last-value store: the display state, which is simply
// the last screen command the service sent.
type Value[T comparable] struct {
	mu sync.RWMutex
	v  T
}

// NewValue starts at initial.
func NewValue[T comparable](initial T) *Value[T] {
	return &Value[T]{v: initial}
}

// Set stores v and reports whether that changed the value.
func (x *Value[T]) Set(v T) (changed bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	changed = x.v != v
	x.v = v
	return changed
}

// Get returns the value.
func (x *Value[T]) Get() T {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.v
}

// SessionTracker follows the session the commands act on (0 = nobody
// logged in), so the samples of a session nothing acts on any more can be
// dropped, and remembers the last ignored heartbeat so a foreign tray app
// costs one log line, not one per post.
type SessionTracker struct {
	mu    sync.Mutex
	last  uint32
	known bool
	// ignoredFrom and ignoredFor are the source and target of the last
	// ignored heartbeat.
	ignoredFrom, ignoredFor uint32
}

// Observe records the target session id; changed is true when it differs
// from the one seen before (never on the first lookup), prev is that one.
func (t *SessionTracker) Observe(id uint32) (prev uint32, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, changed = t.last, t.known && t.last != id
	t.last, t.known = id, true
	return prev, changed
}

// Current is the session last observed; known is false before the first
// lookup.
func (t *SessionTracker) Current() (id uint32, known bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last, t.known
}

// NoteIgnored reports whether a heartbeat from session from, ignored
// because the commands act on target, is the first such pair in a row.
func (t *SessionTracker) NoteIgnored(from, target uint32) (first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	first = t.ignoredFrom != from || t.ignoredFor != target
	t.ignoredFrom, t.ignoredFor = from, target
	return first
}

// Reset forgets everything (tests).
func (t *SessionTracker) Reset() {
	t.mu.Lock()
	t.last, t.known, t.ignoredFrom, t.ignoredFor = 0, false, 0, 0
	t.mu.Unlock()
}
