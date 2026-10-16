package useraction

import (
	"errors"
	"slices"
	"testing"
)

func TestValidatePresetKeys(t *testing.T) {
	for _, ok := range []string{
		"ctrl+alt+m", "win+d", "media_play_pause", "Ctrl + Shift + Esc", "WIN+SHIFT+S",
		"f24", "alt+f4", "ctrl+alt+shift+win+0", "volume_mute", "ctrl+num5", "shift+slash",
		"win+printscreen", "ctrl+pagedown", "alt+tab", "apps",
	} {
		if err := ValidatePresetKeys(ok); err != nil {
			t.Errorf("ValidatePresetKeys(%q) = %v", ok, err)
		}
	}
	for raw, want := range map[string]error{
		"":                    ErrPresetKeysCombo,
		"ctrl+":               ErrPresetKeysCombo,
		"+m":                  ErrPresetKeysCombo,
		"ctrl++m":             ErrPresetKeysCombo,
		"ctrl":                ErrPresetKeysCombo, // a modifier alone
		"m+ctrl":              ErrPresetKeysCombo, // the key before a modifier
		"a+b":                 ErrPresetKeysCombo, // two keys
		"ctrl+ctrl+m":         ErrPresetKeysCombo,
		"ctrl+Ctrl+m":         ErrPresetKeysCombo,
		"ctrl+alt+mm":         ErrPresetKeyUnknown,
		"f25":                 ErrPresetKeyUnknown,
		"ctrl+alt+page up":    ErrPresetKeyUnknown,
		"ctrl+-":              ErrPresetKeyUnknown,
		"hyper+m":             ErrPresetKeyUnknown,
		"ctrl+alt+m; rm -rf":  ErrPresetKeyUnknown,
		"media play pause":    ErrPresetKeyUnknown,
		"ctrl+alt+m+del+x+yz": ErrPresetKeysCombo,
	} {
		if err := ValidatePresetKeys(raw); !errors.Is(err, want) {
			t.Errorf("ValidatePresetKeys(%q) = %v, want %v", raw, err, want)
		}
	}
}

func TestPresetKeyInputs(t *testing.T) {
	c, err := parsePresetKeys("Ctrl+Win+Right")
	if err != nil {
		t.Fatal(err)
	}
	scan := func(vk uint16) uint16 { return vk + 0x1000 }
	got := c.inputs(scan)
	type ev struct {
		vk    uint16
		scan  uint16
		flags uint32
	}
	var evs []ev
	for _, in := range got {
		if in.typ != inputKeyboard {
			t.Fatalf("input type %d", in.typ)
		}
		evs = append(evs, ev{in.ki.wVk, in.ki.wScan, in.ki.dwFlags})
	}
	const ext, up = keyeventfExtendedKey, keyeventfKeyUp
	want := []ev{
		{0x11, 0x1011, 0},        // ctrl down
		{0x5B, 0x105B, ext},      // win down
		{0x27, 0x1027, ext},      // right down
		{0x27, 0x1027, ext | up}, // right up
		{0x5B, 0x105B, ext | up}, // win up
		{0x11, 0x1011, up},       // ctrl up
	}
	if !slices.Equal(evs, want) {
		t.Errorf("inputs\n got %+v\nwant %+v", evs, want)
	}

	c, _ = parsePresetKeys("media_play_pause")
	if got := c.inputs(scan); len(got) != 2 || got[0].ki.wVk != vkMediaPlayPause || got[0].ki.dwFlags != ext || got[1].ki.dwFlags != ext|up {
		t.Errorf("media key: %+v", got)
	}
	c, _ = parsePresetKeys("shift+a")
	if got := c.inputs(scan); got[1].ki.wVk != 'A' {
		t.Errorf("letter: %+v", got)
	}
	c, _ = parsePresetKeys("f1")
	if got := c.inputs(scan); got[0].ki.wVk != 0x70 {
		t.Errorf("f1: %+v", got)
	}
}

func TestHandlePresetKeys(t *testing.T) {
	si, ks := sendInput, keyScanCode
	t.Cleanup(func() { sendInput, keyScanCode = si, ks })
	keyScanCode = func(uint16) uint16 { return 0 }
	var sent [][]keyboardInput
	sendInput = func(in []keyboardInput) (uint32, error) {
		sent = append(sent, slices.Clone(in))
		return uint32(len(in)), nil
	}
	out, err := handlePreset(Request{PresetType: "keys", Path: "win+d"})
	if err != nil || out["started"] != true || len(sent) != 1 || len(sent[0]) != 4 || sent[0][1].ki.wVk != 'D' {
		t.Fatalf("win+d: %v, %v, %+v", out, err, sent)
	}

	sendInput = func(in []keyboardInput) (uint32, error) { return 0, errors.New("Access is denied.") }
	_, err = handlePreset(Request{PresetType: "keys", Path: "ctrl+alt+m"})
	var ue *Error
	if !errors.As(err, &ue) || ue.Code != CodeFailed || ue.Reason != PresetKeysFailed || ue.Message != "could not send the keys" {
		t.Errorf("blocked input: %+v", err)
	}

	sent = nil
	sendInput = func(in []keyboardInput) (uint32, error) { sent = append(sent, in); return uint32(len(in)), nil }
	if _, err := handlePreset(Request{PresetType: "keys", Path: "ctrl+nope"}); !errors.As(err, &ue) || ue.Code != CodeBadArgs || len(sent) != 0 {
		t.Errorf("unknown key reached SendInput: %v, %d", err, len(sent))
	}
}

func TestParsePresetKeysAndFocus(t *testing.T) {
	for _, argv := range [][]string{
		{"preset", "--type", "keys", "--path", "ctrl+alt+m"},
		{"preset", "--type", "focus", "--path", "obs64.exe"},
		{"preset", "--type", "focus", "--path", "제목:메모장"},
	} {
		if _, err := Parse(argv); err != nil {
			t.Errorf("%q: %v", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"preset", "--type", "keys", "--path", "ctrl+alt+m", "--arg", "x"},
		{"preset", "--type", "focus", "--path", "obs64.exe", "--arg", "x"},
		{"preset", "--type", "keys", "--path", "ctrl+"},
		{"preset", "--type", "focus", "--path", `C:\obs\obs64.exe`},
		{"preset", "--type", "Keys", "--path", "win+d"},
	} {
		var ue *Error
		if _, err := Parse(argv); !errors.As(err, &ue) || ue.Code != CodeBadArgs {
			t.Errorf("%q: %v, want bad_args", argv, err)
		}
	}
}
