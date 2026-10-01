package gui

import (
	"errors"
	"testing"

	"fyne.io/fyne/v2/theme"
)

func TestMediaLineText(t *testing.T) {
	ko := &ui{lang: LangKo}
	cases := []struct {
		m     MediaInfo
		share bool
		want  string
	}{
		{MediaInfo{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", App: "Spotify"}, true, "Hype Boy — NewJeans · Spotify"},
		{MediaInfo{Status: "paused", Title: "Ditto"}, true, "Ditto"},
		{MediaInfo{Status: "playing", Artist: "NewJeans", App: "Chrome"}, true, "NewJeans · Chrome"},
		{MediaInfo{Status: "playing", App: "VLC"}, true, "재생 중 · VLC"},
		{MediaInfo{Status: "stopped"}, true, "정지"},
		// Without the opt-in the service sends no track; the line says
		// the state only.
		{MediaInfo{Status: "playing"}, false, "재생 중"},
		{MediaInfo{Status: "paused", Title: "stale"}, false, "일시정지"},
		{MediaInfo{Status: "none"}, true, "재생 중인 미디어 없음"},
		{MediaInfo{}, false, "재생 중인 미디어 없음"},
	}
	for _, c := range cases {
		if got := ko.mediaLineText(c.m, c.share); got != c.want {
			t.Errorf("mediaLineText(%+v, %v) = %q, want %q", c.m, c.share, got, c.want)
		}
	}
	if got := (&ui{lang: LangEn}).mediaLineText(MediaInfo{Status: "paused"}, false); got != "Paused" {
		t.Errorf("en paused = %q", got)
	}
}

func TestMediaLines(t *testing.T) {
	ko := &ui{lang: LangKo}
	cases := []struct {
		m         MediaInfo
		share     bool
		main, sub string
	}{
		{MediaInfo{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", App: "Spotify"}, true, "Hype Boy", "NewJeans · Spotify"},
		{MediaInfo{Status: "paused", Title: "Ditto"}, true, "Ditto", ""},
		{MediaInfo{Status: "playing", Title: "Lo-fi mix", App: "Chrome"}, true, "Lo-fi mix", "Chrome"},
		{MediaInfo{Status: "playing", Artist: "NewJeans", App: "Chrome"}, true, "NewJeans", "Chrome"},
		{MediaInfo{Status: "playing", App: "VLC"}, true, "재생 중", "VLC"},
		{MediaInfo{Status: "playing", Title: "stale", App: "Spotify"}, false, "재생 중", ""},
		{MediaInfo{Status: "none"}, true, "재생 중인 미디어 없음", ""},
	}
	for _, c := range cases {
		if m, s := ko.mediaLines(c.m, c.share); m != c.main || s != c.sub {
			t.Errorf("mediaLines(%+v, %v) = %q, %q; want %q, %q", c.m, c.share, m, s, c.main, c.sub)
		}
	}
}

func TestMediaToggleCommand(t *testing.T) {
	for status, want := range map[string]string{
		"playing": "pause", "paused": "play", "stopped": "play", "none": "playpause", "": "playpause",
	} {
		if got := mediaToggleCommand(status); got != want {
			t.Errorf("mediaToggleCommand(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestMediaIcons(t *testing.T) {
	if mediaPlayButtonIcon("playing") != theme.MediaPauseIcon() || mediaPlayButtonIcon("paused") != theme.MediaPlayIcon() {
		t.Error("the play button shows the wrong symbol")
	}
	for status, want := range map[string]string{
		"playing": theme.MediaPlayIcon().Name(), "paused": theme.MediaPauseIcon().Name(),
		"stopped": theme.MediaStopIcon().Name(), "none": theme.MediaMusicIcon().Name(),
	} {
		if got := mediaStatusIcon(status).Name(); got != want {
			t.Errorf("mediaStatusIcon(%s) = %s, want %s", status, got, want)
		}
	}
}

func TestMediaLevelText(t *testing.T) {
	v := 42
	if got := mediaLevelText(MediaAudio{Available: true, Volume: &v}); got != "42%" {
		t.Errorf("level = %q", got)
	}
	if got := mediaLevelText(MediaAudio{}); got != "—" {
		t.Errorf("unknown level = %q", got)
	}
}

func TestMediaReason(t *testing.T) {
	ko := &ui{lang: LangKo}
	on := MediaState{Enabled: true, Session: true}
	cases := []struct {
		m    MediaState
		err  error
		want string
	}{
		{on, nil, ""},
		{MediaState{Session: true}, nil, "설정 탭에서 '원격 볼륨·미디어 제어 허용'을 켜면 쓸 수 있습니다"},
		{MediaState{Enabled: true}, nil, "로그인한 사용자가 없어 쓸 수 없습니다"},
		{MediaState{}, errMediaUnsupported, "미디어 카드는 서비스 v1.2.0 이상이 필요합니다"},
	}
	for _, c := range cases {
		if got := ko.mediaReason(c.m, c.err); got != c.want {
			t.Errorf("mediaReason(%+v, %v) = %q, want %q", c.m, c.err, got, c.want)
		}
	}
}

func TestMediaErrorText(t *testing.T) {
	ko := &ui{lang: LangKo}
	cases := map[error]string{
		&mediaCommandError{Code: "no_user_session"}:                                    "로그인한 사용자가 없어 쓸 수 없습니다",
		&mediaCommandError{Code: "timeout"}:                                            "PC가 3초 안에 답하지 않았습니다",
		&mediaCommandError{Code: "unsupported", Message: "no default playback device"}: "이 PC에서는 할 수 없습니다: no default playback device",
		&mediaCommandError{Code: "failed"}:                                             "실패: user-action",
		errors.New("connection refused"):                                               "실패: connection refused",
	}
	for err, want := range cases {
		if got := ko.mediaErrorText(err); got != want {
			t.Errorf("mediaErrorText(%v) = %q, want %q", err, got, want)
		}
	}
}
