package power

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// fakeHold is a ScreenHold on fake Windows calls: the request, the display
// watch (driven by display) and the timer (fired by fire).
type fakeHold struct {
	*ScreenHold
	mu       sync.Mutex
	calls    []string
	onChange func(on bool)
	timer    func()
	setErr   error
	watchErr error
	logs     []string
}

func newFakeHold() *fakeHold {
	f := &fakeHold{}
	f.ScreenHold = NewScreenHold(ScreenHoldHooks{
		Set:   func() error { f.record("set"); return f.setErr },
		Clear: func() error { f.record("clear"); return nil },
		Watch: func(onChange func(on bool)) (func(), error) {
			if f.watchErr != nil {
				return nil, f.watchErr
			}
			f.record("watch")
			f.onChange = onChange
			// Windows reports the current state on registering: still on.
			onChange(true)
			return func() { f.record("unwatch") }, nil
		},
		AfterFunc: func(d time.Duration, fn func()) func() bool {
			if d != HoldOffTimeout {
				panic(fmt.Sprintf("timer %s", d))
			}
			f.timer = fn
			return func() bool { f.timer = nil; return true }
		},
		Logf: func(format string, args ...any) { f.logs = append(f.logs, fmt.Sprintf(format, args...)) },
	})
	return f
}

func (f *fakeHold) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeHold) expectCalls(t *testing.T, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls %q, want %q", f.calls, want)
	}
	f.calls = nil
}

// The hold lasts while the display is off and ends when it is on again;
// the display's first report (still on) does not end it.
func TestScreenHoldEndsWhenTheDisplayIsOn(t *testing.T) {
	f := newFakeHold()
	if err := f.Hold(); err != nil {
		t.Fatal(err)
	}
	f.expectCalls(t, "watch", "set")
	if !f.Held() {
		t.Fatal("not held after Hold")
	}
	f.onChange(false)
	if f.timer != nil {
		t.Error("the off timeout is still armed after the display went off")
	}
	f.onChange(false) // repeated reports change nothing
	if !f.Held() {
		t.Fatal("released while the display is off")
	}
	f.onChange(true)
	if f.Held() {
		t.Fatal("still held after the display came on")
	}
	f.expectCalls(t, "unwatch", "clear")
	if want := []string{"Screen-off hold: released (the display is on again)"}; !reflect.DeepEqual(f.logs, want) {
		t.Errorf("logs %q", f.logs)
	}
	f.onChange(false) // a stale watch's report
	f.onChange(true)
	f.expectCalls(t)
}

// A display that never reported off (the screen off did not take) does
// not keep the PC awake.
func TestScreenHoldOffTimeout(t *testing.T) {
	f := newFakeHold()
	if err := f.Hold(); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.timer()
	if f.Held() {
		t.Fatal("held after the off timeout")
	}
	f.expectCalls(t, "unwatch", "clear")
	if len(f.logs) != 1 || f.logs[0] != "Screen-off hold: released (the display did not report off within 1m0s)" {
		t.Errorf("logs %q", f.logs)
	}
}

// Release is idempotent and logs only when something was held.
func TestScreenHoldRelease(t *testing.T) {
	f := newFakeHold()
	f.Release("suspend")
	f.expectCalls(t)
	if len(f.logs) != 0 {
		t.Errorf("logs %q", f.logs)
	}
	if err := f.Hold(); err != nil {
		t.Fatal(err)
	}
	f.onChange(false)
	f.calls = nil
	f.Release("suspend")
	f.Release("suspend")
	f.expectCalls(t, "unwatch", "clear")
	if want := []string{"Screen-off hold: released (suspend)"}; !reflect.DeepEqual(f.logs, want) {
		t.Errorf("logs %q", f.logs)
	}
}

// Holding again starts over: the old watch and request go first.
func TestScreenHoldAgain(t *testing.T) {
	f := newFakeHold()
	_ = f.Hold()
	f.onChange(false)
	f.calls = nil
	if err := f.Hold(); err != nil {
		t.Fatal(err)
	}
	f.expectCalls(t, "unwatch", "clear", "watch", "set")
	f.onChange(true) // the new watch's first report: the display still on
	if !f.Held() {
		t.Fatal("the second hold ended on the display's first report")
	}
}

// Without the display watch nothing is held: a hold nobody ends would keep
// the PC from sleeping. A failing request stops the watch again.
func TestScreenHoldFailures(t *testing.T) {
	f := newFakeHold()
	f.watchErr = errors.New("no")
	if err := f.Hold(); err == nil || f.Held() {
		t.Fatalf("Hold = %v, held %v", err, f.Held())
	}
	f.expectCalls(t)

	f.watchErr = nil
	f.setErr = errors.New("denied")
	if err := f.Hold(); err == nil || f.Held() {
		t.Fatalf("Hold = %v, held %v", err, f.Held())
	}
	f.expectCalls(t, "watch", "set", "unwatch")
}

// The structures handed to Windows: the GUID's bytes and the reason
// context's size (at least REASON_CONTEXT's).
func TestScreenHoldLayouts(t *testing.T) {
	if got := guidConsoleDisplayState.String(); got != "{6FE69556-704A-47A0-8F24-C28D936FDA47}" {
		t.Errorf("GUID_CONSOLE_DISPLAY_STATE = %s", got)
	}
	// REASON_CONTEXT: Version, Flags, then the union whose larger member
	// is {HMODULE, ULONG, ULONG, LPWSTR*}.
	ptr := unsafe.Sizeof(uintptr(0))
	if got, min := unsafe.Sizeof(reasonContext{}), 8+2*ptr+8; got < min {
		t.Errorf("reasonContext is %d bytes, want at least %d", got, min)
	}
	if off := unsafe.Offsetof(reasonContext{}.reason); off != 8 {
		t.Errorf("reason at offset %d, want 8", off)
	}
}
