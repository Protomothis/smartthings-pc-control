package useraction

// The "keys" preset (#146): one key combination such as ctrl+alt+m, win+d
// or media_play_pause, pressed in the user session with SendInput — the
// modifiers down in the order written, the key down and up, the modifiers
// up in reverse.
//
// The rule for its path lives here once and is shared by the subcommand
// (Parse), config.ValidatePreset (through Parse) and the GUI editor, which
// maps the two errors to its own messages:
//
//   - names are joined with "+", compared without regard to case, and may
//     have spaces around them ("Ctrl + Alt + M");
//   - any of the modifiers ctrl, alt, shift and win, each at most once,
//     then exactly one other key, last.
//
// What SendInput cannot do stays impossible: ctrl+alt+delete is handled
// by Windows below the input queue, and a window running as administrator
// ignores input from this unelevated process (UIPI) without SendInput
// saying so.

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

var (
	// ErrPresetKeyUnknown: a name that is not in the key list.
	ErrPresetKeyUnknown = errors.New("preset: unknown key name (ctrl, alt, shift, win, a-z, 0-9, f1-f24, enter, media_play_pause …)")
	// ErrPresetKeysCombo: not "modifiers + one key" (two keys, a modifier
	// twice or after the key, an empty part).
	ErrPresetKeysCombo = errors.New("preset: keys must be modifiers (ctrl, alt, shift, win) and one key joined with +, such as ctrl+alt+m")
)

// presetKey is one key: its virtual-key code and whether it is an
// extended key (the arrows, the navigation block, the media keys …),
// which SendInput must be told for the press to look like the real one.
type presetKey struct {
	vk  uint16
	ext bool
}

// presetModifiers are the modifier names.
var presetModifiers = map[string]presetKey{
	"ctrl":  {0x11, false}, // VK_CONTROL
	"alt":   {0x12, false}, // VK_MENU
	"shift": {0x10, false}, // VK_SHIFT
	"win":   {0x5B, true},  // VK_LWIN
}

// presetKeyNames are the other keys a combination may end in.
var presetKeyNames = func() map[string]presetKey {
	m := map[string]presetKey{
		"enter": {0x0D, false}, "esc": {0x1B, false}, "escape": {0x1B, false},
		"tab": {0x09, false}, "space": {0x20, false}, "backspace": {0x08, false},
		"delete": {0x2E, true}, "del": {0x2E, true}, "insert": {0x2D, true},
		"home": {0x24, true}, "end": {0x23, true},
		"pageup": {0x21, true}, "pagedown": {0x22, true},
		"up": {0x26, true}, "down": {0x28, true}, "left": {0x25, true}, "right": {0x27, true},
		"printscreen": {0x2C, true}, "pause": {0x13, false},
		"capslock": {0x14, false}, "numlock": {0x90, true}, "scrolllock": {0x91, false},
		"apps": {0x5D, true}, // the application (context menu) key

		// The keys of the main block that are not letters or digits, by
		// name: "+" separates the parts.
		"minus": {0xBD, false}, "equals": {0xBB, false}, "comma": {0xBC, false},
		"period": {0xBE, false}, "slash": {0xBF, false}, "backslash": {0xDC, false},
		"semicolon": {0xBA, false}, "quote": {0xDE, false}, "backquote": {0xC0, false},
		"lbracket": {0xDB, false}, "rbracket": {0xDD, false},

		"num_multiply": {0x6A, false}, "num_add": {0x6B, false}, "num_subtract": {0x6D, false},
		"num_decimal": {0x6E, false}, "num_divide": {0x6F, true},

		// Media and volume keys, the ones a keyboard's extra buttons send.
		"media_play_pause": {0xB3, true}, "media_next": {0xB0, true},
		"media_prev": {0xB1, true}, "media_stop": {0xB2, true},
		"volume_up": {0xAF, true}, "volume_down": {0xAE, true}, "volume_mute": {0xAD, true},
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = presetKey{uint16(c - 'a' + 'A'), false}
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = presetKey{uint16(c), false}
		m["num"+string(c)] = presetKey{uint16(0x60 + c - '0'), false}
	}
	for i := 1; i <= 24; i++ {
		m[fmt.Sprintf("f%d", i)] = presetKey{uint16(0x70 + i - 1), false}
	}
	return m
}()

// presetKeyCombo is a parsed keys path: the modifiers in the order written
// and the key.
type presetKeyCombo struct {
	mods []presetKey
	key  presetKey
}

// ValidatePresetKeys checks a keys preset's path: nil, ErrPresetKeyUnknown
// or ErrPresetKeysCombo.
func ValidatePresetKeys(path string) error {
	_, err := parsePresetKeys(path)
	return err
}

func parsePresetKeys(path string) (presetKeyCombo, error) {
	var c presetKeyCombo
	parts := strings.Split(path, "+")
	seen := map[string]bool{}
	for i, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			return c, ErrPresetKeysCombo
		}
		last := i == len(parts)-1
		if m, ok := presetModifiers[name]; ok {
			if last || seen[name] {
				// "ctrl" alone or "m+ctrl": no key; "ctrl+ctrl+m".
				return c, ErrPresetKeysCombo
			}
			seen[name] = true
			c.mods = append(c.mods, m)
			continue
		}
		k, ok := presetKeyNames[name]
		if !ok {
			return c, ErrPresetKeyUnknown
		}
		if !last {
			// "a+b" or "m+ctrl": a key before the end.
			return c, ErrPresetKeysCombo
		}
		c.key = k
	}
	return c, nil
}

// keyInput is a key-down or key-up of k with its scan code.
func keyInput(k presetKey, scan func(vk uint16) uint16, up bool) keyboardInput {
	var flags uint32
	if k.ext {
		flags |= keyeventfExtendedKey
	}
	if up {
		flags |= keyeventfKeyUp
	}
	return keyboardInput{typ: inputKeyboard, ki: keybdInput{wVk: k.vk, wScan: scan(k.vk), dwFlags: flags}}
}

// inputs is the whole press: modifiers down, key down, key up, modifiers
// up in reverse — one SendInput call, so nothing typed on the real
// keyboard lands in between.
func (c presetKeyCombo) inputs(scan func(vk uint16) uint16) []keyboardInput {
	out := make([]keyboardInput, 0, 2*len(c.mods)+2)
	for _, m := range c.mods {
		out = append(out, keyInput(m, scan, false))
	}
	out = append(out, keyInput(c.key, scan, false), keyInput(c.key, scan, true))
	for i := len(c.mods) - 1; i >= 0; i-- {
		out = append(out, keyInput(c.mods[i], scan, true))
	}
	return out
}

var procMapVirtualKeyW = modUser32.NewProc("MapVirtualKeyW")

// keyScanCode is the scan code of vk on the current layout (0 when it has
// none): games and remote-desktop clients that read scan codes instead of
// virtual keys see the press too. Replaced by the tests.
var keyScanCode = func(vk uint16) uint16 {
	const mapvkVKToVSC = 0
	r, _, _ := procMapVirtualKeyW.Call(uintptr(vk), mapvkVKToVSC)
	return uint16(r)
}

// pressPresetKeys presses a keys preset's combination.
func pressPresetKeys(path string) error {
	c, err := parsePresetKeys(path)
	if err != nil {
		return badArgs("%s", err.Error())
	}
	inputs := c.inputs(keyScanCode)
	n, serr := sendInput(inputs)
	if int(n) != len(inputs) {
		// Input is blocked (BlockInput, the secure desktop of a UAC prompt
		// or the lock screen).
		detail := fmt.Sprintf("SendInput inserted %d of %d events", n, len(inputs))
		if serr != nil && !errors.Is(serr, windows.ERROR_SUCCESS) {
			detail += ": " + serr.Error()
		}
		return &Error{Code: CodeFailed, Reason: PresetKeysFailed, Message: "could not send the keys", Detail: detail}
	}
	return nil
}
