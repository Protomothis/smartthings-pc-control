package sapi

import "testing"

func TestMatchVoice(t *testing.T) {
	names := []string{
		"Microsoft Heami Desktop - Korean",
		"Microsoft Zira Desktop - English (United States)",
		"Microsoft David Desktop - English (United States)",
	}
	for want, idx := range map[string]int{
		"":        -1,
		"   ":     -1,
		"Heami":   0,
		"heami":   0,
		"korean":  0,
		"English": 1, // the first that contains it
		"david":   2,
		"Yuna":    -1,
		" Zira ":  1,
		"Microsoft Zira Desktop - English (United States)": 1,
	} {
		if got := MatchVoice(names, want); got != idx {
			t.Errorf("MatchVoice(%q) = %d, want %d", want, got, idx)
		}
	}
	// An exact name beats an earlier partial match.
	if got := MatchVoice([]string{"Voice Two", "Voice"}, "voice"); got != 1 {
		t.Errorf("exact match = %d, want 1", got)
	}
}
