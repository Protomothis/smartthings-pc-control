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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

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

var (
	mediaMu   sync.Mutex
	mediaLast mediaSample
)

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
	mediaMu.Lock()
	defer mediaMu.Unlock()
	if !mediaLast.UpdatedAt.IsZero() && at.Before(mediaLast.UpdatedAt) {
		return false, false
	}
	changed = !mediaLast.UpdatedAt.IsZero() && mediaLast.NowPlaying != np
	mediaLast = mediaSample{NowPlaying: np, UpdatedAt: at}
	return true, changed
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
	mediaMu.Lock()
	defer mediaMu.Unlock()
	if mediaLast.UpdatedAt.IsZero() || audioNow().Sub(mediaLast.UpdatedAt) > mediaSampleTTL {
		return mediaSample{}, false
	}
	return mediaLast, true
}

// resetMediaSample forgets the stored reading (tests).
func resetMediaSample() {
	mediaMu.Lock()
	mediaLast = mediaSample{}
	mediaMu.Unlock()
}

// nowPlaying reads the "media" object of a `media info` reply. A media key
// reply has a string there ("media":"next") and reads as ok=false, as does
// anything that fails Validate.
func (r UserActionResult) nowPlaying() (useraction.NowPlaying, bool) {
	raw := bytes.TrimSpace(r.Fields["media"])
	if len(raw) == 0 || raw[0] != '{' {
		return useraction.NowPlaying{}, false
	}
	var np useraction.NowPlaying
	if json.Unmarshal(raw, &np) != nil || np.Validate() != nil {
		return useraction.NowPlaying{}, false
	}
	return np, true
}

// replyString reads a string field of a reply, "" when absent.
func (r UserActionResult) replyString(key string) string {
	var s string
	if raw, ok := r.Fields[key]; ok {
		json.Unmarshal(raw, &s)
	}
	return s
}

// noteMediaCommand folds a media key reply into the store. The session
// backend says what state it left the session in ("status") and whose it
// is ("app"); the stored track is kept when it is the same app, so the
// play button flips at once and the title stays until the refresh.
func noteMediaCommand(res UserActionResult) {
	status, app := res.replyString("status"), res.replyString("app")
	if res.replyString("via") != "session" || !useraction.ValidMediaStatus(status) {
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
		ctx, cancel := context.WithTimeout(context.Background(), userActionTimeout+time.Second)
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
		return useraction.NowPlaying{}, errMediaDisabled
	}
	res, err := runUserActionFn(ctx, "media", useraction.MediaInfo)
	if err != nil {
		return useraction.NowPlaying{}, err
	}
	np, ok := res.nowPlaying()
	if !ok {
		return useraction.NowPlaying{}, errUserActionOutput
	}
	return np, nil
}

// ---- status ----------------------------------------------------------------

// stMedia is the §15 status block. status is always there: "none" while no
// session plays and also while nothing trustworthy is known (media.enabled
// off, nobody logged in, no fresh sample). The text fields appear only with
// the media.now_playing opt-in and only when the app set them.
type stMedia struct {
	Status    string `json:"status"`
	Title     string `json:"title,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Album     string `json:"album,omitempty"`
	App       string `json:"app,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

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

// ---- /api/media (the desktop app's media card) -----------------------------

// mediaAPIBody is GET /api/media and the reply of POST /api/media: the two
// switches, whether anyone is logged in, and the audio and media blocks
// exactly as /st/v1/status shows them.
type mediaAPIBody struct {
	Enabled    bool    `json:"enabled"`
	NowPlaying bool    `json:"now_playing"`
	Session    bool    `json:"session"`
	Audio      stAudio `json:"audio"`
	Media      stMedia `json:"media"`
}

func mediaAPIView(cfg Config) mediaAPIBody {
	return mediaAPIBody{
		Enabled:    cfg.Media.Enabled,
		NowPlaying: cfg.Media.NowPlaying,
		Session:    audioSessionPresent(),
		Audio:      stAudioStatus(cfg),
		Media:      stMediaStatus(cfg),
	}
}

// handleMediaAPI serves /api/media for the command tab's media card:
//
//	GET                                 the state
//	POST {"command":"next"}             one media command, then the state
//	POST {"command":"volume","value":30}
//
// The commands are the /st/v1 ones (mediaCommandKinds) with the same
// ranges, switch and errors: 403 media_disabled, 409 no_user_session,
// 400, 501 unsupported, 502 failed, 504 timeout.
func handleMediaAPI(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r, getConfig().Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, mediaAPIView(getConfig()))
		return
	case http.MethodPost:
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Command string `json:"command"`
		Value   *int   `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !isMediaCommand(body.Command) {
		writeAPIError(w, http.StatusBadRequest, "unknown media command")
		return
	}
	res, err := runMediaCommand(r.Context(), body.Command, body.Value)
	if err != nil {
		status, code, msg := mediaErrorStatus(err)
		logMsg("App: %s failed: %v", body.Command, err)
		writeJSON(w, status, map[string]string{"error": code, "message": msg})
		return
	}
	view := mediaAPIView(getConfig())
	if res.Audio != nil {
		// The reply's own reading: the store may already hold a newer
		// heartbeat, but the caller asked about this command.
		view.Audio = stAudioView(audioSample{Audio: *res.Audio, UpdatedAt: audioNow()})
	}
	writeJSON(w, http.StatusOK, view)
}
