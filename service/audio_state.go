package service

// The last known state of the user's default playback device (#103). Two
// sources write it: the tray heartbeat every 30s (the user also changes the
// volume with the keyboard, so the service has to be told), and the reply
// of a user-action that touched the audio (so a command's effect shows up
// at once rather than on the next heartbeat). Status and the audio
// commands (#104) read it through currentAudio.

import (
	"strconv"
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
// (#104): a stored sample whose volume, mute or device differs from the
// one before it — the first sample since the service started is a
// baseline, not a change. The heartbeat and the command replies both come
// through here, so a volume changed with the keyboard reaches the
// SmartThings slider within one heartbeat, and a command's effect at once.
// Like the display state it is device state: bus taps only, never a
// Telegram notification.
func recordAudioSample(a useraction.Audio, at time.Time) bool {
	stored, changed := dev.audio.Note(a, at)
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
	a, at, ok := dev.audio.Last()
	return audioSample{Audio: a, UpdatedAt: at}, ok
}

// resetAudioSample forgets the stored reading (tests).
func resetAudioSample() { dev.audio.Reset() }
