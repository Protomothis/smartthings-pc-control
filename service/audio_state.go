package service

// The last known state of the user's default playback device (#103). Two
// sources write it: the tray heartbeat every 30s (the user also changes the
// volume with the keyboard, so the service has to be told), and the reply
// of a user-action that touched the audio (so a command's effect shows up
// at once rather than on the next heartbeat). Status and the audio
// commands (#104) read it through currentAudio.
//
// Since #159 the sample also carries the default recording device's name
// and mute, and next to it the service keeps "playing": whether sound is
// coming out of the speakers. Only the tray app can tell — it reads the
// playback peak meter in the user's session a few times a second
// (gui/playing.go) — so playing comes with its heartbeat alone, is null
// without a tray app reporting, and is kept apart from the sample: a
// command reply must not turn it back into "unknown".

import (
	"strconv"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/devstate"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// dev is what the service knows about the PC's devices between readings
// (service/devstate): one value for what used to be a mutex and a variable
// per store. The battery monitor is the battery variable (battery.go).
var dev = struct {
	audio   devstate.Sample[useraction.Audio]
	media   devstate.Sample[useraction.NowPlaying]
	idle    devstate.Sample[int64]
	display *devstate.Value[string]
	target  devstate.SessionTracker
	// tray is when a heartbeat from the target session last arrived: while
	// one is recent the tray app supplies audio and media, and the service
	// does not read them itself (session_fill.go).
	tray devstate.Sample[struct{}]
	// playing is the tray app's playing flag (#159), see playState.
	playing devstate.Sample[playState]
}{
	// "unknown" until a screen command has run in this process.
	display: devstate.NewValue("unknown"),
}

// audioSample is one stored reading and when it was taken.
type audioSample struct {
	useraction.Audio
	UpdatedAt time.Time
}

// noteAudioSample stores a reading taken at at, unless the stored one is
// newer: a heartbeat and a command reply can race, and the older of the
// two must not overwrite the newer. It reports whether the sample was
// stored. Equal times store (the later call wins).
func noteAudioSample(a useraction.Audio, at time.Time) bool {
	stored, _ := dev.audio.Note(a, at)
	return stored
}

// recordAudioSample stores a reading (newer wins, see noteAudioSample) and
// pushes audio.changed to the hub when the state it stored is different
// (#104): a stored sample whose volume, mute, device or recording device
// differs from the one before it — the first sample since the service
// started is a baseline, not a change. The heartbeat and the command
// replies both come through here, so a volume changed with the keyboard
// reaches the SmartThings slider within one heartbeat, and a command's
// effect at once. Like the display state it is device state: bus taps
// only, never a Telegram notification.
func recordAudioSample(a useraction.Audio, at time.Time) bool {
	stored, _ := recordAudio(a, at)
	return stored
}

// recordAudio is recordAudioSample that also says whether it pushed.
func recordAudio(a useraction.Audio, at time.Time) (stored, changed bool) {
	stored, changed = dev.audio.Note(a, at)
	if changed {
		emitAudioChanged(a)
	}
	return stored, changed
}

// emitAudioChanged pushes audio.changed for a. The data carries the
// recording device and playing while they are known (#159); the status
// in the push has them either way.
func emitAudioChanged(a useraction.Audio) {
	fields := map[string]string{
		"volume": strconv.Itoa(a.Volume),
		"muted":  strconv.FormatBool(a.Muted),
		"device": a.Device,
	}
	if a.Input.Present {
		fields["input_device"] = a.Input.Device
		fields["input_muted"] = strconv.FormatBool(a.Input.Muted)
	}
	if p := currentPlaying(); p != nil {
		fields["playing"] = strconv.FormatBool(*p)
	}
	audioPush.sent()
	emitDevice("audio", "changed", fields)
}

// currentAudio returns the newest reading; ok is false when there has been
// none since the service started. How old is too old to report is the
// caller's decision (UpdatedAt is there for it).
func currentAudio() (audioSample, bool) {
	a, at, ok := dev.audio.Last()
	return audioSample{Audio: a, UpdatedAt: at}, ok
}

// resetAudioSample forgets the stored reading (tests).
func resetAudioSample() { dev.audio.Reset() }

// ---- playing (#159) -----------------------------------------------------------

// playState is the tray app's playing flag. The zero value is unknown: the
// tray app could not read the meter, sent none (older than v1.3.0), or
// has not reported yet.
type playState int8

const (
	playUnknown playState = iota
	playIdle
	playOn
)

func playStateOf(p *bool) playState {
	switch {
	case p == nil:
		return playUnknown
	case *p:
		return playOn
	}
	return playIdle
}

// playingTTL is how long a playing flag stays valid without a heartbeat:
// the idle time's (the tray app sends the flag with every 30s post).
const playingTTL = idleHeartbeatTTL

// playingPushGap is the least time between an audio.changed and one that
// only playing set off (#159): music with pauses must not page the hub
// every second. A change inside the gap is pushed when it ends, with the
// state of that moment.
var playingPushGap = 2 * time.Second

// notePlaying stores the flag of a heartbeat taken at at (newer wins) and
// reports whether it differs from the flag stored before. Like the audio
// sample, the first flag since the service started (or the session
// changed) is a baseline, not a change.
func notePlaying(p *bool, at time.Time) (changed bool) {
	_, changed = dev.playing.Note(playStateOf(p), at)
	return changed
}

// currentPlaying is the playing flag while a tray app reports it, nil
// otherwise.
func currentPlaying() *bool {
	s, _, ok := dev.playing.Fresh(clock.audio(), playingTTL)
	if !ok || s == playUnknown {
		return nil
	}
	on := s == playOn
	return &on
}

// audioPush spaces out the pushes playing sets off.
var audioPush pushSpacer

// pushSpacer remembers when audio.changed last went out and holds at most
// one delayed push.
type pushSpacer struct {
	mu    sync.Mutex
	last  time.Time
	timer *time.Timer
}

// sent records an audio.changed going out now: it carries the current
// playing flag, so a delayed one is no longer needed.
func (p *pushSpacer) sent() {
	p.mu.Lock()
	p.last = time.Now()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
}

// playingChanged pushes audio.changed for a change of playing alone: now,
// or when playingPushGap since the last push has passed. A push already
// waiting covers this change too.
func (p *pushSpacer) playingChanged() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		return
	}
	wait := playingPushGap - time.Since(p.last)
	if wait <= 0 {
		// emitCurrentAudio records the push (sent), which takes p.mu.
		p.mu.Unlock()
		emitCurrentAudio()
		p.mu.Lock()
		return
	}
	var t *time.Timer
	t = time.AfterFunc(wait, func() {
		p.mu.Lock()
		if p.timer != t {
			p.mu.Unlock()
			return // a push went out meanwhile, or the store was reset
		}
		p.timer = nil
		p.mu.Unlock()
		emitCurrentAudio()
	})
	p.timer = t
}

// reset drops a waiting push and the last time (tests, a session change).
func (p *pushSpacer) reset() {
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.last = time.Time{}
	p.mu.Unlock()
}

// emitCurrentAudio pushes audio.changed with the stored sample's data.
func emitCurrentAudio() {
	if s, ok := currentAudio(); ok {
		emitAudioChanged(s.Audio)
		return
	}
	audioPush.sent()
	emitDevice("audio", "changed", map[string]string{})
}

// resetPlaying forgets the flag (a session change, tests).
func resetPlaying() {
	dev.playing.Reset()
	audioPush.reset()
}
