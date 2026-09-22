package gui

import "testing"

// TestIdleSecondsFrom covers the tick arithmetic behind the heartbeat
// (#77), including the ~49.7-day wrap of the 32-bit millisecond counters
// GetLastInputInfo and GetTickCount share.
func TestIdleSecondsFrom(t *testing.T) {
	const wrap = uint32(0xFFFFFFFF) // last tick before the counter wraps

	cases := []struct {
		name           string
		lastInput, now uint32
		want           int64
	}{
		{"just typed", 1_000_000, 1_000_000, 0},
		{"sub-second rounds down", 1_000_000, 1_000_999, 0},
		{"136 seconds", 1_000_000, 1_136_000, 136},
		{"idle for an hour", 0, 3_600_000, 3600},
		// The tick counter wrapped between the last input and now: the
		// uint32 subtraction must yield 9s, not ~49.7 days.
		{"wrapped", wrap - 4_000, 5_000, 9},
		{"wrapped exactly at zero", wrap - 2_999, 0, 3},
	}
	for _, tc := range cases {
		if got := idleSecondsFrom(tc.lastInput, tc.now); got != tc.want {
			t.Errorf("%s: idleSecondsFrom(%d, %d) = %d, want %d", tc.name, tc.lastInput, tc.now, got, tc.want)
		}
	}
}
