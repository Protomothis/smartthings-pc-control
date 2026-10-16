package useraction

import (
	"errors"
	"strings"
	"testing"
)

// fakeDevices is an in-memory endpoint list. SetDefault only moves the
// default flag: no test ever touches this machine's default device.
type fakeDevices struct {
	lists   map[audioFlow][]AudioEndpoint
	setErr  error
	listErr error
	sets    []string
	closed  bool
}

func (f *fakeDevices) List(flow audioFlow) ([]AudioEndpoint, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]AudioEndpoint{}, f.lists[flow]...), nil
}

func (f *fakeDevices) SetDefault(id string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, id)
	for flow, list := range f.lists {
		owns := false
		for _, e := range list {
			owns = owns || e.ID == id
		}
		if !owns {
			continue
		}
		for i := range list {
			list[i].Default = list[i].ID == id
		}
		f.lists[flow] = list
	}
	return nil
}

func (f *fakeDevices) Close() { f.closed = true }

func newFakeDevices() *fakeDevices {
	return &fakeDevices{lists: map[audioFlow][]AudioEndpoint{
		flowRender: {
			{ID: "{0.0.0.00000000}.{spk}", Name: "스피커", Default: true},
			{ID: "{0.0.0.00000000}.{hs}", Name: "헤드셋"},
		},
		flowCapture: {
			{ID: "{0.0.1.00000000}.{mic}", Name: "마이크", Default: true},
			{ID: "{0.0.1.00000000}.{hsmic}", Name: "헤드셋 마이크"},
		},
	}}
}

// withFakeDevices routes the device verbs (and the audio block of a set
// reply) to fakes, without COM.
func withFakeDevices(t *testing.T, d *fakeDevices, openErr error) {
	t.Helper()
	withFakeAudio(t, &fakeEndpoint{level: 0.4, device: "헤드셋"}, nil)
	saved := openAudioDevices
	openAudioDevices = func() (audioDevices, error) {
		if openErr != nil {
			return nil, openErr
		}
		return d, nil
	}
	t.Cleanup(func() { openAudioDevices = saved })
}

func TestParseAudioDevice(t *testing.T) {
	long := strings.Repeat("x", MaxEndpointIDRunes+1)
	cases := []struct {
		args []string
		ok   bool
		id   string
	}{
		{[]string{"audio", "outputs"}, true, ""},
		{[]string{"audio", "inputs"}, true, ""},
		{[]string{"audio", "set-output", "--id", "{0.0.0.00000000}.{a}"}, true, "{0.0.0.00000000}.{a}"},
		{[]string{"audio", "set-input", "--id", "{0.0.1.00000000}.{b}"}, true, "{0.0.1.00000000}.{b}"},
		{[]string{"audio", "outputs", "x"}, false, ""},
		{[]string{"audio", "set-output"}, false, ""},
		{[]string{"audio", "set-output", "--id"}, false, ""},
		{[]string{"audio", "set-output", "--id", " "}, false, ""},
		{[]string{"audio", "set-output", "--id", "a\nb"}, false, ""},
		{[]string{"audio", "set-output", "--id", long}, false, ""},
		{[]string{"audio", "set-output", "--id", "a", "--id", "b"}, false, ""},
		{[]string{"audio", "set-output", "--name", "스피커"}, false, ""},
	}
	for _, c := range cases {
		req, err := Parse(c.args)
		if (err == nil) != c.ok {
			t.Errorf("Parse(%q) err = %v, want ok=%v", c.args, err, c.ok)
			continue
		}
		if c.ok && req.DeviceID != c.id {
			t.Errorf("Parse(%q).DeviceID = %q, want %q", c.args, req.DeviceID, c.id)
		}
	}
}

func endpointsOf(t *testing.T, m map[string]any, key string) []AudioEndpoint {
	t.Helper()
	raw, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%s = %v, want a list", key, m[key])
	}
	var out []AudioEndpoint
	for _, r := range raw {
		e := r.(map[string]any)
		out = append(out, AudioEndpoint{ID: e["id"].(string), Name: e["name"].(string), Default: e["default"].(bool)})
	}
	return out
}

func defaultName(list []AudioEndpoint) string {
	for _, e := range list {
		if e.Default {
			return e.Name
		}
	}
	return ""
}

func TestAudioDeviceVerbs(t *testing.T) {
	d := newFakeDevices()
	withFakeDevices(t, d, nil)

	m, _, code := runMain(t, "audio", "outputs")
	if code != 0 || len(endpointsOf(t, m, "outputs")) != 2 || m["inputs"] != nil {
		t.Fatalf("outputs: exit %d, %v", code, m)
	}
	m, _, code = runMain(t, "audio", "inputs")
	if code != 0 || defaultName(endpointsOf(t, m, "inputs")) != "마이크" {
		t.Fatalf("inputs: exit %d, %v", code, m)
	}

	// A switch reads the list back and adds the playback state.
	m, _, code = runMain(t, "audio", "set-output", "--id", "{0.0.0.00000000}.{hs}")
	if code != 0 {
		t.Fatalf("set-output: exit %d, %v", code, m)
	}
	if got := defaultName(endpointsOf(t, m, "outputs")); got != "헤드셋" {
		t.Errorf("default after set-output = %q, want 헤드셋", got)
	}
	if a, _ := m["audio"].(map[string]any); a["device"] != "헤드셋" {
		t.Errorf("set-output audio block = %v", m["audio"])
	}
	m, _, code = runMain(t, "audio", "set-input", "--id", "{0.0.1.00000000}.{hsmic}")
	if code != 0 || defaultName(endpointsOf(t, m, "inputs")) != "헤드셋 마이크" {
		t.Errorf("set-input: exit %d, %v", code, m)
	}
	if want := []string{"{0.0.0.00000000}.{hs}", "{0.0.1.00000000}.{hsmic}"}; strings.Join(d.sets, ",") != strings.Join(want, ",") {
		t.Errorf("SetDefault calls = %v, want %v", d.sets, want)
	}
	if !d.closed {
		t.Error("device enumerator not closed")
	}

	// get carries both lists next to the volume.
	m, _, code = runMain(t, "audio", "get")
	if code != 0 || len(endpointsOf(t, m, "outputs")) != 2 || len(endpointsOf(t, m, "inputs")) != 2 {
		t.Errorf("get: exit %d, %v", code, m)
	}
}

func TestAudioDeviceRefusals(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		setErr  error
		listErr error
		code    string
		reason  string
	}{
		// An id of the other direction is as missing as an unplugged one.
		{"missing", []string{"audio", "set-output", "--id", "{0.0.0.00000000}.{gone}"}, nil, nil, CodeFailed, AudioDeviceMissing},
		{"other flow", []string{"audio", "set-output", "--id", "{0.0.1.00000000}.{mic}"}, nil, nil, CodeFailed, AudioDeviceMissing},
		{"refused", []string{"audio", "set-input", "--id", "{0.0.1.00000000}.{hsmic}"}, &hresultError{op: "SetDefaultEndpoint", hr: 0x80070005}, nil, CodeFailed, AudioSwitchFailed},
		{"list fails", []string{"audio", "outputs"}, nil, errors.New("E_FAIL"), CodeFailed, ""},
	}
	for _, c := range cases {
		d := newFakeDevices()
		d.setErr, d.listErr = c.setErr, c.listErr
		withFakeDevices(t, d, nil)
		m, _, code := runMain(t, c.args...)
		if code != 1 || m["error"] != c.code || (c.reason != "" && m["reason"] != c.reason) {
			t.Errorf("%s: exit %d, %v; want %s/%s", c.name, code, m, c.code, c.reason)
		}
		if c.reason == AudioDeviceMissing && len(d.sets) != 0 {
			t.Errorf("%s: SetDefault was called for an id not in the list", c.name)
		}
	}

	// get stays a volume reply when the lists cannot be read.
	withFakeDevices(t, newFakeDevices(), errors.New("no enumerator"))
	m, _, code := runMain(t, "audio", "get")
	if code != 0 || m["audio"] == nil || m["outputs"] != nil {
		t.Errorf("get without lists: exit %d, %v", code, m)
	}
}

func TestValidateEndpoints(t *testing.T) {
	ok := []AudioEndpoint{{ID: "{a}", Name: "스피커", Default: true}, {Name: "헤드셋"}}
	if err := ValidateEndpoints(ok); err != nil {
		t.Errorf("valid list refused: %v", err)
	}
	for name, list := range map[string][]AudioEndpoint{
		"control name": {{Name: "a\x07"}},
		"long name":    {{Name: strings.Repeat("가", MaxDeviceRunes+1)}},
		"control id":   {{ID: "a\nb", Name: "x"}},
		"too many":     make([]AudioEndpoint, MaxAudioEndpoints+1),
	} {
		if ValidateEndpoints(list) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
