package service

// Volume, mute and media keys (#104, #105; docs/design/media-notify.md
// §3, §6). All of them only mean something in the logged-in user's
// session, so each command becomes one `user-action audio|media …` run
// there (useraction.go); the reply's audio block updates the store in
// audio_state.go, which pushes audio.changed.
//
// These are not registry commands (commands.go): they take an argument,
// run at once — no grace period, no schedule — and are neither recorded as
// last_command nor notified to Telegram (a notification per slider step
// would be noise, §3). SmartThings and Telegram share action.MediaArgs and
// runMediaCommand, so both apply the same ranges and the same media.enabled
// switch.

import (
	"context"
	"fmt"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// runUserActionFn is runUserAction, replaced by the tests.
var runUserActionFn = runUserAction

// runMediaCommand checks media.enabled, builds the arguments and runs them
// in the user session. err is action.ErrMediaDisabled, *action.ValueError, or one of
// runUserAction's errors (errNoUserSession, errUserActionTimeout,
// *userActionError, ...).
func runMediaCommand(ctx context.Context, name string, value *int) (UserActionResult, error) {
	if !getConfig().Media.Enabled {
		return UserActionResult{}, action.ErrMediaDisabled
	}
	args, err := action.MediaArgs(name, value)
	if err != nil {
		return UserActionResult{}, err
	}
	res, err := runUserActionFn(ctx, args...)
	if err == nil && !action.IsAudio(name) {
		// A media key changes what plays: show the new state at once and
		// read the session again once the player has caught up (#117).
		noteMediaCommand(res)
		scheduleMediaRefresh()
	}
	return res, err
}

// readAudioNow asks the user session for the current state (`audio get`),
// for Telegram's /vol without an argument.
func readAudioNow(ctx context.Context) (useraction.Audio, error) {
	if !getConfig().Media.Enabled {
		return useraction.Audio{}, action.ErrMediaDisabled
	}
	res, err := runUserActionFn(ctx, "audio", "get")
	if err != nil {
		return useraction.Audio{}, err
	}
	if res.Audio == nil {
		return useraction.Audio{}, fmt.Errorf("%w: audio get without an audio block", errUserActionOutput)
	}
	return *res.Audio, nil
}

// ---- status ----------------------------------------------------------------

// audioSessionPresent reports whether someone is logged in; a var so the
// status tests do not depend on the machine they run on. It goes through
// targetUserSession, so a status read also drops the samples of a session
// the commands no longer act on.
var audioSessionPresent = func() bool {
	_, err := targetUserSession()
	return err == nil
}

// stAudioStatus builds the audio block for cfg.
func stAudioStatus(cfg Config) stAudio {
	if !cfg.Media.Enabled {
		return stAudio{}
	}
	s, ok := currentAudio()
	if !ok || !audioSessionPresent() {
		return stAudio{}
	}
	return stAudioView(s)
}

func stAudioView(s audioSample) stAudio {
	volume, muted, device := s.Volume, s.Muted, s.Device
	return stAudio{
		Available: true,
		Volume:    &volume,
		Muted:     &muted,
		Device:    &device,
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
}
