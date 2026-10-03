package service

// Audio and media state when no tray app reports for the target session
// (docs/design/media-notify.md §2). The stores are fed by the tray
// heartbeat and by the replies of user-action runs. With the tray app in
// another session (the RDP session next to an unlocked console), or in
// none at all (just after an install or a service restart), nothing feeds
// them: status said audio.available=false, the driver therefore sent no
// command, and no command reply ever filled the store.
//
// So the service reads the target session itself — `user-action audio
// get` and `user-action media info` through the usual runner, whose reply
// is stored like a command's — whenever someone is logged in there and
// there is no recent sample and no tray app reporting from it: shortly
// after the service starts, when the target session changes, and on the
// status reads that find the sample missing or old. A tray app that
// heartbeats from the target session stays the source; then nothing runs.
// The readings run in the background, one at a time per store and at most
// one every fillEvery, so a status read never waits for one.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/devstate"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// fillStartDelay is how long after the service start the first reading
	// waits: a tray app in the target session usually reports by then.
	fillStartDelay = 5 * time.Second
	// fillEvery is the least time between two readings of one store
	// (unless the target session changed).
	fillEvery = 30 * time.Second
	// fillStaleAfter is the age from which a sample is read again while
	// no tray app reports. Below the media TTL (90s), so the media block
	// of a PC that is polled does not drop to "none" between readings.
	fillStaleAfter = 60 * time.Second
	// trayQuietAfter is how long after its last heartbeat a tray app in
	// the target session still counts as the source. It posts every 30s.
	trayQuietAfter = 60 * time.Second
)

// fillKind is the store a reading fills.
type fillKind int

const (
	fillAudio fillKind = iota
	fillMedia
	fillKinds
)

func (k fillKind) String() string {
	if k == fillAudio {
		return "audio"
	}
	return "media"
}

// sessionFill is the background reading of one store.
type sessionFill struct {
	gate devstate.Refresh
	// failing is set after a failed reading, so a lasting failure is
	// logged once and its end once.
	mu      sync.Mutex
	failing bool
}

// fills holds a reading per store. No initializer: the readings reach
// targetUserSession, which reaches back here.
var fills [fillKinds]sessionFill

// noteTrayReport records that a heartbeat from the target session arrived
// (heartbeatStore stores only those).
func noteTrayReport() { dev.tray.Set(struct{}{}, clock.audio()) }

// trayReporting reports whether a tray app in the target session has
// posted within trayQuietAfter.
func trayReporting(now time.Time) bool {
	_, _, ok := dev.tray.Fresh(now, trayQuietAfter)
	return ok
}

// fillNeeded reports whether store k should be read from the session now:
// media control is on, no tray app reports, and the sample is missing or
// older than fillStaleAfter. Whether someone is logged in is the caller's
// check (each trigger knows it already).
func fillNeeded(k fillKind, now time.Time) bool {
	if !fillWanted(now) {
		return false
	}
	var at time.Time
	var ok bool
	if k == fillAudio {
		_, at, ok = dev.audio.Last()
	} else {
		_, at, ok = dev.media.Last()
	}
	return !ok || now.Sub(at) > fillStaleAfter
}

// fillWanted is the part of fillNeeded that does not look at the sample:
// media control is on and no tray app in the target session reports.
func fillWanted(now time.Time) bool {
	return getConfig().Media.Enabled && !trayReporting(now)
}

// requestFill starts a background reading of store k when one is needed
// and the gate allows it. force is a target-session change: the interval
// does not apply, and a reading still running for the old session is
// followed by one more. It never waits for the reading.
func requestFill(k fillKind, force bool) {
	now := clock.audio()
	if !fillNeeded(k, now) {
		return
	}
	f := &fills[k]
	if !f.gate.Begin(now, fillEvery, force) {
		return
	}
	sys.startFill(func() { f.run(k) })
}

// requestSessionFills is requestFill for both stores.
func requestSessionFills(force bool) {
	requestFill(fillAudio, force)
	requestFill(fillMedia, force)
}

// fillOnTargetChange is what observeTargetSession calls when the target
// moved to a logged-in session: requestSessionFills(true). It is set in
// init because the readings themselves look the target up, which would
// otherwise be an initialization cycle (userRun → … → targetUserSession).
var fillOnTargetChange func()

func init() { fillOnTargetChange = func() { requestSessionFills(true) } }

// startSessionFill reads both stores once fillStartDelay after the start,
// when someone is logged in by then.
func startSessionFill(stop <-chan struct{}) {
	go func() {
		select {
		case <-time.After(fillStartDelay):
		case <-stop:
			return
		}
		if sys.sessionPresent() {
			requestSessionFills(false)
		}
	}()
}

// run reads store k until the gate asks for no more repeats. A repeat
// follows a target change during the reading, which may have read — and
// stored — the old session, so it reads whatever the sample's age; only
// media control having been turned off, or a tray app of the new target
// having reported meanwhile, makes it unnecessary.
func (f *sessionFill) run(k fillKind) {
	need := fillNeeded(k, clock.audio())
	for {
		if need {
			f.report(k, readSessionInto(k))
		}
		if !f.gate.End(clock.audio()) {
			return
		}
		need = fillWanted(clock.audio())
	}
}

// report logs the first failure of a run of failures and the recovery.
// Nobody logged in is no failure: the session went away under the run.
func (f *sessionFill) report(k fillKind, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case err == nil:
		if f.failing {
			logMsg("%s state read from the user session again", k)
		}
		f.failing = false
	case errors.Is(err, errNoUserSession):
	case !f.failing:
		f.failing = true
		logMsg("%s state from the user session: %v (status reports it unavailable)", k, err)
	}
}

// readSessionInto runs one reading of store k, stores it and pushes the
// change.
func readSessionInto(k fillKind) error {
	ctx, cancel := context.WithTimeout(context.Background(), userActions.Timeout+time.Second)
	defer cancel()
	if k == fillAudio {
		return fillAudioSample(ctx)
	}
	return fillMediaSample(ctx)
}

// fillAudioSample reads the audio state. runUserAction already stored the
// reply; storing it here as well changes nothing then (same state, newer
// time) and keeps the store right for a runner that does not. The first
// sample is a baseline to recordAudioSample, but the hub was told
// available=false until now, so the reading that ends that pushes
// audio.changed itself; a later reading that differs pushes through
// recordAudioSample.
func fillAudioSample(ctx context.Context) error {
	_, _, had := dev.audio.Last()
	a, err := readAudioNow(ctx)
	if err != nil {
		return err
	}
	recordAudioSample(a, clock.audio())
	if !had {
		if s, ok := currentAudio(); ok {
			emitAudioChanged(s.Audio)
		}
	}
	return nil
}

// fillMediaSample reads the media session (`media info`); the opt-in is
// applied on storing. Status showed "none" while there was no fresh
// sample, so a reading that ends that pushes media.changed — unless
// recordMediaSample did already (the stale sample it compared with
// differed) or there is still nothing playing.
func fillMediaSample(ctx context.Context) error {
	_, hadFresh := currentMedia()
	prev, _, hadAny := dev.media.Last()
	np, err := readNowPlayingNow(ctx)
	if err != nil {
		return err
	}
	recordMediaSample(np, clock.audio())
	if hadFresh {
		return nil
	}
	if s, ok := currentMedia(); ok && s.Status != useraction.MediaNone && (!hadAny || prev == s.NowPlaying) {
		emitDevice("media", "changed", mediaEventFields(s.NowPlaying))
	}
	return nil
}

// resetSessionFills forgets the gates, the failure state and the tray
// report (tests).
func resetSessionFills() {
	for i := range fills {
		fills[i].gate.Reset()
		fills[i].mu.Lock()
		fills[i].failing = false
		fills[i].mu.Unlock()
	}
	dev.tray.Reset()
}
