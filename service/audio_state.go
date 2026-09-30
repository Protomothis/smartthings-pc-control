package service

// The last known state of the user's default playback device (#103). Two
// sources write it: the tray heartbeat every 30s (the user also changes the
// volume with the keyboard, so the service has to be told), and the reply
// of a user-action that touched the audio (so a command's effect shows up
// at once rather than on the next heartbeat). Status and the audio
// commands (#104) read it through currentAudio.

import (
	"strconv"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// audioSample is one stored reading and when it was taken.
type audioSample struct {
	useraction.Audio
	UpdatedAt time.Time
}

var (
	audioMu   sync.Mutex
	audioLast audioSample
	// audioNow is time.Now, replaced by the tests.
	audioNow = time.Now
)

// noteAudioSample stores a reading taken at at, unless the stored one is
// newer: a heartbeat and a command reply can race, and the older of the
// two must not overwrite the newer. It reports whether the sample was
// stored. Equal times store (the later call wins).
func noteAudioSample(a useraction.Audio, at time.Time) bool {
	stored, _ := noteAudioSampleChange(a, at)
	return stored
}

// noteAudioSampleChange is noteAudioSample that also reports whether the
// stored state changed: a stored sample whose volume, mute or device
// differs from the one before it. The first sample since the service
// started is a baseline, not a change.
func noteAudioSampleChange(a useraction.Audio, at time.Time) (stored, changed bool) {
	audioMu.Lock()
	defer audioMu.Unlock()
	if !audioLast.UpdatedAt.IsZero() && at.Before(audioLast.UpdatedAt) {
		return false, false
	}
	changed = !audioLast.UpdatedAt.IsZero() && audioLast.Audio != a
	audioLast = audioSample{Audio: a, UpdatedAt: at}
	return true, changed
}

// recordAudioSample stores a reading (newer wins, see noteAudioSample) and
// pushes audio.changed to the hub when the state it stored is different
// (#104). The heartbeat and the command replies both come through here,
// so a volume changed with the keyboard reaches the SmartThings slider
// within one heartbeat, and a command's effect at once. Like the display
// state it is device state: bus taps only, never a Telegram notification.
func recordAudioSample(a useraction.Audio, at time.Time) bool {
	stored, changed := noteAudioSampleChange(a, at)
	if changed {
		emitDevice("audio", "changed", map[string]string{
			"volume": strconv.Itoa(a.Volume),
			"muted":  strconv.FormatBool(a.Muted),
			"device": a.Device,
		})
	}
	return stored
}

// currentAudio returns the newest reading; ok is false when there has been
// none since the service started. How old is too old to report is the
// caller's decision (UpdatedAt is there for it).
func currentAudio() (audioSample, bool) {
	audioMu.Lock()
	defer audioMu.Unlock()
	return audioLast, !audioLast.UpdatedAt.IsZero()
}

// resetAudioSample forgets the stored reading (tests).
func resetAudioSample() {
	audioMu.Lock()
	audioLast = audioSample{}
	audioMu.Unlock()
}
