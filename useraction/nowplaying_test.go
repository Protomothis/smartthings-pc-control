package useraction

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// No test may reach the real media session: it would read (or, worse,
// pause) whatever this machine is playing. Tests that need a session
// install one with withFakeSession.
func init() {
	openSession = func() (mediaSession, error) { return nil, nil }
}

type fakeSession struct {
	aumid                string
	status               string
	statusErr            error
	title, artist, album string
	propsErr             error
	declined             bool
	controlErr           error
	sent                 []string
	closed               bool
}

func (f *fakeSession) AppID() string           { return f.aumid }
func (f *fakeSession) Status() (string, error) { return f.status, f.statusErr }
func (f *fakeSession) Properties() (string, string, string, error) {
	return f.title, f.artist, f.album, f.propsErr
}
func (f *fakeSession) Control(verb string) (bool, error) {
	f.sent = append(f.sent, verb)
	return !f.declined, f.controlErr
}
func (f *fakeSession) Close() { f.closed = true }

// withFakeSession makes s the current session (nil: none) or openErr the
// failure to find one.
func withFakeSession(t *testing.T, s *fakeSession, openErr error) {
	t.Helper()
	savedOpen, savedRun := openSession, runCOM
	openSession = func() (mediaSession, error) {
		if openErr != nil || s == nil {
			return nil, openErr
		}
		return s, nil
	}
	runCOM = func(f func() error) error { return f() }
	t.Cleanup(func() { openSession, runCOM = savedOpen, savedRun })
}

func TestSessionOutcome(t *testing.T) {
	cases := []struct {
		verb, before, after string
		noop                bool
	}{
		{"play", MediaPaused, MediaPlaying, false},
		{"play", MediaStopped, MediaPlaying, false},
		{"play", MediaPlaying, MediaPlaying, true},
		{"pause", MediaPlaying, MediaPaused, false},
		// Not playing: nothing to pause, and never the toggle key.
		{"pause", MediaPaused, MediaPaused, true},
		{"pause", MediaStopped, MediaStopped, true},
		{"playpause", MediaPlaying, MediaPaused, false},
		{"playpause", MediaPaused, MediaPlaying, false},
		{"playpause", MediaStopped, MediaPlaying, false},
		{"stop", MediaPlaying, MediaStopped, false},
		{"stop", MediaStopped, MediaStopped, true},
		{"next", MediaPlaying, MediaPlaying, false},
		{"prev", MediaPaused, MediaPaused, false},
	}
	for _, c := range cases {
		after, noop := sessionOutcome(c.verb, c.before)
		if after != c.after || noop != c.noop {
			t.Errorf("sessionOutcome(%s, %s) = %s, %v; want %s, %v", c.verb, c.before, after, noop, c.after, c.noop)
		}
	}
}

func TestSessionBackend(t *testing.T) {
	s := &fakeSession{aumid: "Spotify.exe", status: MediaPlaying}
	withFakeSession(t, s, nil)
	handled, fields, err := sendSessionMedia("pause")
	if !handled || err != nil || !reflect.DeepEqual(fields, map[string]any{"status": "paused", "app": "Spotify"}) {
		t.Fatalf("pause: %v %v %v", handled, fields, err)
	}
	if !reflect.DeepEqual(s.sent, []string{"pause"}) || !s.closed {
		t.Errorf("sent %v, closed %v", s.sent, s.closed)
	}

	// Already paused: handled without a request.
	s.status, s.sent = MediaPaused, nil
	if handled, _, _ := sendSessionMedia("pause"); !handled || len(s.sent) != 0 {
		t.Errorf("pause while paused: handled %v, sent %v", handled, s.sent)
	}

	// The app declines: the keys get it.
	s.declined = true
	if handled, _, err := sendSessionMedia("next"); handled || err != nil {
		t.Errorf("declined: handled %v, %v", handled, err)
	}
	s.declined = false

	// A failing call is an error (the next backend still runs).
	s.controlErr = errors.New("boom")
	if handled, _, err := sendSessionMedia("next"); handled || err == nil {
		t.Errorf("control error: handled %v, %v", handled, err)
	}
	s.controlErr = nil

	// A session that is closing is no session.
	s.status = MediaNone
	if handled, _, err := sendSessionMedia("play"); handled || err != nil {
		t.Errorf("closed session: handled %v, %v", handled, err)
	}
}

func TestSessionBackendNoSession(t *testing.T) {
	withFakeSession(t, nil, nil)
	if handled, _, err := sendSessionMedia("play"); handled || err != nil {
		t.Errorf("no session: handled %v, %v", handled, err)
	}
	withFakeSession(t, nil, errNoMediaSessionAPI)
	if handled, _, err := sendSessionMedia("play"); handled || err != nil {
		t.Errorf("no API: handled %v, %v (want a quiet fall-through)", handled, err)
	}
	withFakeSession(t, nil, errors.New("RequestAsync: no result"))
	var ue *Error
	if _, _, err := sendSessionMedia("play"); !errors.As(err, &ue) || ue.Code != CodeFailed {
		t.Errorf("manager failure: %v", err)
	}
}

// With a session the verb never reaches the keys.
func TestMediaHandlerViaSession(t *testing.T) {
	keys := withFakeSendInput(t, ^uint32(0), nil)
	withFakeSession(t, &fakeSession{aumid: "Chrome", status: MediaPaused}, nil)
	if _, line, code := runMain(t, "media", "play"); code != 0 || line != `{"ok":true,"app":"Chrome","media":"play","status":"playing","via":"session"}` {
		t.Errorf("exit %d, %s", code, line)
	}
	if len(*keys) != 0 {
		t.Error("a media key was pressed")
	}
}

func TestReadNowPlaying(t *testing.T) {
	withFakeSession(t, nil, nil)
	if np, err := ReadNowPlaying(); err != nil || np != (NowPlaying{Status: MediaNone}) {
		t.Errorf("no session: %+v, %v", np, err)
	}

	s := &fakeSession{aumid: "SpotifyAB.SpotifyMusic_zpdnekdrzrea0!Spotify", status: MediaPlaying,
		title: "  Hype\nBoy ", artist: "NewJeans", album: "New Jeans"}
	withFakeSession(t, s, nil)
	want := NowPlaying{Status: MediaPlaying, Title: "Hype Boy", Artist: "NewJeans", Album: "New Jeans", App: "Spotify"}
	if np, err := ReadNowPlaying(); err != nil || np != want {
		t.Errorf("playing: %+v, %v; want %+v", np, err, want)
	}
	if !s.closed {
		t.Error("session not closed")
	}

	// No track from the app: the status still counts.
	s.propsErr = errors.New("E_ACCESSDENIED")
	if np, err := ReadNowPlaying(); err != nil || np != (NowPlaying{Status: MediaPlaying, App: "Spotify"}) {
		t.Errorf("no properties: %+v, %v", np, err)
	}

	s.statusErr = errors.New("RPC_E_DISCONNECTED")
	var ue *Error
	if _, err := ReadNowPlaying(); !errors.As(err, &ue) || ue.Code != CodeFailed {
		t.Errorf("status error: %v", err)
	}
	withFakeSession(t, nil, errNoMediaSessionAPI)
	if _, err := ReadNowPlaying(); !errors.As(err, &ue) || ue.Code != CodeUnsupported {
		t.Errorf("no API: %v", err)
	}
}

func TestMediaInfoVerb(t *testing.T) {
	keys := withFakeSendInput(t, ^uint32(0), nil)
	withFakeSession(t, &fakeSession{aumid: "vlc.exe", status: MediaPaused, title: "Movie"}, nil)
	if _, line, code := runMain(t, "media", "info"); code != 0 || line != `{"ok":true,"media":{"status":"paused","title":"Movie","app":"VLC"}}` {
		t.Errorf("exit %d, %s", code, line)
	}
	withFakeSession(t, nil, nil)
	if _, line, code := runMain(t, "media", "info"); code != 0 || line != `{"ok":true,"media":{"status":"none"}}` {
		t.Errorf("exit %d, %s", code, line)
	}
	if len(*keys) != 0 {
		t.Error("media info pressed a key")
	}
}

func TestAppDisplayName(t *testing.T) {
	saved := uiLanguageKorean
	t.Cleanup(func() { uiLanguageKorean = saved })
	uiLanguageKorean = func() bool { return true }
	cases := map[string]string{
		"":            "",
		"Spotify.exe": "Spotify",
		"SpotifyAB.SpotifyMusic_zpdnekdrzrea0!Spotify": "Spotify",
		"Chrome":     "Chrome",
		"chrome.exe": "Chrome",
		"Chrome._crx_cinhimbnkkaeohfgghhklpknlkffjgod": "Chrome",
		"MSEdge": "Edge",
		"Microsoft.MicrosoftEdge.Stable_8wekyb3d8bbwe!App": "Edge",
		"308046B0AF4A39CB": "Firefox",
		"firefox.exe":      "Firefox",
		"VideoLAN.VLC":     "VLC",
		`C:\Program Files\foobar2000\foobar2000.exe`:            "foobar2000",
		"Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic": "미디어 플레이어",
		"Music.UI.exe":                    "미디어 플레이어",
		"Contoso.Player_abc123!PlayerApp": "PlayerApp",
		`C:\Tools\MyPlayer.EXE`:           "MyPlayer",
		"weird.exe":                       "weird",
	}
	for in, want := range cases {
		if got := AppDisplayName(in); got != want {
			t.Errorf("AppDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
	uiLanguageKorean = func() bool { return false }
	if got := AppDisplayName("Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic"); got != "Media Player" {
		t.Errorf("English Media Player = %q", got)
	}
}

func TestSanitizeMediaText(t *testing.T) {
	if got := sanitizeMediaText(" a\tb\r\n  c ", 10); got != "a b c" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("가", MaxMediaTextRunes+5)
	got := sanitizeMediaText(long, MaxMediaTextRunes)
	if n := len([]rune(got)); n != MaxMediaTextRunes || !strings.HasSuffix(got, "…") {
		t.Errorf("cut to %d runes: %q", n, got)
	}
	if err := (NowPlaying{Status: MediaPlaying, Title: got}).Validate(); err != nil {
		t.Errorf("sanitized title fails Validate: %v", err)
	}
}

func TestNowPlayingValidate(t *testing.T) {
	good := []NowPlaying{
		{Status: MediaNone},
		{Status: MediaStopped, App: "VLC"},
		{Status: MediaPlaying, Title: "t", Artist: "a", Album: "b", App: "Spotify"},
	}
	for _, n := range good {
		if err := n.Validate(); err != nil {
			t.Errorf("%+v: %v", n, err)
		}
	}
	bad := []NowPlaying{
		{},
		{Status: "Playing"},
		{Status: MediaPlaying, Title: "a\nb"},
		{Status: MediaPlaying, Artist: strings.Repeat("x", MaxMediaTextRunes+1)},
		{Status: MediaPlaying, App: strings.Repeat("x", maxAppRunes+1)},
	}
	for _, n := range bad {
		if n.Validate() == nil {
			t.Errorf("%+v validated", n)
		}
	}
	full := NowPlaying{Status: MediaPaused, Title: "t", Artist: "a", Album: "b", App: "c"}
	if full.StatusOnly() != (NowPlaying{Status: MediaPaused}) {
		t.Errorf("StatusOnly = %+v", full.StatusOnly())
	}
}

func TestPlaybackStatusName(t *testing.T) {
	want := map[int32]string{0: "none", 1: "stopped", 2: "stopped", 3: "stopped", 4: "playing", 5: "paused", 9: "none"}
	for in, w := range want {
		if got := playbackStatusName(in); got != w {
			t.Errorf("playbackStatusName(%d) = %s, want %s", in, got, w)
		}
	}
}
