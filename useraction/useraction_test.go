package useraction

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	cases := []struct {
		args []string
		want Request
	}{
		{[]string{"audio", "get"}, Request{Action: "audio", Verb: "get"}},
		{[]string{"audio", "set", "0"}, Request{Action: "audio", Verb: "set", Value: 0}},
		{[]string{"audio", "set", "30"}, Request{Action: "audio", Verb: "set", Value: 30}},
		{[]string{"audio", "set", "100"}, Request{Action: "audio", Verb: "set", Value: 100}},
		{[]string{"audio", "step", "10"}, Request{Action: "audio", Verb: "step", Value: 10}},
		{[]string{"audio", "step", "+10"}, Request{Action: "audio", Verb: "step", Value: 10}},
		{[]string{"audio", "step", "-5"}, Request{Action: "audio", Verb: "step", Value: -5}},
		{[]string{"audio", "step", "-100"}, Request{Action: "audio", Verb: "step", Value: -100}},
		{[]string{"audio", "step", "100"}, Request{Action: "audio", Verb: "step", Value: 100}},
		{[]string{"audio", "mute", "on"}, Request{Action: "audio", Verb: "mute", Mode: "on"}},
		{[]string{"audio", "mute", "off"}, Request{Action: "audio", Verb: "mute", Mode: "off"}},
		{[]string{"audio", "mute", "toggle"}, Request{Action: "audio", Verb: "mute", Mode: "toggle"}},
		{[]string{"media", "playpause"}, Request{Action: "media", Verb: "playpause"}},
		{[]string{"media", "play"}, Request{Action: "media", Verb: "play"}},
		{[]string{"media", "pause"}, Request{Action: "media", Verb: "pause"}},
		{[]string{"media", "stop"}, Request{Action: "media", Verb: "stop"}},
		{[]string{"media", "next"}, Request{Action: "media", Verb: "next"}},
		{[]string{"media", "prev"}, Request{Action: "media", Verb: "prev"}},
		{[]string{"media", "info"}, Request{Action: "media", Verb: "info"}},
		{[]string{"notify", "--title", "SmartThings", "--text", "빨래 끝"},
			Request{Action: "notify", Title: "SmartThings", Text: "빨래 끝"}},
		{[]string{"notify", "--text", "hi", "--title", ""},
			Request{Action: "notify", Title: "", Text: "hi"}},
		// A value is the next argument taken literally, even one that
		// looks like a flag.
		{[]string{"notify", "--title", "--text", "--text", "--title"},
			Request{Action: "notify", Title: "--text", Text: "--title"}},
		{[]string{"notify", "--title", "t", "--text", strings.Repeat("가", MaxTextRunes)},
			Request{Action: "notify", Title: "t", Text: strings.Repeat("가", MaxTextRunes)}},
		{[]string{"preset", "--type", "program", "--path", `C:\Games\Steam\steam.exe`},
			Request{Action: "preset", PresetType: "program", Path: `C:\Games\Steam\steam.exe`}},
		{[]string{"preset", "--type", "program", "--path", `C:\a b\x.exe`, "--arg", "-silent", "--arg", "two words", "--arg", ""},
			Request{Action: "preset", PresetType: "program", Path: `C:\a b\x.exe`, Args: []string{"-silent", "two words", ""}}},
		{[]string{"preset", "--path", "https://example.com/x?y=1", "--type", "url"},
			Request{Action: "preset", PresetType: "url", Path: "https://example.com/x?y=1"}},
		{[]string{"preset", "--type", "url", "--path", "http://192.168.1.50:8080/"},
			Request{Action: "preset", PresetType: "url", Path: "http://192.168.1.50:8080/"}},
		{[]string{"preset", "--type", "script", "--path", `C:\s\go.PS1`, "--arg", "x"},
			Request{Action: "preset", PresetType: "script", Path: `C:\s\go.PS1`, Args: []string{"x"}}},
		{[]string{"preset", "--type", "script", "--path", `C:\s\go.bat`},
			Request{Action: "preset", PresetType: "script", Path: `C:\s\go.bat`}},
		{[]string{"preset", "--type", "script", "--path", `C:\s\go.cmd`},
			Request{Action: "preset", PresetType: "script", Path: `C:\s\go.cmd`}},
	}
	for _, c := range cases {
		got, err := Parse(c.args)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %+v, want %+v", c.args, got, c.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"volume"},
		{"AUDIO", "get"},
		// audio
		{"audio"},
		{"audio", "get", "x"},
		{"audio", "Get"},
		{"audio", "louder"},
		{"audio", "set"},
		{"audio", "set", "101"},
		{"audio", "set", "-1"},
		{"audio", "set", "+5"},
		{"audio", "set", "5.5"},
		{"audio", "set", "abc"},
		{"audio", "set", ""},
		{"audio", "set", " 5"},
		{"audio", "set", "0005"},
		{"audio", "set", "5", "6"},
		{"audio", "step"},
		{"audio", "step", "101"},
		{"audio", "step", "-101"},
		{"audio", "step", "+"},
		{"audio", "step", "--5"},
		{"audio", "step", "1e2"},
		{"audio", "mute"},
		{"audio", "mute", "ON"},
		{"audio", "mute", "yes"},
		{"audio", "mute", "on", "off"},
		// media
		{"media"},
		{"media", "PLAY"},
		{"media", "rewind"},
		{"media", "play", "pause"},
		{"media", "info", "now"},
		{"media", "INFO"},
		// notify
		{"notify"},
		{"notify", "--text", "hi"},
		{"notify", "--title", "t"},
		{"notify", "--title", "t", "--text"},
		{"notify", "--title", "t", "--text", ""},
		{"notify", "--title", "t", "--text", "   "},
		{"notify", "--title", "t", "--text", "hi", "--text", "again"},
		{"notify", "--title", "t", "--text", "hi", "--loud"},
		{"notify", "--title", "t", "--text", "hi", "extra"},
		{"notify", "--title=t", "--text", "hi"},
		{"notify", "--title", "t", "--text", strings.Repeat("a", MaxTextRunes+1)},
		{"notify", "--title", strings.Repeat("a", MaxTitleRunes+1), "--text", "hi"},
		{"notify", "--title", "t", "--text", "line1\nline2"},
		{"notify", "--title", "t\x00", "--text", "hi"},
		{"notify", "--title", "t", "--text", "hi\x1b[2J"},
		{"notify", "--title", "t", "--text", "\xff\xfe"},
		// preset
		{"preset"},
		{"preset", "--type", "program"},
		{"preset", "--path", `C:\x.exe`},
		{"preset", "--type", "program", "--path", ""},
		{"preset", "--type", "program", "--path", "  "},
		{"preset", "--type", "program", "--path", `C:\x.exe`, "--type", "url"},
		{"preset", "--type", "shell", "--path", `C:\x.exe`},
		{"preset", "--type", "PROGRAM", "--path", `C:\x.exe`},
		{"preset", "--type", "program", "--path", "C:\\x.exe\x00evil"},
		{"preset", "--type", "program", "--path", `C:\x.exe`, "--arg"},
		{"preset", "--type", "program", "--path", `C:\x.exe`, "--arg", "a\r\nb"},
		{"preset", "--type", "program", "--path", `C:\x.exe`, "--args", "a"},
		{"preset", "--type", "program", "--path", `C:\x.exe`, strings.Repeat("--arg\x00", 1)},
		{"preset", "--type", "url", "--path", "file:///C:/Windows/System32/cmd.exe"},
		{"preset", "--type", "url", "--path", "javascript:alert(1)"},
		{"preset", "--type", "url", "--path", "https://"},
		{"preset", "--type", "url", "--path", "example.com"},
		{"preset", "--type", "url", "--path", "https://example.com", "--arg", "x"},
		{"preset", "--type", "script", "--path", `C:\s\go.exe`},
		{"preset", "--type", "script", "--path", `C:\s\go.vbs`},
		{"preset", "--type", "script", "--path", `C:\s\go`},
	}
	tooMany := []string{"preset", "--type", "program", "--path", `C:\x.exe`}
	for i := 0; i <= MaxPresetArgs; i++ {
		tooMany = append(tooMany, "--arg", "a")
	}
	cases = append(cases, tooMany)

	for _, args := range cases {
		_, err := Parse(args)
		var ue *Error
		if !errors.As(err, &ue) || ue.Code != CodeBadArgs {
			t.Errorf("Parse(%q) = %v, want a bad_args error", args, err)
		}
	}
}

// withHandlers swaps the registry for the duration of a test.
func withHandlers(t *testing.T, hs map[string]Handler) {
	t.Helper()
	handlersMu.Lock()
	saved := handlers
	handlers = hs
	handlersMu.Unlock()
	t.Cleanup(func() {
		handlersMu.Lock()
		handlers = saved
		handlersMu.Unlock()
	})
}

// runMain runs the subcommand and checks the one-line contract.
func runMain(t *testing.T, args ...string) (map[string]any, string, int) {
	t.Helper()
	var out bytes.Buffer
	code := Main(args, &out)
	s := out.String()
	if !strings.HasSuffix(s, "\n") || strings.Count(s, "\n") != 1 {
		t.Fatalf("Main(%q) wrote %q, want exactly one line", args, s)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("Main(%q) wrote %q: %v", args, s, err)
	}
	return m, strings.TrimSuffix(s, "\n"), code
}

func TestMainUnregisteredIsUnsupported(t *testing.T) {
	withHandlers(t, map[string]Handler{})
	for _, args := range [][]string{
		{"audio", "get"},
		{"media", "next"},
		{"notify", "--title", "t", "--text", "x"},
		{"preset", "--type", "url", "--path", "https://example.com"},
	} {
		m, _, code := runMain(t, args...)
		if code != 1 || m["ok"] != false || m["error"] != CodeUnsupported {
			t.Errorf("%q: exit %d, %v; want exit 1 unsupported", args, code, m)
		}
		if msg, _ := m["message"].(string); msg == "" {
			t.Errorf("%q: empty message", args)
		}
	}
}

func TestMainBadArgs(t *testing.T) {
	withHandlers(t, map[string]Handler{})
	m, _, code := runMain(t, "audio", "set", "200")
	if code != 1 || m["ok"] != false || m["error"] != CodeBadArgs {
		t.Errorf("exit %d, %v; want exit 1 bad_args", code, m)
	}
	if len(m) != 3 {
		t.Errorf("error line has keys %v, want ok, error, message", m)
	}
}

func TestMainOKShape(t *testing.T) {
	var got Request
	withHandlers(t, map[string]Handler{
		ActionAudio: func(req Request) (map[string]any, error) {
			got = req
			return map[string]any{
				"ok":    false, // ignored: the outcome is the error value
				"audio": Audio{Volume: 30, Muted: false, Device: "스피커 <Realtek>"},
			}, nil
		},
	})
	m, line, code := runMain(t, "audio", "set", "30")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got.Verb != "set" || got.Value != 30 {
		t.Errorf("handler got %+v", got)
	}
	if !strings.HasPrefix(line, `{"ok":true,`) {
		t.Errorf("line %s does not start with ok:true", line)
	}
	want := map[string]any{"ok": true, "audio": map[string]any{"volume": float64(30), "muted": false, "device": "스피커 <Realtek>"}}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("line = %v, want %v", m, want)
	}

	// No fields at all is still a complete line.
	withHandlers(t, map[string]Handler{ActionMedia: func(Request) (map[string]any, error) { return nil, nil }})
	if _, line, code := runMain(t, "media", "next"); code != 0 || line != `{"ok":true}` {
		t.Errorf("empty result: exit %d, %s", code, line)
	}
}

func TestMainHandlerErrors(t *testing.T) {
	withHandlers(t, map[string]Handler{
		ActionAudio: func(Request) (map[string]any, error) { return nil, Unsupported("no playback device") },
		ActionMedia: func(Request) (map[string]any, error) {
			return nil, errors.New("SendInput: access denied\r\nsecond line")
		},
		ActionNotify: func(Request) (map[string]any, error) { panic("boom") },
		ActionPreset: func(Request) (map[string]any, error) {
			return map[string]any{"bad": make(chan int)}, nil
		},
	})
	cases := []struct {
		args []string
		code string
	}{
		{[]string{"audio", "get"}, CodeUnsupported},
		{[]string{"media", "play"}, CodeFailed},
		{[]string{"notify", "--title", "t", "--text", "x"}, CodeFailed},
		{[]string{"preset", "--type", "program", "--path", `C:\x.exe`}, CodeFailed},
	}
	for _, c := range cases {
		m, _, exit := runMain(t, c.args...)
		if exit != 1 || m["ok"] != false || m["error"] != c.code {
			t.Errorf("%q: exit %d, %v; want exit 1 %s", c.args, exit, m, c.code)
		}
	}
}

func TestRegister(t *testing.T) {
	withHandlers(t, map[string]Handler{})
	h := func(Request) (map[string]any, error) { return nil, nil }
	Register(ActionMedia, h)
	if lookup(ActionMedia) == nil {
		t.Fatal("registered handler not found")
	}
	for name, f := range map[string]func(){
		"duplicate": func() { Register(ActionMedia, h) },
		"unknown":   func() { Register("volume", h) },
		"nil":       func() { Register(ActionAudio, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s registration did not panic", name)
				}
			}()
			f()
		}()
	}
}

func TestAudioValidate(t *testing.T) {
	for _, a := range []Audio{{Volume: 0}, {Volume: 100, Muted: true, Device: "스피커(Realtek(R) Audio)"}} {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	for _, a := range []Audio{{Volume: -1}, {Volume: 101}, {Device: strings.Repeat("a", MaxDeviceRunes+1)}, {Device: "a\nb"}} {
		if a.Validate() == nil {
			t.Errorf("%+v validated", a)
		}
	}
}
