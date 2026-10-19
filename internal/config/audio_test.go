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
}

// A device seen for the first time takes the lowest free slot; a slotted
// one keeps its slot whether it is there or not (#159).
func TestAssignAudioSlots(t *testing.T) {
	cases := []struct {
		name        string
		names, devs []string
		want        []string
		wantChanged bool
	}{
		{"first devices, in order", nil, []string{"스피커", "헤드셋"}, []string{"스피커", "헤드셋"}, true},
		{"all slotted already", []string{"스피커", "헤드셋"}, []string{"헤드셋", " 스피커 "}, []string{"스피커", "헤드셋"}, false},
		{"case and spaces do not count", []string{"Speakers"}, []string{"SPEAKERS "}, []string{"Speakers"}, false},
		{"a gap fills first", []string{"스피커", "", "TV"}, []string{"헤드셋", "모니터"}, []string{"스피커", "헤드셋", "TV", "모니터"}, true},
		{"a gone device keeps its slot", []string{"TV", "스피커"}, []string{"스피커"}, []string{"TV", "스피커"}, false},
		{"five at most", []string{"1", "2", "3", "4"}, []string{"5", "6"}, []string{"1", "2", "3", "4", "5"}, true},
		{"full", []string{"1", "2", "3", "4", "5"}, []string{"6"}, []string{"1", "2", "3", "4", "5"}, false},
		{"empty and repeated names", []string{}, []string{"", "마이크", "마이크"}, []string{"마이크"}, true},
	}
	for _, c := range cases {
		got, changed := AssignAudioSlots(c.names, c.devs)
		if !slices.Equal(got, c.want) || changed != c.wantChanged {
			t.Errorf("%s: %q, %v; want %q, %v", c.name, got, changed, c.want, c.wantChanged)
		}
	}
	in := []string{"스피커", ""}
	AssignAudioSlots(in, []string{"헤드셋"})
	if in[1] != "" {
		t.Error("AssignAudioSlots wrote into its argument")
	}
}

// A POST cannot set the slots: they are the service's (#159). Neither an
// omitted object nor a stale or emptied one changes them.
func TestNormalizeKeepsAudioSlots(t *testing.T) {
	current := Default()
	current.Audio = AudioConfig{Outputs: []string{"스피커", "헤드셋"}, Inputs: []string{"마이크"}}
	for name, posted := range map[string]AudioConfig{
		"omitted": {},
		"emptied": {Outputs: []string{}, Inputs: []string{}},
		"stale":   {Outputs: []string{"스피커"}, Inputs: []string{}},
	} {
		cfg := current.ForUpdate()
		cfg.Audio = posted
		got := Normalize(cfg, current)
		if !slices.Equal(got.Audio.Outputs, current.Audio.Outputs) || !slices.Equal(got.Audio.Inputs, current.Audio.Inputs) {
			t.Errorf("%s: audio = %+v, want the current slots", name, got.Audio)
		}
	}
}
