package useraction

// The media handler (#105): `user-action media <key>` presses one media key
// with SendInput, the same key a keyboard's play/pause or next-track button
// sends. Whatever app currently owns the system media session (a browser,
// Spotify, a video player) receives it. The keys are the last entry of an
// ordered backend list (mediaBackends), so a session-aware backend can be
// tried first and fall back to them.
//
// Windows has one play/pause toggle key and no way to ask what is playing
// without the WinRT media session API, so "play" and "pause" both send
// VK_MEDIA_PLAY_PAUSE: pressing "pause" while nothing plays starts playback.
// The design accepts this for the first version (§5).

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32     = windows.NewLazySystemDLL("user32.dll")
	procSendInput = modUser32.NewProc("SendInput")
)

// Virtual-key codes of the media keys.
const (
	vkMediaNextTrack = 0xB0
	vkMediaPrevTrack = 0xB1
	vkMediaStop      = 0xB2
	vkMediaPlayPause = 0xB3
)

const (
	inputKeyboard        = 1
	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
)

// mediaKeyVK maps every verb of MediaKeys to the key it presses.
var mediaKeyVK = map[string]uint16{
	"playpause": vkMediaPlayPause,
	"play":      vkMediaPlayPause, // no playback state: see the file comment
	"pause":     vkMediaPlayPause,
	"stop":      vkMediaStop,
	"next":      vkMediaNextTrack,
	"prev":      vkMediaPrevTrack,
}

// keybdInput is KEYBDINPUT.
type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// keyboardInput is INPUT with the keyboard member of the union. The union
// is as large as MOUSEINPUT, 8 bytes more than KEYBDINPUT on both 386 and
// amd64, hence the padding: sizeof(INPUT) is 28 and 40 respectively, and
// SendInput rejects any other cbSize.
type keyboardInput struct {
	typ uint32
	ki  keybdInput
	_   [8]byte
}

// sendInput is the SendInput call; tests replace it. It returns how many
// events were inserted.
var sendInput = func(inputs []keyboardInput) (uint32, error) {
	n, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	return uint32(n), err
}

func init() {
	Register(ActionMedia, mediaHandler)
}

// mediaKeyInputs is a key-down and key-up of vk. The media keys are
// extended keys; without the flag some players ignore the synthesized press.
func mediaKeyInputs(vk uint16) []keyboardInput {
	return []keyboardInput{
		{typ: inputKeyboard, ki: keybdInput{wVk: vk, dwFlags: keyeventfExtendedKey}},
		{typ: inputKeyboard, ki: keybdInput{wVk: vk, dwFlags: keyeventfExtendedKey | keyeventfKeyUp}},
	}
}

// mediaBackend is one way to carry out a media verb. send reports
// handled=false when the verb is not its to handle here (no media session
// to address, say), so the next backend gets it; an error means it tried
// and failed, and the next backend still gets a chance.
type mediaBackend struct {
	name string // the reply's "via"
	send func(verb string) (handled bool, err error)
}

// mediaBackends are tried in order and the first that handles the verb
// wins. The media keys come last because they always "work" — SendInput
// cannot tell whether any player reacted. A session-aware backend (the
// WinRT GlobalSystemMediaTransportControlsSessionManager, #117) goes in
// front of them and falls back to them when it has no session.
var mediaBackends = []mediaBackend{
	{name: "keys", send: sendMediaKey},
}

// sendMediaKey presses the verb's media key.
func sendMediaKey(verb string) (bool, error) {
	vk, ok := mediaKeyVK[verb]
	if !ok {
		return false, nil
	}
	inputs := mediaKeyInputs(vk)
	n, err := sendInput(inputs)
	if int(n) != len(inputs) {
		// Fewer than sent means input is blocked (BlockInput, the secure
		// desktop). A UIPI refusal is not reported by SendInput at all,
		// so success here means "pressed", not "some player reacted".
		return false, Failed("SendInput inserted %d of %d events: %v", n, len(inputs), err)
	}
	return true, nil
}

// mediaHandler serves `user-action media <key>`. The reply names the verb
// and the backend that carried it out: {"ok":true,"media":"next","via":"keys"}.
// When no backend handles it, the last backend error is the answer, or
// unsupported when none of them even tried.
func mediaHandler(req Request) (map[string]any, error) {
	if _, ok := mediaKeyVK[req.Verb]; !ok {
		return nil, badArgs("media: unknown key %q", req.Verb)
	}
	var lastErr error
	for _, b := range mediaBackends {
		handled, err := b.send(req.Verb)
		if err != nil {
			lastErr = err
			continue
		}
		if handled {
			return map[string]any{"media": req.Verb, "via": b.name}, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, Unsupported("media: nothing on this PC handles %q", req.Verb)
}
