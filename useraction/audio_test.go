package useraction

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

// fakeEndpoint is an in-memory IAudioEndpointVolume.
type fakeEndpoint struct {
	level   float32
	muted   bool
	device  string
	failGet error
	failSet error
	sets    []float32
	closed  bool
}

func (f *fakeEndpoint) Volume() (float32, error) { return f.level, f.failGet }
func (f *fakeEndpoint) SetVolume(l float32) error {
	if f.failSet != nil {
		return f.failSet
	}
	f.sets = append(f.sets, l)
	f.level = l
	return nil
}
func (f *fakeEndpoint) Muted() (bool, error) { return f.muted, f.failGet }
func (f *fakeEndpoint) SetMuted(on bool) error {
	if f.failSet != nil {
		return f.failSet
	}
	f.muted = on
	return nil
}
func (f *fakeEndpoint) Device() string { return f.device }
func (f *fakeEndpoint) Close()         { f.closed = true }

// withFakeAudio routes the handler to ep (or to openErr) without COM.
func withFakeAudio(t *testing.T, ep *fakeEndpoint, openErr error) {
	t.Helper()
	savedOpen, savedRun := openAudio, runCOM
	openAudio = func() (audioEndpoint, error) {
		if openErr != nil {
			return nil, openErr
		}
		return ep, nil
	}
	runCOM = func(f func() error) error { return f() }
	t.Cleanup(func() { openAudio, runCOM = savedOpen, savedRun })
}

func TestAudioVerbs(t *testing.T) {
	cases := []struct {
		args       []string
		start      float32
		startMuted bool
		want       Audio
	}{
		{[]string{"audio", "get"}, 0.3, false, Audio{Volume: 30, Device: "스피커"}},
		{[]string{"audio", "set", "42"}, 0.3, false, Audio{Volume: 42, Device: "스피커"}},
		{[]string{"audio", "set", "0"}, 0.3, true, Audio{Volume: 0, Muted: true, Device: "스피커"}},
		{[]string{"audio", "step", "+10"}, 0.3, false, Audio{Volume: 40, Device: "스피커"}},
		{[]string{"audio", "step", "-10"}, 0.3, false, Audio{Volume: 20, Device: "스피커"}},
		{[]string{"audio", "step", "5"}, 0.3, false, Audio{Volume: 35, Device: "스피커"}},
		// Clamped at both ends.
		{[]string{"audio", "step", "+20"}, 0.95, false, Audio{Volume: 100, Device: "스피커"}},
		{[]string{"audio", "step", "-50"}, 0.1, false, Audio{Volume: 0, Device: "스피커"}},
		// A level change leaves mute alone.
		{[]string{"audio", "step", "+5"}, 0.3, true, Audio{Volume: 35, Muted: true, Device: "스피커"}},
		{[]string{"audio", "mute", "on"}, 0.3, false, Audio{Volume: 30, Muted: true, Device: "스피커"}},
		{[]string{"audio", "mute", "off"}, 0.3, true, Audio{Volume: 30, Device: "스피커"}},
		{[]string{"audio", "mute", "toggle"}, 0.3, true, Audio{Volume: 30, Device: "스피커"}},
		{[]string{"audio", "mute", "toggle"}, 0.3, false, Audio{Volume: 30, Muted: true, Device: "스피커"}},
	}
	for _, c := range cases {
		ep := &fakeEndpoint{level: c.start, muted: c.startMuted, device: "스피커"}
		withFakeAudio(t, ep, nil)
		m, _, code := runMain(t, c.args...)
		if code != 0 {
			t.Errorf("%q: exit %d, %v", c.args, code, m)
			continue
		}
		a, _ := m["audio"].(map[string]any)
		got := Audio{Volume: int(a["volume"].(float64)), Muted: a["muted"].(bool), Device: a["device"].(string)}
		if got != c.want {
			t.Errorf("%q: audio = %+v, want %+v", c.args, got, c.want)
		}
		if !ep.closed {
			t.Errorf("%q: endpoint not closed", c.args)
		}
	}
}

func TestAudioErrors(t *testing.T) {
	withFakeAudio(t, nil, errNoPlaybackDevice)
	if m, _, code := runMain(t, "audio", "get"); code != 1 || m["error"] != CodeUnsupported {
		t.Errorf("no device: exit %d, %v; want unsupported", code, m)
	}
	if _, err := ReadAudio(); err == nil || !strings.Contains(err.Error(), CodeUnsupported) {
		t.Errorf("ReadAudio without a device = %v, want unsupported", err)
	}

	withFakeAudio(t, nil, &hresultError{op: "CoCreateInstance", hr: 0x80040154})
	if m, _, code := runMain(t, "audio", "get"); code != 1 || m["error"] != CodeFailed {
		t.Errorf("COM failure: exit %d, %v; want failed", code, m)
	}

	ep := &fakeEndpoint{level: 0.5, failSet: errors.New("E_ACCESSDENIED")}
	withFakeAudio(t, ep, nil)
	if m, _, code := runMain(t, "audio", "set", "10"); code != 1 || m["error"] != CodeFailed {
		t.Errorf("set failure: exit %d, %v; want failed", code, m)
	}
	if !ep.closed {
		t.Error("endpoint not closed after a failed set")
	}
}

func TestReadAudio(t *testing.T) {
	withFakeAudio(t, &fakeEndpoint{level: 0.07, muted: true, device: "헤드폰\x00"}, nil)
	a, err := ReadAudio()
	if err != nil || a != (Audio{Volume: 7, Muted: true, Device: "헤드폰"}) {
		t.Errorf("ReadAudio = %+v, %v", a, err)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("ReadAudio result does not validate: %v", err)
	}
}

func TestSanitizeDevice(t *testing.T) {
	long := strings.Repeat("가", MaxDeviceRunes+10)
	for in, want := range map[string]string{
		"스피커(Realtek(R) Audio)": "스피커(Realtek(R) Audio)",
		" 헤드폰\r\n":              "헤드폰",
		long:                    strings.Repeat("가", MaxDeviceRunes),
	} {
		got := sanitizeDevice(in)
		if got != want {
			t.Errorf("sanitizeDevice(%q) = %q, want %q", in, got, want)
		}
		if err := (Audio{Device: got}).Validate(); err != nil {
			t.Errorf("sanitized %q does not validate: %v", in, err)
		}
	}
}

func TestLevelPercent(t *testing.T) {
	for p := 0; p <= 100; p++ {
		if got := levelToPercent(percentToLevel(p)); got != p {
			t.Errorf("round trip of %d = %d", p, got)
		}
	}
	if levelToPercent(1.5) != 100 || levelToPercent(-0.2) != 0 {
		t.Error("levelToPercent does not clamp")
	}
}

// The COM structs must match the Windows ABI they are handed to.
func TestCOMLayout(t *testing.T) {
	want := uintptr(16)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = 24
	}
	if got := unsafe.Sizeof(propVariant{}); got != want {
		t.Errorf("sizeof(PROPVARIANT) = %d, want %d", got, want)
	}
	if got := unsafe.Offsetof(propVariant{}.val); got != 8 {
		t.Errorf("PROPVARIANT value offset = %d, want 8", got)
	}
	if got := unsafe.Sizeof(propertyKey{}); got != 20 {
		t.Errorf("sizeof(PROPERTYKEY) = %d, want 20", got)
	}
}
