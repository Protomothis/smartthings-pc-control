package useraction

// Now playing and per-session control (#117, media-notify.md §15). The
// system media session — whatever Windows shows in its media flyout — is
// read and controlled through the WinRT session manager (winrt.go):
//
//	user-action media info
//	{"ok":true,"media":{"status":"playing","title":"…","artist":"…","album":"…","app":"Spotify"}}
//
// and the media verbs go to that session first (the "session" backend in
// front of the media keys, see media.go), which is what finally tells
// play and pause apart.
//
// ReadNowPlaying is the same reader for in-process use: the tray app polls
// it for the media block of its heartbeat. What leaves the machine (title
// and the rest only with the media.now_playing opt-in) is the caller's
// decision; this package reads everything and sends nothing.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// Playback states of NowPlaying.Status.
const (
	MediaPlaying = "playing"
	MediaPaused  = "paused"
	MediaStopped = "stopped"
	// MediaNone means no media session at all.
	MediaNone = "none"
)

// MediaInfo is the read-only media verb.
const MediaInfo = "info"

// MaxMediaTextRunes bounds each text field of NowPlaying. Titles are
// whatever an app sets (a browser passes the tab title), so they are cut
// rather than rejected.
const MaxMediaTextRunes = 200

// maxAppRunes bounds the app name.
const maxAppRunes = 64

// NowPlaying is the current media session. It is the "media" object of a
// `media info` reply and of the tray heartbeat. Empty text fields are
// unknown (or withheld) and left out of the JSON.
type NowPlaying struct {
	Status string `json:"status"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	App    string `json:"app,omitempty"`
}

// ValidMediaStatus reports whether s is one of the Media* states.
func ValidMediaStatus(s string) bool {
	switch s {
	case MediaPlaying, MediaPaused, MediaStopped, MediaNone:
		return true
	}
	return false
}

// Validate checks what a sample must satisfy to be stored.
func (n NowPlaying) Validate() error {
	if !ValidMediaStatus(n.Status) {
		return fmt.Errorf("status %q is not playing, paused, stopped or none", n.Status)
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{{"title", n.Title, MaxMediaTextRunes}, {"artist", n.Artist, MaxMediaTextRunes}, {"album", n.Album, MaxMediaTextRunes}, {"app", n.App, maxAppRunes}} {
		if utf8.RuneCountInString(f.v) > f.max {
			return fmt.Errorf("%s longer than %d characters", f.name, f.max)
		}
		if hasControl(f.v) {
			return fmt.Errorf("%s contains control characters", f.name)
		}
	}
	return nil
}

// StatusOnly is n without the track and the app: what may be shared while
// the media.now_playing opt-in is off.
func (n NowPlaying) StatusOnly() NowPlaying {
	return NowPlaying{Status: n.Status}
}

// mediaSession is the part of the WinRT session the handlers use, behind
// an interface so they can be tested without a player.
type mediaSession interface {
	// AppID is the SourceAppUserModelId, "" when unknown.
	AppID() string
	// Status is one of the Media* states.
	Status() (string, error)
	Properties() (title, artist, album string, err error)
	// Control runs a media verb; false means the app declined it (the
	// control is disabled, say).
	Control(verb string) (bool, error)
	Close()
}

// openSession returns the current session, or nil when there is none;
// tests replace it.
var openSession = openCurrentSession

// ReadNowPlaying returns the current media session. No session is not an
// error: the status is then MediaNone. The error is a *Error: unsupported
// when this Windows has no session API, failed otherwise.
func ReadNowPlaying() (NowPlaying, error) {
	var np NowPlaying
	err := runCOM(func() error {
		s, err := openSession()
		if err != nil {
			return err
		}
		if s == nil {
			np = NowPlaying{Status: MediaNone}
			return nil
		}
		defer s.Close()
		np, err = readSession(s)
		return err
	})
	if err != nil {
		if errors.Is(err, errNoMediaSessionAPI) {
			return NowPlaying{}, Unsupported("%v", err)
		}
		return NowPlaying{}, Failed("media session: %v", err)
	}
	return np, nil
}

// readSession reads status, track and app. The track is best effort: some
// apps publish a status but refuse TryGetMediaPropertiesAsync, and a
// status without a title is still worth reporting.
func readSession(s mediaSession) (NowPlaying, error) {
	status, err := s.Status()
	if err != nil {
		return NowPlaying{}, err
	}
	np := NowPlaying{Status: status}
	if status == MediaNone {
		return np, nil
	}
	np.App = AppDisplayName(s.AppID())
	if title, artist, album, err := s.Properties(); err == nil {
		np.Title = sanitizeMediaText(title, MaxMediaTextRunes)
		np.Artist = sanitizeMediaText(artist, MaxMediaTextRunes)
		np.Album = sanitizeMediaText(album, MaxMediaTextRunes)
	}
	return np, nil
}

// sessionOutcome is what a media verb does to a session in state before:
// the state after it, and whether it is already there (so nothing needs to
// be sent). "pause" on a session that is not playing is a no-op rather
// than a request: if the app declined it, the fallback toggle key would
// start playback, the opposite of what was asked.
func sessionOutcome(verb, before string) (after string, noop bool) {
	switch verb {
	case "play":
		return MediaPlaying, before == MediaPlaying
	case "pause":
		if before != MediaPlaying {
			return before, true
		}
		return MediaPaused, false
	case "playpause":
		if before == MediaPlaying {
			return MediaPaused, false
		}
		return MediaPlaying, false
	case "stop":
		return MediaStopped, before == MediaStopped
	}
	return before, false // next, prev
}

// sendSessionMedia is the "session" media backend: the verb goes straight
// to the current session. It does not handle the verb — so the media keys
// get it — when there is no session or the app declines the request. The
// reply fields are the state the session is left in and its app:
// {"status":"paused","app":"Spotify"}.
func sendSessionMedia(verb string) (bool, map[string]any, error) {
	var (
		handled bool
		fields  map[string]any
	)
	err := runCOM(func() error {
		s, err := openSession()
		if err != nil || s == nil {
			return err
		}
		defer s.Close()
		before, err := s.Status()
		if err != nil {
			return err
		}
		if before == MediaNone {
			return nil
		}
		after, noop := sessionOutcome(verb, before)
		if !noop {
			ok, err := s.Control(verb)
			if err != nil || !ok {
				return err
			}
		}
		handled = true
		fields = map[string]any{"status": after}
		if app := AppDisplayName(s.AppID()); app != "" {
			fields["app"] = app
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errNoMediaSessionAPI) {
			return false, nil, nil // nothing to try: the keys it is
		}
		return false, nil, Failed("media session: %v", err)
	}
	return handled, fields, nil
}

// knownApps maps a fragment of a lower-cased AppUserModelID to the name
// people know the app by. Order matters where fragments overlap. Browsers
// are listed by engine name because a PWA's id carries the browser's
// prefix (Chrome._crx_…), and it is the browser that plays it.
var knownApps = []struct{ fragment, name string }{
	{"spotify", "Spotify"},
	{"msedge", "Edge"},
	{"microsoftedge", "Edge"},
	{"chrome", "Chrome"},
	{"firefox", "Firefox"},
	{"308046b0af4a39cb", "Firefox"}, // Firefox's installer-assigned AUMID
	{"opera", "Opera"},
	{"brave", "Brave"},
	{"whale", "Whale"},
	{"vlc", "VLC"},
	{"foobar2000", "foobar2000"},
	{"potplayer", "PotPlayer"},
	{"aimp", "AIMP"},
	{"musicbee", "MusicBee"},
	{"applemusic", "Apple Music"},
	{"itunes", "iTunes"},
	{"tidal", "TIDAL"},
	{"deezer", "Deezer"},
	{"discord", "Discord"},
	{"melon", "Melon"},
	{"zunemusic", mediaPlayerName},
	{"music.ui", mediaPlayerName},
	{"zunevideo", mediaPlayerName},
}

// mediaPlayerName marks the Windows Media Player entries, whose name is
// localised (see localMediaPlayerName).
const mediaPlayerName = "\x00mediaplayer"

// uiLanguageKorean reports whether the Windows display language is Korean;
// a var for the tests.
var uiLanguageKorean = func() bool {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	return err == nil && len(langs) > 0 && strings.HasPrefix(strings.ToLower(langs[0]), "ko")
}

// localMediaPlayerName is the built-in player's name in the Windows
// display language, as its own Start menu entry shows it.
func localMediaPlayerName() string {
	if uiLanguageKorean() {
		return "미디어 플레이어"
	}
	return "Media Player"
}

// AppDisplayName turns a SourceAppUserModelID into a short name:
// "Spotify.exe" and "SpotifyAB.SpotifyMusic_zpdnekdrzrea0!Spotify" are
// both "Spotify". An unknown app keeps the tail of its id — the part after
// "!" for a packaged app, the file name without ".exe" for a desktop one.
func AppDisplayName(aumid string) string {
	aumid = strings.TrimSpace(aumid)
	if aumid == "" {
		return ""
	}
	lower := strings.ToLower(aumid)
	for _, a := range knownApps {
		if strings.Contains(lower, a.fragment) {
			if a.name == mediaPlayerName {
				return localMediaPlayerName()
			}
			return a.name
		}
	}
	tail := aumid
	if i := strings.LastIndex(tail, "!"); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	tail = filepath.Base(strings.ReplaceAll(tail, "/", `\`))
	if ext := filepath.Ext(tail); strings.EqualFold(ext, ".exe") {
		tail = tail[:len(tail)-len(ext)]
	}
	return sanitizeMediaText(tail, maxAppRunes)
}

// sanitizeMediaText drops control characters (a title may carry a line
// break), trims and cuts s to max runes, so any value an app sets passes
// NowPlaying.Validate.
func sanitizeMediaText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max-1])) + "…"
	}
	return s
}
