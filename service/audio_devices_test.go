package service

// Output and input device slots (#145, #159): the status slots, the
// device-list pushes, the automatic slot assignment and its reset. The
// switching commands of #145 are gone.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func defaultEndpoint(list []useraction.AudioEndpoint) string {
	for _, e := range list {
		if e.Default {
			return e.Name
		}
	}
	return ""
}

// audioSlotsSetup is mediaSetup with the golden slots and devices.
func audioSlotsSetup(t *testing.T) {
	t.Helper()
	cfg := mediaOn()
	cfg.Audio = goldenAudioConfig()
	mediaSetup(t, cfg)
	resetAudioDevices()
	t.Cleanup(resetAudioDevices)
	noteAudioSample(useraction.Audio{Volume: 35, Device: goldenSpeaker}, time.Now())
	noteAudioDevices(goldenOutputs(), goldenInputs(), true, true, time.Now())
}

// The switches of #145 are no commands any more: /st/v1/command does not
// know them, so nothing runs in the user session.
func TestAudioDeviceCommandsGone(t *testing.T) {
	audioSlotsSetup(t)
	run := stubMediaRun(t, UserActionResult{OK: true}, nil).Calls
	for _, body := range []string{`{"command":"audio_output","value":3}`, `{"command":"audio_input","value":2}`} {
		w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unknown command") {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if got := run(); len(got) != 0 {
		t.Errorf("runs = %q", got)
	}
}

func TestAudioSlotsStatus(t *testing.T) {
	slot := func(n int, name string, present, def bool) status.AudioSlot {
		return status.AudioSlot{Slot: n, Name: name, Present: present, Default: def}
	}
	speaker := "스피커 (Realtek(R) Audio)"
	cases := []struct {
		name     string
		media    bool
		session  bool
		sample   bool
		devices  bool
		outputs  []status.AudioSlot
		inputs   []status.AudioSlot
		features string
	}{
		{"everything known", true, true, true, true,
			[]status.AudioSlot{slot(1, speaker, true, true), slot(3, goldenHeadset, true, false), slot(4, goldenTV, false, false)},
			[]status.AudioSlot{slot(1, goldenMic, true, true), slot(2, goldenHeadsetMic, true, false)},
			"[awake audio media audio_output audio_input notify presets]"},
		// No list yet: only the device the audio sample names is present.
		{"no device list", true, true, true, false,
			[]status.AudioSlot{slot(1, speaker, true, true), slot(3, goldenHeadset, false, false), slot(4, goldenTV, false, false)},
			[]status.AudioSlot{slot(1, goldenMic, false, false), slot(2, goldenHeadsetMic, false, false)},
			"[awake audio media audio_output audio_input notify presets]"},
		{"nobody logged in", true, false, true, true,
			[]status.AudioSlot{slot(1, speaker, false, false), slot(3, goldenHeadset, false, false), slot(4, goldenTV, false, false)},
			[]status.AudioSlot{slot(1, goldenMic, false, false), slot(2, goldenHeadsetMic, false, false)},
			"[awake audio media audio_output audio_input notify presets]"},
		{"media off", false, true, true, true, []status.AudioSlot{}, []status.AudioSlot{}, "[awake notify presets]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := mediaOn()
			cfg.Media.Enabled = c.media
			cfg.Audio = goldenAudioConfig()
			// The slot names differ in case and spacing from the devices.
			cfg.Audio.Outputs[0] = " " + strings.ToUpper(speaker) + " "
			mediaSetup(t, cfg)
			resetAudioDevices()
			t.Cleanup(resetAudioDevices)
			sys.sessionPresent = func() bool { return c.session }
			if c.sample {
				noteAudioSample(useraction.Audio{Volume: 35, Device: speaker}, time.Now())
			}
			if c.devices {
				noteAudioDevices(goldenOutputs(), goldenInputs(), true, true, time.Now())
			}
			var got struct {
				Audio    stAudio  `json:"audio"`
				Features []string `json:"features"`
			}
			if err := json.Unmarshal(stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", "").Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			// The upper-cased name is reported as configured.
			for i := range got.Audio.Outputs {
				if got.Audio.Outputs[i].Slot == 1 {
					got.Audio.Outputs[i].Name = speaker
				}
			}
			if !slices.Equal(got.Audio.Outputs, c.outputs) {
				t.Errorf("outputs = %+v\nwant      %+v", got.Audio.Outputs, c.outputs)
			}
			if !slices.Equal(got.Audio.Inputs, c.inputs) {
				t.Errorf("inputs = %+v\nwant     %+v", got.Audio.Inputs, c.inputs)
			}
			if fmt.Sprint(got.Features) != c.features {
				t.Errorf("features = %v, want %s", got.Features, c.features)
			}
		})
	}
}

// A device seen for the first time takes the lowest free slot, the default
// one first; a device that goes away keeps its slot (present:false). Each
// assignment is saved and pushes audio.changed (#159).
func TestAudioSlotsAssigned(t *testing.T) {
	mediaSetup(t, mediaOn())
	resetAudioDevices()
	t.Cleanup(resetAudioDevices)
	t.Cleanup(dev.tray.Reset)
	events := tapDeviceEvents(t)

	// The headset is listed first but the speaker is the default.
	outputs := []useraction.AudioEndpoint{{Name: goldenHeadset}, {Name: goldenSpeaker, Default: true}}
	heartbeat.AudioDevices(outputs, goldenInputs(), time.Now())
	a := getConfig().Audio
	if !slices.Equal(a.Outputs, []string{goldenSpeaker, goldenHeadset}) || !slices.Equal(a.Inputs, []string{goldenMic, goldenHeadsetMic}) {
		t.Fatalf("first lists: slots %+v", a)
	}
	if got := events(); len(got) != 1 || got[0] != "audio.changed" {
		t.Errorf("first assignment: events = %q, want one audio.changed", got)
	}

	// The headset goes, a TV comes: the TV takes slot 3, the headset keeps 2.
	later := []useraction.AudioEndpoint{{Name: goldenSpeaker, Default: true}, {Name: goldenTV}}
	heartbeat.AudioDevices(later, goldenInputs(), time.Now())
	noteAudioSample(useraction.Audio{Volume: 35, Device: goldenSpeaker}, time.Now())
	if got := getConfig().Audio.Outputs; !slices.Equal(got, []string{goldenSpeaker, goldenHeadset, goldenTV}) {
		t.Errorf("after the TV: outputs %q", got)
	}
	st := stAudioStatus(getConfig())
	if len(st.Outputs) != 3 || st.Outputs[1].Present || !st.Outputs[2].Present || !st.Outputs[0].Default {
		t.Errorf("status outputs = %+v", st.Outputs)
	}

	// Media control off: nothing is assigned (no audio block to report in).
	withConfig(t, func(c *Config) { c.Media.Enabled = false })
	heartbeat.AudioDevices([]useraction.AudioEndpoint{{Name: "USB 스피커", Default: true}}, goldenInputs(), time.Now())
	if got := getConfig().Audio.Outputs; len(got) != 3 {
		t.Errorf("assigned with media off: %q", got)
	}
}

// The real save: assigned slots land in config.json.
func TestAudioSlotsSaved(t *testing.T) {
	mediaSetup(t, mediaOn())
	resetAudioDevices()
	t.Cleanup(resetAudioDevices)
	saved := saveSlots
	saveSlots = saveConfig
	t.Cleanup(func() { saveSlots = saved })
	path := filepath.Join(configDir(), "config.json")
	orig, origErr := os.ReadFile(path)
	t.Cleanup(func() {
		if origErr == nil {
			os.WriteFile(path, orig, 0o644)
		} else {
			os.Remove(path)
		}
	})

	noteAudioDevices(goldenOutputs(), goldenInputs(), true, true, time.Now())
	if got := loadConfig().Audio; !slices.Equal(got.Outputs, []string{goldenSpeaker, goldenHeadset}) || !slices.Equal(got.Inputs, []string{goldenMic, goldenHeadsetMic}) {
		t.Errorf("config.json slots = %+v", got)
	}
}

// The app's reset empties both lists and lets the devices connected now
// fill them again; a device that is gone loses its slot.
func TestAudioSlotsReset(t *testing.T) {
	audioSlotsSetup(t)
	events := tapDeviceEvents(t)
	got, err := resetAudioSlots()
	if err != nil {
		t.Fatal(err)
	}
	// The TV of slot 4 is not connected; the gap of slot 2 closes.
	want := config.AudioConfig{Outputs: []string{goldenSpeaker, goldenHeadset}, Inputs: []string{goldenMic, goldenHeadsetMic}}
	if !slices.Equal(got.Outputs, want.Outputs) || !slices.Equal(got.Inputs, want.Inputs) {
		t.Errorf("reset = %+v, want %+v", got, want)
	}
	if a := getConfig().Audio; !slices.Equal(a.Outputs, want.Outputs) {
		t.Errorf("live config after reset = %+v", a)
	}
	if ev := events(); len(ev) != 1 || ev[0] != "audio.changed" {
		t.Errorf("reset: events = %q, want one audio.changed", ev)
	}

	// Without a device list the slots stay empty until one arrives.
	resetAudioDevices()
	if got, _ := resetAudioSlots(); len(got.Outputs)+len(got.Inputs) != 0 {
		t.Errorf("reset without lists = %+v", got)
	}
	noteAudioDevices(goldenOutputs(), nil, true, false, time.Now())
	if a := getConfig().Audio; !slices.Equal(a.Outputs, want.Outputs) || len(a.Inputs) != 0 {
		t.Errorf("after the next list = %+v", a)
	}
}

// A device list that differs from the last one pushes audio.changed; the
// first one is a baseline, an equal one is no news, and a reply whose
// audio block already pushed does not push twice.
func TestAudioDeviceListPushes(t *testing.T) {
	audioSlotsSetup(t)
	resetAudioDevices()
	t.Cleanup(dev.tray.Reset)
	events := tapDeviceEvents(t)

	heartbeat.AudioDevices(goldenOutputs(), goldenInputs(), time.Now())
	heartbeat.AudioDevices(goldenOutputs(), goldenInputs(), time.Now())
	if got := events(); len(got) != 0 {
		t.Fatalf("baseline and repeat pushed %q", got)
	}
	inputs := goldenInputs()
	inputs[0].Default, inputs[1].Default = false, true
	heartbeat.AudioDevices(goldenOutputs(), inputs, time.Now())
	if got := events(); len(got) != 1 || got[0] != "audio.changed" {
		t.Fatalf("default input switched: events = %q, want one audio.changed", got)
	}
	// An older reading never overwrites a newer one.
	heartbeat.AudioDevices(goldenOutputs(), goldenInputs(), time.Now().Add(-time.Hour))
	if _, in, _, _ := currentAudioDevices(); defaultEndpoint(in) != goldenHeadsetMic {
		t.Errorf("older heartbeat overwrote the list: %+v", in)
	}

	// `audio get` after a switch in Windows: the audio block and the list
	// change together, one push.
	outputs := goldenOutputs()
	outputs[0].Default, outputs[1].Default = false, true
	raw, _ := json.Marshal(map[string]any{"ok": true, "outputs": outputs, "audio": useraction.Audio{Volume: 20, Device: goldenHeadset}})
	res, err := session.ParseOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	storeUserActionReply([]string{"audio", "get"}, res)
	if got := events(); len(got) != 2 {
		t.Errorf("audio get reply: events = %q, want one more audio.changed", got)
	}
}

// A reply with a list that fails validation is ignored, not stored.
func TestAudioDeviceReplyValidated(t *testing.T) {
	audioSlotsSetup(t)
	res, err := session.ParseOutput([]byte(`{"ok":true,"outputs":[{"name":"bad\u0007name","default":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	storeUserActionReply([]string{"audio", "get"}, res)
	if out, _, _, _ := currentAudioDevices(); defaultEndpoint(out) != goldenSpeaker {
		t.Errorf("invalid list stored: %+v", out)
	}
	if !strings.Contains(logs.String(), "ignoring outputs") {
		t.Errorf("no log line for the ignored list: %q", logs.String())
	}
}

// Telegram has no /output or /input any more (#159); /status still names
// the default output.
func TestTelegramAudioDevicesGone(t *testing.T) {
	audioSlotsSetup(t)
	withConfig(t, func(cfg *Config) { cfg.Telegram.Lang = "ko" })
	ctx := context.Background()
	for _, cmd := range []string{"output", "input"} {
		if reply, kb, _ := tgCtl.HandleCommand(ctx, "42", cmd, nil); strings.Contains(reply, "출력 장치") || kb != nil {
			t.Errorf("/%s = %q, %v", cmd, reply, kb)
		}
	}
	if st, _, _ := tgCtl.HandleCommand(ctx, "42", "status", nil); !strings.Contains(st, "출력: "+goldenSpeaker) {
		t.Errorf("/status lacks the output line:\n%s", st)
	}
}
