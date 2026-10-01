package service

// Now playing (#117, docs/design/media-notify.md §15). The tray app reads
// the system media session every 3s and posts it with the heartbeat when
// it changes; a `user-action media info` run (Telegram /np, the refresh
// after a media command) stores its answer too. Newer sampled_at wins, as
// for the audio store.
//
// What is kept and published follows two switches: media.enabled gates the
// whole block (as it gates the commands), and the opt-in media.now_playing
// decides whether title, artist, album and app go with the status. The
// status alone is shared whenever media.enabled is on — it is what makes a
// play/pause button show the right symbol. The opt-in is applied when a
// sample is stored and again when it is shown, so turning it off hides a
// title that was stored a moment earlier.
//
// Like the audio state it is device state: media.changed goes to the hub
// (bus taps only) and never becomes a Telegram notification.

import (
	"context"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// mediaSampleTTL is how long a sample describes the session. The tray app
// posts one at least every 30s, so a quiet minute and a half means it is
// gone (logged off, quit) and "playing" can no longer be vouched for.
const mediaSampleTTL = 90 * time.Second

// mediaSample is one stored reading and when it was taken.
type mediaSample struct {
	useraction.NowPlaying
	UpdatedAt time.Time
}

// shareNowPlaying applies the opt-in: np as it may be stored and shown.
func shareNowPlaying(np useraction.NowPlaying, cfg Config) useraction.NowPlaying {
	if !cfg.Media.NowPlaying {
		return np.StatusOnly()
	}
	return np
}

// noteMediaSampleChange stores np taken at at unless the stored sample is
// newer, and reports whether it stored it and whether that changed the
// stored state. The first sample since the service started is a baseline,
// not a change.
func noteMediaSampleChange(np useraction.NowPlaying, at time.Time) (stored, changed bool) {
	return dev.media.Note(np, at)
}

// recordMediaSample stores a reading (opt-in applied, newer wins) and
// pushes media.changed when the stored state is different.
func recordMediaSample(np useraction.NowPlaying, at time.Time) bool {
	np = shareNowPlaying(np, getConfig())
	stored, changed := noteMediaSampleChange(np, at)
	if changed {
		emitDevice("media", "changed", mediaEventFields(np))
	}
	return stored
}

// mediaEventFields is the media.changed data: the status and whichever of
// the (already opt-in filtered) text fields are known.
func mediaEventFields(np useraction.NowPlaying) map[string]string {
	fields := map[string]string{"status": np.Status}
	for k, v := range map[string]string{"title": np.Title, "artist": np.Artist, "album": np.Album, "app": np.App} {
		if v != "" {
			fields[k] = v
		}
	}
	return fields
}

// currentMedia returns the newest reading while it is fresh (see
// mediaSampleTTL); ok is false otherwise.
func currentMedia() (mediaSample, bool) {
	np, at, ok := dev.media.Fresh(audioNow(), mediaSampleTTL)
	return mediaSample{NowPlaying: np, UpdatedAt: at}, ok
}

// resetMediaSample forgets the stored reading (tests).
func resetMediaSample() { dev.media.Reset() }

// noteMediaCommand folds a media key reply into the store. The session
// backend says what state it left the session in ("status") and whose it
// is ("app"); the stored track is kept when it is the same app, so the
// play button flips at once and the title stays until the refresh.
func noteMediaCommand(res UserActionResult) {
	status, app := res.ReplyString("status"), res.ReplyString("app")
	if res.ReplyString("via") != "session" || !useraction.ValidMediaStatus(status) {
		return
	}
	np := useraction.NowPlaying{Status: status, App: app}
	if last, ok := currentMedia(); ok && (last.App == "" || last.App == app) {
		np = last.NowPlaying
		np.Status = status
		if np.App == "" {
			np.App = app
		}
	}
	if np.Validate() == nil {
		recordMediaSample(np, audioNow())
	}
}

// mediaRefreshDelay is how long after a media command the session is read
// again: a player takes a moment to publish the next track's title.
const mediaRefreshDelay = 1200 * time.Millisecond

// scheduleMediaRefresh reads the session once more after a media command
// (`media info`, stored by runUserAction), so the new track reaches status
// and the hub even when no tray app is running. Tests replace it.
var scheduleMediaRefresh = func() {
	go func() {
		time.Sleep(mediaRefreshDelay)
		ctx, cancel := context.WithTimeout(context.Background(), userActions.Timeout+time.Second)
		defer cancel()
		if _, err := readNowPlayingNow(ctx); err != nil {
			logMsg("media refresh after a command: %v", err)
		}
	}()
}

// readNowPlayingNow asks the user session for the current session (`media
// info`); runUserAction stores the answer. The result is not yet filtered
// by the opt-in: callers decide what to show.
func readNowPlayingNow(ctx context.Context) (useraction.NowPlaying, error) {
	if !getConfig().Media.Enabled {
		return useraction.NowPlaying{}, action.ErrMediaDisabled
	}
	res, err := runUserActionFn(ctx, "media", useraction.MediaInfo)
	if err != nil {
		return useraction.NowPlaying{}, err
	}
	np, ok := res.NowPlaying()
	if !ok {
		return useraction.NowPlaying{}, errUserActionOutput
	}
	return np, nil
}

// ---- status ----------------------------------------------------------------

// stMediaStatus builds the media block for cfg.
func stMediaStatus(cfg Config) stMedia {
	if !cfg.Media.Enabled {
		return stMedia{Status: useraction.MediaNone}
	}
	s, ok := currentMedia()
	if !ok || !audioSessionPresent() {
		return stMedia{Status: useraction.MediaNone}
	}
	np := shareNowPlaying(s.NowPlaying, cfg)
	return stMedia{
		Status:    np.Status,
		Title:     np.Title,
		Artist:    np.Artist,
		Album:     np.Album,
		App:       np.App,
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
}
