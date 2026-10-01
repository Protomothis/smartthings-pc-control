package ratelimit

import (
	"testing"
	"time"
)

func TestLockoutLocksAfterMaxFailures(t *testing.T) {
	var locked []string
	l := NewLockout(3, time.Hour, func(key string) { locked = append(locked, key) })
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("attempt %d refused before the limit", i+1)
		}
		l.Fail("a")
	}
	if l.Allow("a") {
		t.Error("allowed after three failures")
	}
	if len(locked) != 1 || locked[0] != "a" {
		t.Errorf("onLock calls = %v, want [a]", locked)
	}
	if !l.Allow("b") || l.Failing("b") {
		t.Error("another key shares the count")
	}
	l.Fail("b")
	l.Reset("b")
	if l.Failing("b") {
		t.Error("a success did not clear the count")
	}
}
