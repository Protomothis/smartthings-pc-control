package config

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeAudioSlots(t *testing.T) {
	long := strings.Repeat("가", AudioSlotNameRunes+5)
	cases := []struct {
		in, want []string
	}{
		{nil, []string{}},
		{[]string{"", ""}, []string{}},
		// An empty slot in between keeps the later slots at their numbers.
		{[]string{" 스피커 ", "", "헤드셋\r\n", ""}, []string{"스피커", "", "헤드셋"}},
		{[]string{"1", "2", "3", "4", "5", "6"}, []string{"1", "2", "3", "4", "5"}},
		{[]string{long}, []string{strings.Repeat("가", AudioSlotNameRunes)}},
	}
	for _, c := range cases {
		got := NormalizeAudioSlots(c.in)
		if got == nil || !slices.Equal(got, c.want) {
			t.Errorf("NormalizeAudioSlots(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	names := []string{"스피커", "", "헤드셋"}
	for slot, want := range map[int]string{0: "", 1: "스피커", 2: "", 3: "헤드셋", 4: "", 6: ""} {
		if got := AudioSlotName(names, slot); got != want {
			t.Errorf("AudioSlotName(%d) = %q, want %q", slot, got, want)
		}
	}
}

// A POST without the audio object keeps the slots; an emptied list clears
// them (#145).
func TestNormalizeKeepsAudioSlots(t *testing.T) {
	current := Default()
	current.Audio = AudioConfig{Outputs: []string{"스피커", "헤드셋"}, Inputs: []string{"마이크"}}
	kept := Normalize(current.ForUpdate(), current)
	if !slices.Equal(kept.Audio.Outputs, current.Audio.Outputs) || !slices.Equal(kept.Audio.Inputs, current.Audio.Inputs) {
		t.Errorf("omitted audio = %+v, want the current slots", kept.Audio)
	}
	cleared := current.ForUpdate()
	cleared.Audio.Outputs = []string{}
	cleared.Audio.Inputs = current.Audio.Inputs
	got := Normalize(cleared, current)
	if len(got.Audio.Outputs) != 0 || !slices.Equal(got.Audio.Inputs, current.Audio.Inputs) {
		t.Errorf("cleared outputs = %+v", got.Audio)
	}
}
