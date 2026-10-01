package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestRateLimiterWindowAndKeys(t *testing.T) {
	now := time.Date(2026, 10, 1, 21, 0, 0, 0, time.UTC)
	l := New(2, time.Second, func() time.Time { return now })
	for i := range 2 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("event %d refused", i+1)
		}
	}
	now = now.Add(400 * time.Millisecond)
	if ok, wait := l.Allow("a"); ok || wait != 600*time.Millisecond {
		t.Errorf("third event: ok %v, retry after %v", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("keys share a limit")
	}
	now = now.Add(600 * time.Millisecond)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("still refused after the window passed")
	}

	// Quiet keys are swept once there are many.
	for i := range MaxKeys + 1 {
		l.Allow(fmt.Sprint("probe ", i))
	}
	now = now.Add(2 * time.Second)
	l.Allow("late")
	if n := l.keys(); n != 1 {
		t.Errorf("%d keys kept after the sweep, want 1", n)
	}
	l.Reset()
	if ok, _ := l.Allow("a"); !ok {
		t.Error("refused after reset")
	}
}
