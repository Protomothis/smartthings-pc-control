package gui

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func TestAudioSlotsRoundTrip(t *testing.T) {
	base := Config{Audio: AudioConfig{Outputs: []string{"스피커", "", "헤드셋"}, Inputs: []string{}}}
	s := audioSlotsFromConfig(base)
	if s.dirty(base) {
		t.Error("unchanged slots read as dirty")
	}
	// Moving slot 3 up swaps it with the empty slot 2.
	s.Outputs = moveSlot(s.Outputs, 2, -1)
	if !s.dirty(base) {
		t.Error("moved slot not dirty")
	}
	cfg := base
	s.applyTo(&cfg)
	if !slices.Equal(cfg.Audio.Outputs, []string{"스피커", "헤드셋"}) {
		t.Errorf("outputs = %q", cfg.Audio.Outputs)
	}
	// Emptied lists travel as [], never null (null keeps the stored list).
	var empty audioSlotsState
	empty.applyTo(&cfg)
	b, _ := json.Marshal(cfg.Audio)
	if string(b) != `{"outputs":[],"inputs":[]}` {
		t.Errorf("emptied = %s", b)
	}
	if moveSlot(s.Outputs, 0, -1) != s.Outputs || moveSlot(s.Outputs, 4, +1) != s.Outputs {
		t.Error("moving past an end changed the slots")
	}
}

func TestSlotChoices(t *testing.T) {
	devices := []string{"스피커", "헤드셋"}
	cases := []struct {
		saved string
		want  []string
	}{
		{"", []string{"", "스피커", "헤드셋"}},
		{"헤드셋", []string{"", "스피커", "헤드셋"}},
		// A saved device that is not connected stays selectable.
		{"LG TV", []string{"", "스피커", "헤드셋", "LG TV"}},
	}
	for _, c := range cases {
		if got := slotChoices(devices, c.saved); !slices.Equal(got, c.want) {
			t.Errorf("slotChoices(%q) = %q, want %q", c.saved, got, c.want)
		}
	}
}

// The heartbeat carries the device names and defaults, never the ids.
func TestHeartbeatAudioDevices(t *testing.T) {
	saved := heartbeatDevices
	t.Cleanup(func() { heartbeatDevices = saved })
	heartbeatDevices = func() (_, _ []useraction.AudioEndpoint, _ error) {
		return []useraction.AudioEndpoint{{ID: "{0.0.0.00000000}.{a}", Name: "스피커", Default: true}},
			[]useraction.AudioEndpoint{}, nil
	}
	b, _ := json.Marshal(sampleAudioDevices("2026-10-17T10:00:00+09:00"))
	want := `{"outputs":[{"name":"스피커","default":true}],"inputs":[],"sampled_at":"2026-10-17T10:00:00+09:00"}`
	if string(b) != want {
		t.Errorf("devices = %s\nwant      %s", b, want)
	}
}
