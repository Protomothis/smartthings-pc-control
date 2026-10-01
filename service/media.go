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
	"net/http"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
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

// ---- /st/v1 ----------------------------------------------------------------

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

// mediaFeatures are the §3 features entries media.enabled turns on, plus
// "nowplaying" with the media.now_playing opt-in (#117).
func mediaFeatures(cfg Config) []string {
	if !cfg.Media.Enabled {
		return nil
	}
	if cfg.Media.NowPlaying {
		return []string{"audio", "media", "nowplaying"}
	}
	return []string{"audio", "media"}
}

// handleSTMedia runs one volume, mute or media-key command from
// /st/v1/command. The reply is the usual command response plus, for the
// audio commands, the audio block after the change:
//
//	403 {"error":"media_disabled"}             media.enabled is off
//	409 {"error":"no_user_session"}            nobody is logged in
//	400 {"error":"..."}                        value out of range, minutes given
//	501 {"error":"unsupported","message":...}  no playback device
//	502 {"error":"failed","message":...}       the action did not work
//	504 {"error":"timeout"}                    no answer within 3s
func handleSTMedia(w http.ResponseWriter, r *http.Request, name string, body stCommandRequest, from string) {
	if body.Minutes != 0 {
		stError(w, http.StatusBadRequest, "minutes does not apply to "+name)
		return
	}
	res, err := runMediaCommand(r.Context(), name, body.Value)
	if err != nil {
		f := action.Classify(err)
		logMsg("ST API: %s from %s failed: %v", name, from, err)
		if f.Detail == "" {
			stError(w, f.Status, f.Code)
		} else {
			httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Detail})
		}
		return
	}
	logMsg("ST API: %s from %s", name, from)
	resp := stCommandResponse{Accepted: true, Executed: true, Schedule: stScheduleView()}
	if action.IsAudio(name) && res.Audio != nil {
		// The reply's own reading, stamped now: the store may hold it or
		// a newer heartbeat, and the caller asked about this command.
		view := stAudioView(audioSample{Audio: *res.Audio, UpdatedAt: audioNow()})
		resp.Audio = &view
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
