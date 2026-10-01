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
// would be noise, §3). SmartThings and Telegram share mediaCommandArgs and
// runMediaCommand, so both apply the same ranges and the same media.enabled
// switch.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// MediaConfig is the "media" object in config.json (§4). There is nothing
// to normalise: a missing key keeps the default (on) because loadConfig
// decodes over defaultConfig.
type MediaConfig struct {
	// Enabled allows the volume, mute and media-key commands. Default on.
	Enabled bool `json:"enabled"`
	// NowPlaying (#117) shares the title, artist, album and app of the
	// playing media in status, pushes and Telegram. Opt-in, default off;
	// the playback status alone follows Enabled (nowplaying.go).
	NowPlaying bool `json:"now_playing"`
}

// defaultVolumeStep is volumeup/volumedown without a value (§3), and the
// Windows volume keys' own step.
const defaultVolumeStep = 5

// errMediaDisabled is returned while media.enabled is off; /st/v1 answers
// 403 media_disabled.
var errMediaDisabled = errors.New("media_disabled")

// errMediaValue is a value outside the command's range (400).
type errMediaValue struct{ msg string }

func (e *errMediaValue) Error() string { return e.msg }

// mediaCommandKinds are the /st/v1 command names, true for the audio ones
// (their reply carries the new audio state), false for the media keys.
var mediaCommandKinds = map[string]bool{
	"volume":     true,
	"volumeup":   true,
	"volumedown": true,
	"mute":       true,
	"unmute":     true,
	"playpause":  false,
	"play":       false,
	"pause":      false,
	"stop":       false,
	"next":       false,
	"prev":       false,
}

// isMediaCommand reports whether name is one of mediaCommandKinds.
func isMediaCommand(name string) bool {
	_, ok := mediaCommandKinds[name]
	return ok
}

// mediaCommandArgs builds the user-action argument vector for a command:
//
//	volume      value 0–100, required
//	volumeup    value 1–100, default 5
//	volumedown  value 1–100, default 5
//	mute        audio mute on      (value ignored)
//	unmute      audio mute off
//	play…prev   media <key>        (the session tells play from pause, #117)
func mediaCommandArgs(name string, value *int) ([]string, error) {
	switch name {
	case "volume":
		if value == nil {
			return nil, &errMediaValue{"volume needs a value 0-100"}
		}
		if *value < 0 || *value > 100 {
			return nil, &errMediaValue{"value must be between 0 and 100"}
		}
		return []string{"audio", "set", strconv.Itoa(*value)}, nil
	case "volumeup", "volumedown":
		step := defaultVolumeStep
		if value != nil {
			step = *value
		}
		if step < 1 || step > 100 {
			return nil, &errMediaValue{"value must be between 1 and 100"}
		}
		sign := "+"
		if name == "volumedown" {
			sign = "-"
		}
		return []string{"audio", "step", sign + strconv.Itoa(step)}, nil
	case "mute":
		return []string{"audio", "mute", "on"}, nil
	case "unmute":
		return []string{"audio", "mute", "off"}, nil
	case "playpause", "play", "pause", "stop", "next", "prev":
		return []string{"media", name}, nil
	}
	return nil, fmt.Errorf("not a media command: %q", name)
}

// runUserActionFn is runUserAction, replaced by the tests.
var runUserActionFn = runUserAction

// runMediaCommand checks media.enabled, builds the arguments and runs them
// in the user session. err is errMediaDisabled, *errMediaValue, or one of
// runUserAction's errors (errNoUserSession, errUserActionTimeout,
// *userActionError, ...).
func runMediaCommand(ctx context.Context, name string, value *int) (UserActionResult, error) {
	if !getConfig().Media.Enabled {
		return UserActionResult{}, errMediaDisabled
	}
	args, err := mediaCommandArgs(name, value)
	if err != nil {
		return UserActionResult{}, err
	}
	res, err := runUserActionFn(ctx, args...)
	if err == nil && !mediaCommandKinds[name] {
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
		return useraction.Audio{}, errMediaDisabled
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

// stAudio is the §3 status block. Available is false — and every other
// key absent — while there is nothing trustworthy to report: nobody is
// logged in, no sample has arrived since the service started, or
// media.enabled is off. Device is a pointer so that a device without a
// name still reports "" rather than dropping the key.
type stAudio struct {
	Available bool    `json:"available"`
	Volume    *int    `json:"volume,omitempty"`
	Muted     *bool   `json:"muted,omitempty"`
	Device    *string `json:"device,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
}

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
		status, code, msg := mediaErrorStatus(err)
		logMsg("ST API: %s from %s failed: %v", name, from, err)
		if msg == "" {
			stError(w, status, code)
		} else {
			writeJSON(w, status, map[string]string{"error": code, "message": msg})
		}
		return
	}
	logMsg("ST API: %s from %s", name, from)
	resp := stCommandResponse{Accepted: true, Executed: true, Schedule: stScheduleView()}
	if mediaCommandKinds[name] && res.Audio != nil {
		// The reply's own reading, stamped now: the store may hold it or
		// a newer heartbeat, and the caller asked about this command.
		view := stAudioView(audioSample{Audio: *res.Audio, UpdatedAt: audioNow()})
		resp.Audio = &view
	}
	writeJSON(w, http.StatusOK, resp)
}

// mediaErrorStatus maps a runMediaCommand error to its HTTP status, error
// code and (optional) message.
func mediaErrorStatus(err error) (status int, code, msg string) {
	var valErr *errMediaValue
	var uaErr *userActionError
	switch {
	case errors.Is(err, errMediaDisabled):
		return http.StatusForbidden, "media_disabled", ""
	case errors.Is(err, errNoUserSession):
		return http.StatusConflict, "no_user_session", ""
	case errors.As(err, &valErr):
		return http.StatusBadRequest, valErr.msg, ""
	case errors.Is(err, errUserActionTimeout):
		return http.StatusGatewayTimeout, "timeout", ""
	case errors.As(err, &uaErr):
		if uaErr.Code == useraction.CodeUnsupported {
			return http.StatusNotImplemented, "unsupported", uaErr.Message
		}
		return http.StatusBadGateway, "failed", uaErr.Message
	}
	// A start failure or unreadable output: the details (paths, child
	// output) stay in service.log.
	return http.StatusBadGateway, "failed", ""
}
