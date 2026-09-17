package service

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// bus is the process-wide notification pipeline (design doc §4). It is nil
// until startNotifier runs (and in most tests), which makes emit a no-op.
var (
	bus   *notify.Bus
	busMu sync.RWMutex
)

// notifyDebug makes the sink-less bus log every event it would have
// delivered. Set STPC_NOTIFY_DEBUG=1 in the service environment.
var notifyDebug = os.Getenv("STPC_NOTIFY_DEBUG") != ""

// emit queues a catalogue event next to the logMsg call that reports it.
// Field values must already be human-readable (formatDelay, 15:04:05).
// Safe to call before startNotifier or after stopNotifier.
func emit(cat, kind string, fields map[string]string, actions ...notify.Action) {
	busMu.RLock()
	b := bus
	busMu.RUnlock()
	if b == nil {
		return
	}
	b.Emit(notify.Event{
		Category: cat,
		Kind:     kind,
		At:       time.Now(),
		Fields:   fields,
		Actions:  actions,
	})
}

// emitDevice reports a device-state change (display.changed,
// session.locked/unlocked) to the bus taps only — the SmartThings push
// sink in practice. Device state is not a notification (edge-driver doc
// §4.5), so it never reaches Telegram and has no catalogue entry.
func emitDevice(cat, kind string, fields map[string]string) {
	busMu.RLock()
	b := bus
	busMu.RUnlock()
	if b == nil {
		return
	}
	b.TapOnly(notify.Event{
		Category: cat,
		Kind:     kind,
		At:       time.Now(),
		Fields:   fields,
	})
}

// startNotifier builds the bus on top of sink, reading the notify and
// quiet-hours settings from the live config on every event (hot reload).
// A nil sink installs debugSink so the pipeline still runs. Issue #56
// passes the Telegram sink here; that sink should itself consult
// getConfig().Telegram on each Send so enabling Telegram needs no restart.
// Calling it again replaces (and closes) the previous bus.
func startNotifier(sink notify.Sink) {
	if sink == nil {
		sink = debugSink{}
	}
	b := notify.New(notify.Options{
		Sink:   sink,
		Config: func() notify.Config { return getConfig().Notify },
		Quiet:  func() notify.QuietHours { return getConfig().Telegram.QuietHours },
		Log:    logMsg,
	})
	// The SmartThings hub push (#68) rides on a raw tap so device state
	// reaches the Edge driver unfiltered; it is a no-op without
	// subscriptions, and has to be re-attached to every new bus.
	b.Tap(stPushTap)
	busMu.Lock()
	old := bus
	bus = b
	busMu.Unlock()
	old.Close()
}

// stopNotifier closes the bus (delivering what is already queued) and
// turns emit back into a no-op.
func stopNotifier() {
	busMu.Lock()
	old := bus
	bus = nil
	busMu.Unlock()
	old.Close()
}

// debugSink stands in for a real channel while none is configured.
type debugSink struct{}

func (debugSink) Send(_ context.Context, ev notify.Event) error {
	if notifyDebug {
		logMsg("notify(debug): %s %v", ev.Key(), ev.Fields)
	}
	return nil
}

// notifyLabels are the inline-button captions, ko then en.
var notifyLabels = map[string][2]string{
	"run_now": {"바로 실행", "Run now"},
	"cancel":  {"취소", "Cancel"},
}

// notifyLabel picks the caption for telegram.lang.
func notifyLabel(key string) string {
	l := notifyLabels[key]
	if getConfig().Telegram.Lang == "en" {
		return l[1]
	}
	return l[0]
}

// graceActions are the buttons on remote.grace_scheduled (design doc §8).
// The callback payloads are handled by issue #62.
func graceActions() []notify.Action {
	return []notify.Action{
		{Label: notifyLabel("run_now"), Data: "runnow:"},
		{Label: notifyLabel("cancel"), Data: "cancel:"},
	}
}
