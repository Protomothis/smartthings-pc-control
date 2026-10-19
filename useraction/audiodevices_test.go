package useraction

import (
	"errors"
	"strings"
	"testing"
)

// fakeDevices is an in-memory endpoint list.
type fakeDevices struct {
	lists   map[audioFlow][]AudioEndpoint
	listErr error
	closed  bool
}

func (f *fakeDevices) List(flow audioFlow) ([]AudioEndpoint, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]AudioEndpoint{}, f.lists[flow]...), nil
}

func (f *fakeDevices) Close() { f.closed = true }

func newFakeDevices() *fakeDevices {
	return &fakeDevices{lists: map[audioFlow][]AudioEndpoint{
		flowRender: {
			{Name: "스피커", Default: true},
			{Name: "헤드셋"},
		},
		flowCapture: {
			{Name: "마이크", Default: true},
			{Name: "헤드셋 마이크"},
		},
	}}
}

// withFakeDevices routes the device lists (and the audio block of `audio
// get`) to fakes, without COM.
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

func endpointsOf(t *testing.T, m map[string]any, key string) []AudioEndpoint {
	t.Helper()
	raw, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%s = %v, want a list", key, m[key])
	}
	var out []AudioEndpoint
	for _, r := range raw {
		e := r.(map[string]any)
		if _, leaked := e["id"]; leaked {
			t.Errorf("%s entry carries an id: %v", key, e)
		}
		out = append(out, AudioEndpoint{Name: e["name"].(string), Default: e["default"].(bool)})
	}
	return out
}

// `audio get` carries both lists next to the volume; the switching verbs
// of #145 are gone (#159).
func TestAudioGetCarriesDeviceLists(t *testing.T) {
	d := newFakeDevices()
	withFakeDevices(t, d, nil)

	m, _, code := runMain(t, "audio", "get")
	if code != 0 {
		t.Fatalf("get: exit %d, %v", code, m)
	}
	outs, ins := endpointsOf(t, m, "outputs"), endpointsOf(t, m, "inputs")
	if len(outs) != 2 || !outs[0].Default || len(ins) != 2 || ins[1].Name != "헤드셋 마이크" {
		t.Errorf("get lists: outputs %v inputs %v", outs, ins)
	}
	if !d.closed {
		t.Error("device enumerator not closed")
	}
	for _, verb := range []string{"outputs", "inputs", "set-output", "set-input"} {
		if _, _, code := runMain(t, "audio", verb, "--id", "x"); code != 1 {
			t.Errorf("audio %s: exit %d, want a refusal", verb, code)
		}
	}
}

func TestAudioGetWithoutDeviceLists(t *testing.T) {
	// get stays a volume reply when the lists cannot be read.
	withFakeDevices(t, newFakeDevices(), errors.New("no enumerator"))
	m, _, code := runMain(t, "audio", "get")
	if code != 0 || m["audio"] == nil || m["outputs"] != nil {
		t.Errorf("get without enumerator: exit %d, %v", code, m)
	}
	d := newFakeDevices()
	d.listErr = errors.New("E_FAIL")
	withFakeDevices(t, d, nil)
	m, _, code = runMain(t, "audio", "get")
	if code != 0 || m["audio"] == nil || m["inputs"] != nil {
		t.Errorf("get with a failing list: exit %d, %v", code, m)
	}
}

func TestValidateEndpoints(t *testing.T) {
	ok := []AudioEndpoint{{Name: "스피커", Default: true}, {Name: "헤드셋"}}
	if err := ValidateEndpoints(ok); err != nil {
		t.Errorf("valid list refused: %v", err)
	}
	for name, list := range map[string][]AudioEndpoint{
		"control name": {{Name: "a\x07"}},
		"long name":    {{Name: strings.Repeat("가", MaxDeviceRunes+1)}},
		"too many":     make([]AudioEndpoint, MaxAudioEndpoints+1),
	} {
		if ValidateEndpoints(list) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
