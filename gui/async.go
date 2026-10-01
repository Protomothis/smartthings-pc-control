package gui

import "fyne.io/fyne/v2"

// Every service call a click starts goes through runAsync: the request
// runs off the UI goroutine (a save, a restart or a stuck service could
// otherwise freeze the window for the client's whole timeout, several
// times over), the control that started it is busy meanwhile, and the
// result is applied back on the UI goroutine. The periodic loads (pollLoop,
// initialLoad) already run on goroutines of their own.

// runAsync calls busy(true), runs work in the background and then, on the
// UI goroutine, busy(false) followed by done with work's result. busy and
// done may be nil. Call it from the UI goroutine when busy touches widgets.
func runAsync[T any](busy func(bool), work func() (T, error), done func(T, error)) {
	if busy != nil {
		busy(true)
	}
	background(func() {
		v, err := work()
		fyne.Do(func() {
			if busy != nil {
				busy(false)
			}
			if done != nil {
				done(v, err)
			}
		})
	})
}

// runAsyncErr is runAsync for calls that only report an error.
func runAsyncErr(busy func(bool), work func() error, done func(error)) {
	runAsync(busy, func() (struct{}, error) { return struct{}{}, work() }, func(_ struct{}, err error) {
		if done != nil {
			done(err)
		}
	})
}

// busyControls is a busy callback that disables the controls while a call
// is in flight and enables them again afterwards; a done callback that
// wants them disabled (a cancel button with nothing left to cancel) runs
// after it and can disable them again.
func busyControls(ws ...fyne.Disableable) func(bool) {
	return func(on bool) {
		for _, w := range ws {
			setEnabled(w, !on)
		}
	}
}

// syncBackground makes background() run its work inline. Tests set it:
// Fyne's test driver runs fyne.Do on the calling goroutine instead of the UI
// thread, so work a build starts in the background would race that build.
var syncBackground = false

// background runs slow work a build kicks off (the service state) off the
// UI thread; the work hands its result back with fyne.Do.
func background(f func()) {
	if syncBackground {
		f()
		return
	}
	go f()
}
