package useraction

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// fakeVtbl is a one-method vtable whose slot 0 is a Go callback with the
// COM calling shape (this, out) that writes a marker through out — the
// way GetMute or GetCurrentSession fill their caller's variable.
var (
	fakeVtbl    [32]uintptr
	fakeFillOut = syscall.NewCallback(func(this uintptr, out *uint32) uintptr {
		*out = 0xC0FFEE
		return sOK
	})
)

func init() { fakeVtbl[0] = fakeFillOut }

// fillAtDepth recurses depth frames, each with some stack of its own, and
// then lets the fake object fill a local through comObject.call. Across
// the depths below one of the calls lands where call's prologue (or the
// callback re-entering Go) has to grow and therefore move the stack. If a
// pointer passed as a uintptr were not kept on the heap for the call
// (unsafe.Pointer rule 4, #121), the callback would write into the old
// stack and the local would keep its zero value.
func fillAtDepth(o *comObject, depth int) uint32 {
	var pad [64]byte
	if depth > 0 {
		r := fillAtDepth(o, depth-1)
		runtime.KeepAlive(&pad)
		return r
	}
	var got uint32
	o.call(0, uintptr(unsafe.Pointer(&got)))
	return got
}

func TestComObjectCallKeepsOutPointerValidAcrossStackGrowth(t *testing.T) {
	o := &comObject{vtbl: &fakeVtbl}
	for depth := 0; depth < 400; depth++ {
		done := make(chan uint32)
		// A fresh goroutine starts with a small stack, so the growth
		// happens inside the depths tried here.
		go func() { done <- fillAtDepth(o, depth) }()
		if got := <-done; got != 0xC0FFEE {
			t.Fatalf("depth %d: out = %#x, want 0xC0FFEE (pointer went stale across a stack move)", depth, got)
		}
	}
}
