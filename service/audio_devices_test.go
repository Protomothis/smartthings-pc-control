package service

// Output and input device switching (#145): the command flow against a
// fake user session, the status slots and the device-list pushes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// fakeAudioSession answers the audio verbs of userRun.media like a user
// session with these devices would. A set moves the default flag; no test
// ever touches a real device.
type fakeAudioSession struct {
	mu      sync.Mutex
	outputs []useraction.AudioEndpoint
	inputs  []useraction.AudioEndpoint
	// refuse makes the sets fail the way the policy interface does.
	refuse bool
	calls  []string
}

func stubAudioSession(t *testing.T, outputs, inputs []useraction.AudioEndpoint) *fakeAudioSession {
	t.Helper()
	f := &fakeAudioSession{outputs: slices.Clone(outputs), inputs: slices.Clone(inputs)}
	saved := userRun.media
	userRun.media = f.run
	t.Cleanup(func() { userRun.media = saved })
	return f
}

func (f *fakeAudioSession) run(_ context.Context, args ...string) (UserActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	reply := map[string]any{"ok": true}
	switch args[1] {
	case "outputs":
		reply["outputs"] = f.outputs
	case "inputs":
		reply["inputs"] = f.inputs
	case "set-output", "set-input":
		list := &f.outputs
		key := "outputs"
		if args[1] == "set-input" {
			list, key = &f.inputs, "inputs"
		}
		if f.refuse {
			msg := "Windows did not switch the default playback device"
			return UserActionResult{}, &userActionError{Code: useraction.CodeFailed, Message: msg, Reason: useraction.AudioSwitchFailed}
		}
		for i := range *list {
			(*list)[i].Default = (*list)[i].ID == args[3]
		}
		reply[key] = *list
		// The new output plays at its own level; an input switch leaves
		// the playback state as it was.
		volume := 35
		if key == "outputs" {
			volume = 20
		}
		reply["audio"] = useraction.Audio{Volume: volume, Device: defaultEndpoint(f.outputs)}
	default:
		return UserActionResult{}, fmt.Errorf("fake session: unexpected %q", args)
	}
	raw, _ := json.Marshal(reply)
	return session.ParseOutput(raw)
}

func (f *fakeAudioSession) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func defaultEndpoint(list []useraction.AudioEndpoint) string {
	for _, e := range list {
		if e.Default {
			return e.Name
		}
	}
	return ""
}

// audioSlotsSetup is mediaSetup with the golden slots and devices.
func audioSlotsSetup(t *testing.T) *fakeAudioSession {
	t.Helper()
	cfg := mediaOn()
	cfg.Audio = goldenAudioConfig()
	mediaSetup(t, cfg)
	resetAudioDevices()
	t.Cleanup(resetAudioDevices)
	noteAudioSample(useraction.Audio{Volume: 35, Device: goldenSpeaker}, time.Now())
	noteAudioDevices(goldenOutputs(), goldenInputs(), true, true, time.Now())
	return stubAudioSession(t, goldenOutputs(), goldenInputs())
}

func TestAudioDeviceCommands(t *testing.T) {
	headset, headsetMic := goldenOutputs()[1].ID, goldenInputs()[1].ID
	cases := []struct {
		name   string
		body   string
		refuse bool
		media  bool // media.enabled off when false
		code   int
		error  string
		calls  []string
	}{
		{"output", `{"command":"audio_output","value":3}`, false, true, 200, "",
			[]string{"audio outputs", "audio set-output --id " + headset}},
		{"input", `{"command":"audio_input","value":2}`, false, true, 200, "",
			[]string{"audio inputs", "audio set-input --id " + headsetMic}},
		{"slot empty", `{"command":"audio_output","value":2}`, false, true, 404, "slot_empty", nil},
		{"input slot empty", `{"command":"audio_input","value":3}`, false, true, 404, "slot_empty", nil},
		// The TV is in slot 4 but not connected: no switch is attempted.
		{"device missing", `{"command":"audio_output","value":4}`, false, true, 409, "device_missing", []string{"audio outputs"}},
		{"refused", `{"command":"audio_output","value":3}`, true, true, 502, "switch_failed",
			[]string{"audio outputs", "audio set-output --id " + headset}},
		{"no value", `{"command":"audio_output"}`, false, true, 400, "value must be a slot 1-5", nil},
		{"slot 6", `{"command":"audio_output","value":6}`, false, true, 400, "value must be a slot 1-5", nil},
		{"media off", `{"command":"audio_output","value":3}`, false, false, 403, "media_disabled", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := audioSlotsSetup(t)
			f.refuse = c.refuse
			if !c.media {
				withConfig(t, func(cfg *Config) { cfg.Media.Enabled = false })
			}
			w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", c.body)
			if w.Code != c.code {
				t.Fatalf("code %d, want %d: %s", w.Code, c.code, w.Body.String())
			}
			body := stJSON(t, w)
			if c.error != "" && body["error"] != c.error {
				t.Errorf("error = %v, want %s", body["error"], c.error)
			}
			if got := f.Calls(); !slices.Equal(got, c.calls) {
				t.Errorf("runs = %q, want %q", got, c.calls)
			}
		})
	}
}

// After a switch the status shows the new default at once — the reply's
// audio block for outputs, the reply's list for inputs.
func TestAudioDeviceSwitchShowsInStatus(t *testing.T) {
	audioSlotsSetup(t)
	for _, body := range []string{`{"command":"audio_output","value":3}`, `{"command":"audio_input","value":2}`} {
		if w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	a := stAudioStatus(getConfig())
	if d := status.DefaultSlot(a.Outputs); d == nil || d.Slot != 3 {
		t.Errorf("outputs = %+v, want slot 3 default", a.Outputs)
	}
	if d := status.DefaultSlot(a.Inputs); d == nil || d.Slot != 2 {
		t.Errorf("inputs = %+v, want slot 2 default", a.Inputs)
	}
	if a.Device == nil || *a.Device != goldenHeadset {
		t.Errorf("device = %v, want the headset", a.Device)
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

	// set-output: the audio block and the list change together, one push.
	outputs := goldenOutputs()
	outputs[0].Default, outputs[1].Default = false, true
	raw, _ := json.Marshal(map[string]any{"ok": true, "outputs": outputs, "audio": useraction.Audio{Volume: 20, Device: goldenHeadset}})
	res, err := session.ParseOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	storeUserActionReply([]string{"audio", "set-output", "--id", outputs[1].ID}, res)
	if got := events(); len(got) != 2 {
		t.Errorf("set-output reply: events = %q, want one more audio.changed", got)
	}
}

// A reply with a list that fails validation is ignored, not stored.
func TestAudioDeviceReplyValidated(t *testing.T) {
	audioSlotsSetup(t)
	res, err := session.ParseOutput([]byte(`{"ok":true,"outputs":[{"id":"x","name":"bad\u0007name","default":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	storeUserActionReply([]string{"audio", "outputs"}, res)
	if out, _, _, _ := currentAudioDevices(); defaultEndpoint(out) != goldenSpeaker {
		t.Errorf("invalid list stored: %+v", out)
	}
	if !strings.Contains(logs.String(), "ignoring outputs") {
		t.Errorf("no log line for the ignored list: %q", logs.String())
	}
}

// Telegram /output and /input (#145): the list with ✓ and "(연결 안 됨)",
// one button per slot, a press that switches and keeps the buttons, and
// the refusals in words.
func TestTelegramAudioDevices(t *testing.T) {
	f := audioSlotsSetup(t)
	withConfig(t, func(cfg *Config) { cfg.Telegram.Lang = "ko" })
	ctx := context.Background()

	list, kb, err := tgCtl.HandleCommand(ctx, "42", "output", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓ 1 · 스피커", "3 · 헤드셋 (WH-1000XM4)", "4 · LG TV (NVIDIA High Definition Audio) (연결 안 됨)"} {
		if !strings.Contains(list, want) {
			t.Errorf("/output lacks %q:\n%s", want, list)
		}
	}
	if got := keyboardData(kb); !slices.Equal(got, []string{"out:1", "out:3", "out:4"}) {
		t.Errorf("/output buttons = %q", got)
	}

	edit, toast, err := tgCtl.HandleCallback(ctx, "42", 7, tgBody(list), "out:3")
	if err != nil || !strings.Contains(edit, "✓ 3 · 헤드셋") || !strings.Contains(toast, "헤드셋") {
		t.Errorf("out:3 = %q / %q / %v", edit, toast, err)
	}
	if got := keyboardData(tgCtl.EditKeyboard("out:3")); len(got) != 3 {
		t.Errorf("buttons after a press = %q", got)
	}
	if got := f.Calls(); len(got) != 2 {
		t.Errorf("runs = %q", got)
	}

	for _, c := range []struct{ cmd, arg, want string }{
		{"output", "4", "지금 연결되어 있지 않습니다"},
		{"output", "2", "2번 칸이 비어 있습니다"},
		{"output", "9", "사용법"},
		{"input", "2", "입력 전환: <b>헤드셋 마이크"},
	} {
		reply, _, _ := tgCtl.HandleCommand(ctx, "42", c.cmd, []string{c.arg})
		if !strings.Contains(reply, c.want) {
			t.Errorf("/%s %s = %q, want %q", c.cmd, c.arg, reply, c.want)
		}
	}

	if st, _, _ := tgCtl.HandleCommand(ctx, "42", "status", nil); !strings.Contains(st, "출력: 헤드셋 (WH-1000XM4)") {
		t.Errorf("/status lacks the output line:\n%s", st)
	}

	withConfig(t, func(cfg *Config) { cfg.Media.Enabled = false })
	if reply, kb, _ := tgCtl.HandleCommand(ctx, "42", "output", nil); !strings.Contains(reply, "미디어 제어가 꺼져") || kb != nil {
		t.Errorf("/output with media off = %q, %v", reply, kb)
	}
}
