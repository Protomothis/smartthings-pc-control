package devstate

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)

// Newer wins, equal times store, the first reading is a baseline.
func TestSampleNewerWins(t *testing.T) {
	var s Sample[int]
	if _, _, ok := s.Last(); ok {
		t.Fatal("a fresh sample has a reading")
	}
	if stored, changed := s.Note(30, t0); !stored || changed {
		t.Errorf("first: stored %v changed %v", stored, changed)
	}
	if stored, changed := s.Note(40, t0.Add(-time.Second)); stored || changed {
		t.Errorf("older: stored %v changed %v", stored, changed)
	}
	if stored, changed := s.Note(30, t0); !stored || changed {
		t.Errorf("same value, same time: stored %v changed %v", stored, changed)
	}
	if stored, changed := s.Note(50, t0.Add(time.Second)); !stored || !changed {
		t.Errorf("newer: stored %v changed %v", stored, changed)
	}
	if v, at, ok := s.Last(); !ok || v != 50 || !at.Equal(t0.Add(time.Second)) {
		t.Errorf("Last = %v %v %v", v, at, ok)
	}
	s.Set(10, t0) // Set ignores the order
	if v, _, _ := s.Last(); v != 10 {
		t.Errorf("after Set: %v", v)
	}
	if _, _, ok := s.Fresh(t0.Add(91*time.Second), 90*time.Second); ok {
		t.Error("a stale reading is fresh")
	}
	if v, _, ok := s.Fresh(t0.Add(90*time.Second), 90*time.Second); !ok || v != 10 {
		t.Errorf("Fresh at the edge = %v %v", v, ok)
	}
	s.Reset()
	if _, _, ok := s.Last(); ok {
		t.Error("reading survived Reset")
	}
}

func TestValueAndSessionTracker(t *testing.T) {
	d := NewValue("unknown")
	if d.Set("unknown") || !d.Set("off") || d.Get() != "off" {
		t.Error("Value change detection")
	}

	var tr SessionTracker
	if _, changed := tr.Observe(1); changed {
		t.Error("the first lookup is not a change")
	}
	if prev, changed := tr.Observe(2); !changed || prev != 1 {
		t.Errorf("1 → 2: prev %d changed %v", prev, changed)
	}
	if id, known := tr.Current(); !known || id != 2 {
		t.Errorf("Current = %d %v", id, known)
	}
	if !tr.NoteIgnored(3, 2) || tr.NoteIgnored(3, 2) || !tr.NoteIgnored(4, 2) {
		t.Error("NoteIgnored should report each new pair once")
	}
	tr.Reset()
	if _, known := tr.Current(); known {
		t.Error("known after Reset")
	}
}
