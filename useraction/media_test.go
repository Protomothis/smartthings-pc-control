package useraction

import (
	"errors"
	"reflect"
	"testing"
	"unsafe"
)

// withFakeSendInput records what the handler would have pressed.
func withFakeSendInput(t *testing.T, ret uint32, err error) *[][]keyboardInput {
	t.Helper()
	var calls [][]keyboardInput
	saved := sendInput
	sendInput = func(in []keyboardInput) (uint32, error) {
		calls = append(calls, append([]keyboardInput(nil), in...))
		if ret == ^uint32(0) {
			return uint32(len(in)), nil
		}
		return ret, err
	}
	t.Cleanup(func() { sendInput = saved })
	return &calls
}

func TestMediaKeyTable(t *testing.T) {
	want := map[string]uint16{
		"playpause": 0xB3, // VK_MEDIA_PLAY_PAUSE
		"play":      0xB3, // no playback state: play and pause share the toggle
		"pause":     0xB3,
		"stop":      0xB2, // VK_MEDIA_STOP
		"next":      0xB0, // VK_MEDIA_NEXT_TRACK
		"prev":      0xB1, // VK_MEDIA_PREV_TRACK
	}
	if !reflect.DeepEqual(mediaKeyVK, want) {
		t.Errorf("mediaKeyVK = %v, want %v", mediaKeyVK, want)
	}
	// Every verb the parser accepts has a key.
	for _, k := range MediaKeys {
		if _, ok := mediaKeyVK[k]; !ok {
			t.Errorf("media verb %q has no key", k)
		}
	}
}

func TestMediaHandlerSendsDownUp(t *testing.T) {
	calls := withFakeSendInput(t, ^uint32(0), nil)
	m, line, code := runMain(t, "media", "next")
	if code != 0 || line != `{"ok":true,"media":"next","via":"keys"}` {
		t.Fatalf("exit %d, %s (%v)", code, line, m)
	}
	if len(*calls) != 1 {
		t.Fatalf("SendInput called %d times, want once", len(*calls))
	}
	in := (*calls)[0]
	want := []keyboardInput{
		{typ: inputKeyboard, ki: keybdInput{wVk: vkMediaNextTrack, dwFlags: keyeventfExtendedKey}},
		{typ: inputKeyboard, ki: keybdInput{wVk: vkMediaNextTrack, dwFlags: keyeventfExtendedKey | keyeventfKeyUp}},
	}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("inputs = %+v, want %+v", in, want)
	}
}

func TestMediaHandlerBlocked(t *testing.T) {
	withFakeSendInput(t, 0, errors.New("Access is denied."))
	if m, _, code := runMain(t, "media", "playpause"); code != 1 || m["error"] != CodeFailed {
		t.Errorf("blocked SendInput: exit %d, %v; want failed", code, m)
	}
}

// A backend in front of the keys (the WinRT session manager of #117) wins
// when it handles the verb and falls back to the keys when it does not or
// fails.
func TestMediaBackendOrder(t *testing.T) {
	calls := withFakeSendInput(t, ^uint32(0), nil)
	saved := mediaBackends
	t.Cleanup(func() { mediaBackends = saved })

	var result struct {
		handled bool
		err     error
	}
	var got []string
	session := mediaBackend{name: "session", send: func(verb string) (bool, error) {
		got = append(got, verb)
		return result.handled, result.err
	}}
	mediaBackends = append([]mediaBackend{session}, saved...)

	result.handled = true
	if _, line, code := runMain(t, "media", "pause"); code != 0 || line != `{"ok":true,"media":"pause","via":"session"}` {
		t.Errorf("session handles: exit %d, %s", code, line)
	}
	if len(*calls) != 0 {
		t.Error("the keys were pressed although the session backend handled it")
	}

	result.handled = false
	if _, line, code := runMain(t, "media", "pause"); code != 0 || line != `{"ok":true,"media":"pause","via":"keys"}` {
		t.Errorf("no session: exit %d, %s", code, line)
	}
	result.err = Failed("WinRT: boom")
	if _, line, code := runMain(t, "media", "next"); code != 0 || line != `{"ok":true,"media":"next","via":"keys"}` {
		t.Errorf("session error: exit %d, %s", code, line)
	}
	if len(got) != 3 || len(*calls) != 2 {
		t.Errorf("session saw %v, keys pressed %d times", got, len(*calls))
	}

	// Nobody handles it: the last error, or unsupported without one.
	mediaBackends = []mediaBackend{session}
	if m, _, code := runMain(t, "media", "stop"); code != 1 || m["error"] != CodeFailed {
		t.Errorf("all failed: exit %d, %v", code, m)
	}
	result.err = nil
	if m, _, code := runMain(t, "media", "stop"); code != 1 || m["error"] != CodeUnsupported {
		t.Errorf("none handled: exit %d, %v", code, m)
	}
}

// SendInput rejects a cbSize other than sizeof(INPUT).
func TestInputLayout(t *testing.T) {
	want := uintptr(28)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = 40
	}
	if got := unsafe.Sizeof(keyboardInput{}); got != want {
		t.Errorf("sizeof(INPUT) = %d, want %d", got, want)
	}
}
