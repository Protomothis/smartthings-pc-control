package gui

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func TestSlotLine(t *testing.T) {
	connected := []string{"스피커", "헤드셋 "}
	cases := []struct {
		name  string
		known bool
		want  string
	}{
		{"", true, "(비어 있음)"},
		{"스피커", true, "스피커"},
		{"헤드셋", true, "헤드셋"},
		{"LG TV", true, "LG TV (연결 안 됨)"},
		// Before the devices were read nothing is marked.
		{"LG TV", false, "LG TV"},
	}
	for _, c := range cases {
		if got := slotLine(LangKo, c.name, connected, c.known); got != c.want {
			t.Errorf("slotLine(%q, known %v) = %q, want %q", c.name, c.known, got, c.want)
		}
	}
	s := audioSlotsFromConfig(Config{Audio: AudioConfig{Outputs: []string{" 스피커", "", "헤드셋", "4", "5", "6"}}})
	if s.Outputs != [audioSlotCount]string{"스피커", "", "헤드셋", "4", "5"} || s.Inputs != [audioSlotCount]string{} {
		t.Errorf("audioSlotsFromConfig = %+v", s)
	}
}

// resetFake answers the reset with the slots the service filled again.
type resetFake struct {
	*fakeAPI
	slots AudioConfig
	err   error
	calls int
}

func (f *resetFake) ResetAudioSlots() (AudioConfig, error) {
	f.calls++
	if f.err == nil {
		f.mu.Lock()
		f.cfg.Audio = f.slots
		f.mu.Unlock()
	}
	return f.slots, f.err
}

// The features tab lists the slots read-only; a save does not send them as
// an edit, and the reset shows the new slots without making the tab dirty.
func TestAudioSlotsInTheWindow(t *testing.T) {
	saved := readAudioDevices
	readAudioDevices = func() (_, _ []useraction.AudioEndpoint, _ error) {
		return []useraction.AudioEndpoint{{Name: "스피커", Default: true}}, []useraction.AudioEndpoint{{Name: "마이크", Default: true}}, nil
	}
	t.Cleanup(func() { readAudioDevices = saved })

	base := &fakeAPI{cfg: storedConfig()}
	base.cfg.Audio = AudioConfig{Outputs: []string{"스피커", "헤드셋"}, Inputs: []string{"마이크"}}
	svc := &resetFake{fakeAPI: base, slots: AudioConfig{Outputs: []string{"스피커"}, Inputs: []string{"마이크"}}}
	u := newTestUI(t, LangKo, svc)
	u.initialLoad()
	if u.forms.base == nil {
		t.Fatal("initialLoad did not load the config")
	}
	s := u.audioSlots
	if got := []string{s.out.labels[0].Text, s.out.labels[1].Text, s.out.labels[2].Text}; !slices.Equal(got, []string{"스피커", "헤드셋 (연결 안 됨)", "(비어 있음)"}) {
		t.Errorf("output rows = %q", got)
	}
	if h := u.features.acc.Items[featAudio].Title; h != "오디오 장치 칸 · 출력 2 · 입력 1" {
		t.Errorf("head = %q", h)
	}

	u.resetAudioSlots()
	if svc.calls != 1 || s.out.labels[1].Text != "(비어 있음)" || u.forms.base.Audio.Outputs[0] != "스피커" || len(u.forms.base.Audio.Outputs) != 1 {
		t.Errorf("after reset: calls %d, row 2 %q, base %+v", svc.calls, s.out.labels[1].Text, u.forms.base.Audio)
	}
	if h := u.features.acc.Items[featAudio].Title; h != "오디오 장치 칸 · 출력 1 · 입력 1" {
		t.Errorf("head after reset = %q", h)
	}
	if d := dirtyIndices(u.forms); d != nil {
		t.Errorf("dirty after reset: %v", d)
	}

	svc.err = errors.New("HTTP 500")
	u.resetAudioSlots()
	if s.status.Text == "" {
		t.Error("a failed reset says nothing")
	}
}

// The heartbeat carries the device names and defaults.
func TestHeartbeatAudioDevices(t *testing.T) {
	saved := heartbeatDevices
	t.Cleanup(func() { heartbeatDevices = saved })
	heartbeatDevices = func() (_, _ []useraction.AudioEndpoint, _ error) {
		return []useraction.AudioEndpoint{{Name: "스피커", Default: true}},
			[]useraction.AudioEndpoint{}, nil
	}
	b, _ := json.Marshal(sampleAudioDevices("2026-10-17T10:00:00+09:00"))
	want := `{"outputs":[{"name":"스피커","default":true}],"inputs":[],"sampled_at":"2026-10-17T10:00:00+09:00"}`
	if string(b) != want {
		t.Errorf("devices = %s\nwant      %s", b, want)
	}
}
