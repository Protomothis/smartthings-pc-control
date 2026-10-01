package useraction

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type sentMessage struct {
	hwnd, msg, wParam, lParam uintptr
	flags, timeoutMs          uint32
}

// withFakeScreen records the broadcasts and mouse input the handler would
// have sent; nothing reaches the real monitors. ret and err are what the
// fake SendMessageTimeoutW answers.
func withFakeScreen(t *testing.T, ret uintptr, err error) (*[]sentMessage, *[][]pointerInput) {
	t.Helper()
	var msgs []sentMessage
	var inputs [][]pointerInput
	savedMsg, savedInput := sendMessageTimeout, sendMouseInput
	sendMessageTimeout = func(hwnd, msg, wParam, lParam uintptr, flags, timeoutMs uint32) (uintptr, error) {
		msgs = append(msgs, sentMessage{hwnd, msg, wParam, lParam, flags, timeoutMs})
		return ret, err
	}
	sendMouseInput = func(in []pointerInput) (uint32, error) {
		inputs = append(inputs, append([]pointerInput(nil), in...))
		return uint32(len(in)), nil
	}
	t.Cleanup(func() { sendMessageTimeout, sendMouseInput = savedMsg, savedInput })
	return &msgs, &inputs
}

func TestParseScreen(t *testing.T) {
	for _, v := range []string{"off", "on"} {
		req, err := Parse([]string{"screen", v})
		if err != nil || !reflect.DeepEqual(req, Request{Action: ActionScreen, Verb: v}) {
			t.Errorf("Parse(screen %s) = %+v, %v", v, req, err)
		}
	}
	for _, args := range [][]string{{"screen"}, {"screen", "OFF"}, {"screen", "toggle"}, {"screen", "off", "on"}} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) accepted", args)
		}
	}
}

func TestScreenOffBroadcastsWithTimeout(t *testing.T) {
	msgs, inputs := withFakeScreen(t, 1, nil)
	if _, line, code := runMain(t, "screen", "off"); code != 0 || line != `{"ok":true,"screen":"off"}` {
		t.Fatalf("exit %d, %s", code, line)
	}
	want := []sentMessage{{0xFFFF, 0x0112, 0xF170, 2, 0x0002, 2000}} // HWND_BROADCAST, WM_SYSCOMMAND, SC_MONITORPOWER, off, SMTO_ABORTIFHUNG
	if !reflect.DeepEqual(*msgs, want) {
		t.Errorf("sent %+v, want %+v", *msgs, want)
	}
	if len(*inputs) != 0 {
		t.Errorf("screen off sent input %v", *inputs)
	}
}

func TestScreenOnNudgesThenBroadcasts(t *testing.T) {
	msgs, inputs := withFakeScreen(t, 1, nil)
	if _, line, code := runMain(t, "screen", "on"); code != 0 || line != `{"ok":true,"input":true,"screen":"on"}` {
		t.Fatalf("exit %d, %s", code, line)
	}
	minusOne := -1
	want := []sentMessage{{0xFFFF, 0x0112, 0xF170, uintptr(minusOne), 0x0002, 2000}}
	if !reflect.DeepEqual(*msgs, want) {
		t.Errorf("sent %+v, want %+v", *msgs, want)
	}
	wantIn := [][]pointerInput{{{typ: inputMouse, mi: mouseInput{dwFlags: mouseeventfMove}}}}
	if !reflect.DeepEqual(*inputs, wantIn) {
		t.Errorf("input %+v, want a zero mouse move", *inputs)
	}
}

func TestScreenBroadcastTimeoutIsNotAFailure(t *testing.T) {
	withFakeScreen(t, 0, windows.ERROR_TIMEOUT)
	if _, line, code := runMain(t, "screen", "off"); code != 0 {
		t.Errorf("timed-out broadcast: exit %d, %s", code, line)
	}
	withFakeScreen(t, 0, windows.ERROR_ACCESS_DENIED)
	m, _, code := runMain(t, "screen", "off")
	if code != 1 || m["error"] != CodeFailed || !strings.Contains(m["message"].(string), "SendMessageTimeout") {
		t.Errorf("failed broadcast: exit %d, %v", code, m)
	}
}

func TestPointerInputSize(t *testing.T) {
	// SendInput takes one cbSize for every INPUT, whichever member is used.
	if unsafe.Sizeof(pointerInput{}) != unsafe.Sizeof(keyboardInput{}) {
		t.Errorf("sizeof(pointerInput) = %d, sizeof(keyboardInput) = %d", unsafe.Sizeof(pointerInput{}), unsafe.Sizeof(keyboardInput{}))
	}
}
