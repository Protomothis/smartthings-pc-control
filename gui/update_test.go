package gui

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.3.1", "v0.3.2", true},
		{"v0.3.2", "v0.3.2", false},
		{"v0.3.2", "v0.3.1", false},
		{"v0.3.2", "v0.4.0", true},
		{"v0.3.2", "v1.0.0", true},
		{"dev", "v9.9.9", false}, // dev builds never prompt
		{"v0.3.2", "garbage", false},
		{"v0.9.9", "v0.10.0", true}, // numeric, not lexicographic
	}
	for _, c := range cases {
		if got := isNewer(c.current, c.latest); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
