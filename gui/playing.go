package gui

// Is sound coming out of the speakers (#159)? The service cannot tell: it
// runs in session 0, and a one-shot `user-action` run in the user's
// session sees one ~10 ms slice of the meter, far too little to say
// "playing" about music with quiet passages. This app runs in the user's
// session all along, so — like the volume and the media session it
// already reports (idle.go) — it reads the default playback device's peak
// meter in-process every playingSampleInterval and turns the readings
// into a flag:
//
//   - playing as soon as one reading is above playingThreshold,
//   - not playing after playingHold of readings at or below it,
//   - unknown while the meter cannot be read (no playback device, a Core
//     Audio failure) or media control is off.
//
// The flag rides in the heartbeat's audio block. A change posts at once
// through the 3s change check (watchMediaChanges), and the 30s heartbeat
// repeats it so the service keeps it fresh; the service spaces out the
// hub pushes a change of the flag alone sets off (playingPushGap).

import (
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// playingSampleInterval is how often the peak meter is read.
	playingSampleInterval = 300 * time.Millisecond
	// playingThreshold is the peak above which a reading counts as sound:
	// about -60 dBFS, above the noise floor of a silent stream and below
	// any audible playback.
	playingThreshold = 0.001
	// playingHold is how long the readings must stay silent before the
	// flag turns off: the gap between two songs, or a pause in speech,
	// keeps it on.
	playingHold = 5 * time.Second
	// playingSwitchEvery is how many samples pass between two reads of the
	// media.enabled switch (tray.json): every 3s rather than every sample.
	playingSwitchEvery = int(mediaWatchInterval / playingSampleInterval)
)

// playState is the flag: unknown, idle or playing.
type playState int8

const (
	playUnknown playState = iota
	playIdle
	playOn
)

// playingTracker turns peak readings into the flag. Pure; the meter loop
// owns it.
type playingTracker struct {
	state    playState
	lastLoud time.Time
}

// observe takes one reading (peak, or err when the meter could not be
// read) at now and reports whether the flag changed.
func (p *playingTracker) observe(peak float32, err error, now time.Time) (changed bool) {
	next := playIdle
	switch {
	case err != nil:
		next = playUnknown
	case peak > playingThreshold:
		next = playOn
		p.lastLoud = now
	case p.state == playOn && now.Sub(p.lastLoud) < playingHold:
		next = playOn
	}
	changed = next != p.state
	p.state = next
	return changed
}

// playingFlag is the flag the heartbeat sends: nil while unknown.
func playingFlag(s playState) *bool {
	if s == playUnknown {
		return nil
	}
	on := s == playOn
	return &on
}

// playing is the current flag, shared by the meter loop and the heartbeat.
var playing struct {
	mu    sync.Mutex
	state playState
}

func setPlaying(s playState) {
	playing.mu.Lock()
	playing.state = s
	playing.mu.Unlock()
}

// currentPlaying is the flag for the heartbeat's audio block.
func currentPlaying() *bool {
	playing.mu.Lock()
	defer playing.mu.Unlock()
	return playingFlag(playing.state)
}

// Seams of the meter loop, replaced by the tests.
var (
	readPeak   = useraction.ReadPeak
	playingNow = time.Now
	// playingChanged runs the 3s change check at once, which posts the
	// audio block with the new flag.
	playingChanged = func(u *ui) { go u.mediaTick() }
	playingTick    = func() (<-chan time.Time, func()) {
		t := time.NewTicker(playingSampleInterval)
		return t.C, t.Stop
	}
)

// playingLoop reads the meter until the app quits. A change of the flag
// runs the 3s change check at once, which posts the audio block with it.
func (u *ui) playingLoop() {
	tick, stop := playingTick()
	defer stop()
	var tr playingTracker
	enabled, n := heartbeatMediaEnabled(), 0
	for {
		select {
		case <-u.quit:
			return
		case <-tick:
		}
		if n++; n >= playingSwitchEvery {
			enabled, n = heartbeatMediaEnabled(), 0
		}
		var changed bool
		if enabled {
			peak, err := readPeak()
			changed = tr.observe(peak, err, playingNow())
		} else if tr.state != playUnknown {
			// Nothing is sent while media control is off; start afresh
			// once it is on again.
			tr, changed = playingTracker{}, true
		}
		if changed {
			setPlaying(tr.state)
			playingChanged(u)
		}
	}
}
