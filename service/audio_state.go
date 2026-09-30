package service

// The last known state of the user's default playback device (#103). Two
// sources write it: the tray heartbeat every 30s (the user also changes the
// volume with the keyboard, so the service has to be told), and the reply
// of a user-action that touched the audio (so a command's effect shows up
// at once rather than on the next heartbeat). Status and the audio
// commands (#104) read it through currentAudio.

import (
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
	audioMu.Lock()
	defer audioMu.Unlock()
	if !audioLast.UpdatedAt.IsZero() && at.Before(audioLast.UpdatedAt) {
		return false
	}
	audioLast = audioSample{Audio: a, UpdatedAt: at}
	return true
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
