package service

// Telegram volume and media commands (#104, #105; media-notify.md §6):
//
//	/vol            current volume: "볼륨 30% · 음소거 꺼짐 · 스피커"
//	/vol 30         set the level (0–100)
//	/vol +10 | -10  step it (1–100)
//	/mute /unmute   PC mute on/off
//	/play /pause /stop /next /prev
//	/np             what is playing (#117): "▶ 제목 — 아티스트 · Spotify"
//
// They follow media.enabled and answer "nobody is logged in" the way /st/v1
// answers 409. None of them raises a notification (§3).
//
// /mute and /unmute used to pause and resume notifications. A duration
// ("/mute 30m") still pauses them; the pause now also has its own
// /quiet 30m|2h|off, and /unmute unmutes the PC, pointing at /quiet off
// while a pause is on.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/service/telegram"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// parseVolArg reads the /vol argument: "30" (or "30%") sets the level,
// "+10"/"-10" steps it. It returns the /st/v1 command and its value.
func parseVolArg(arg string) (name string, value int, ok bool) {
	arg = strings.TrimSuffix(strings.TrimSpace(arg), "%")
	name = "volume"
	digits := arg
	switch {
	case strings.HasPrefix(arg, "+"):
		name, digits = "volumeup", arg[1:]
	case strings.HasPrefix(arg, "-"):
		name, digits = "volumedown", arg[1:]
	}
	if digits == "" || len(digits) > 3 {
		return "", 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", 0, false
		}
	}
	value, _ = strconv.Atoi(digits)
	if name == "volume" && value > 100 || name != "volume" && (value < 1 || value > 100) {
		return "", 0, false
	}
	return name, value, true
}

// tgVolume handles /vol [0-100|+n|-n].
func tgVolume(ctx context.Context, args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) == 0 {
		a, err := readAudioNow(ctx)
		if err != nil {
			return tgMediaError(err)
		}
		return tgAudioState(a), nil, nil
	}
	name, value, ok := parseVolArg(args[0])
	if !ok {
		return tgText("vol_usage"), nil, fmt.Errorf("invalid /vol argument %q", args[0])
	}
	return tgMediaCommand(ctx, name, &value)
}

// tgMediaCommand runs one command from mediaCommandKinds and words the
// result: the audio state after a volume or mute command, what the media
// session did after a media command ("⏸ 일시정지했습니다 · Spotify"), or
// "⏯ 재생/일시정지 키를 보냈습니다" when only the media key could be pressed.
func tgMediaCommand(ctx context.Context, name string, value *int) (string, *telegram.InlineKeyboard, error) {
	res, err := runMediaCommand(ctx, name, value)
	if err != nil {
		return tgMediaError(err)
	}
	logMsg("Telegram: %s", name)
	if !mediaCommandKinds[name] {
		return tgMediaResult(name, res, getConfig().Media.NowPlaying), nil, nil
	}
	if res.Audio == nil {
		return tgMediaError(fmt.Errorf("%w: no audio block", errUserActionOutput))
	}
	reply := tgAudioState(*res.Audio)
	if name == "unmute" {
		// The notification pause /unmute used to end is still on: say how
		// to end it now.
		if b := currentBus(); b != nil {
			if until := b.MutedUntil(); !until.IsZero() {
				reply += "\n" + tgText("quiet_still", until.Format("15:04"))
			}
		}
	}
	return reply, nil, nil
}

// tgAudioState is "볼륨 30% · 음소거 꺼짐 · 스피커"; a device without a name
// leaves the last part off.
func tgAudioState(a useraction.Audio) string {
	muted := tgText("st_off")
	if a.Muted {
		muted = tgText("st_on")
	}
	s := tgText("vol_state", a.Volume, muted)
	if a.Device != "" {
		s += " · " + html.EscapeString(a.Device)
	}
	return s
}

// tgMediaLabel names a media key; play and pause send the play/pause
// toggle, so they are worded as what was actually pressed.
func tgMediaLabel(name string) string {
	switch name {
	case "play", "pause":
		name = "playpause"
	}
	return tgText("media_" + name)
}

// tgMediaError words a runMediaCommand failure. Only real failures are
// returned as errors (and so logged by the poller); a switched-off
// setting or an empty PC is an answer, not a fault.
func tgMediaError(err error) (string, *telegram.InlineKeyboard, error) {
	var uaErr *userActionError
	var valErr *errMediaValue
	switch {
	case errors.Is(err, errMediaDisabled):
		return tgText("media_disabled"), nil, nil
	case errors.Is(err, errNoUserSession):
		return tgText("media_no_user"), nil, nil
	case errors.As(err, &valErr):
		return tgText("vol_usage"), nil, err
	case errors.Is(err, errUserActionTimeout):
		return tgText("media_timeout"), nil, err
	case errors.As(err, &uaErr) && uaErr.Code == useraction.CodeUnsupported:
		return tgText("media_unsupported", html.EscapeString(uaErr.Message)), nil, err
	case errors.As(err, &uaErr):
		return tgText("media_failed", html.EscapeString(uaErr.Message)), nil, err
	}
	// Start errors can carry paths and child output; those stay in the log.
	return tgText("media_failed", "user-action"), nil, err
}

// tgMediaResult words a media command's reply. The session backend says
// which state it left the session in, so play, pause and the toggle read
// as what happened; the app name follows with the media.now_playing
// opt-in. The key path only knows that a key was pressed.
func tgMediaResult(name string, res UserActionResult, share bool) string {
	status := res.ReplyString("status")
	if res.ReplyString("via") != "session" || !useraction.ValidMediaStatus(status) {
		return tgText("media_sent", tgMediaLabel(name))
	}
	var key string
	switch name {
	case "next", "prev":
		key = "media_done_" + name
	default: // play, pause, playpause, stop: by the state reached
		switch status {
		case useraction.MediaPlaying:
			key = "media_done_play"
		case useraction.MediaPaused:
			key = "media_done_pause"
		default:
			key = "media_done_stop"
		}
	}
	reply := tgText(key)
	if app := res.ReplyString("app"); share && app != "" {
		reply += " · " + html.EscapeString(app)
	}
	return reply
}

// tgNowPlaying handles /np: the session as the user session sees it now,
// not the stored sample, so it also works without the tray app.
func tgNowPlaying(ctx context.Context) (string, *telegram.InlineKeyboard, error) {
	np, err := readNowPlayingNow(ctx)
	if err != nil {
		return tgMediaError(err)
	}
	return tgNowPlayingText(np, getConfig().Media.NowPlaying), nil, nil
}

// tgMediaGlyph is the status symbol of the /np and /status lines.
func tgMediaGlyph(status string) string {
	switch status {
	case useraction.MediaPlaying:
		return "▶"
	case useraction.MediaPaused:
		return "⏸"
	}
	return "⏹"
}

// tgNowPlayingText is "▶ 제목 — 아티스트 · Spotify", "⏸ …", or "재생 중인
// 미디어 없음". Without the opt-in only the state is told, with a note on
// why: "▶ 재생 중 (재생 정보 공유가 꺼져 있습니다)".
func tgNowPlayingText(np useraction.NowPlaying, share bool) string {
	if np.Status == useraction.MediaNone || !useraction.ValidMediaStatus(np.Status) {
		return tgText("np_none")
	}
	state := tgText("np_" + np.Status)
	if !share {
		return tgMediaGlyph(np.Status) + " " + state + " " + tgText("np_private")
	}
	return tgMediaGlyph(np.Status) + " " + tgTrackText(np, state)
}

// tgTrackText is "제목 — 아티스트 · 앱" with whatever parts are known;
// fallback stands in for a missing title and artist.
func tgTrackText(np useraction.NowPlaying, fallback string) string {
	var track string
	switch {
	case np.Title != "" && np.Artist != "":
		track = html.EscapeString(np.Title) + " — " + html.EscapeString(np.Artist)
	case np.Title != "":
		track = html.EscapeString(np.Title)
	case np.Artist != "":
		track = html.EscapeString(np.Artist)
	default:
		track = fallback
	}
	if np.App != "" {
		track += " · " + html.EscapeString(np.App)
	}
	return track
}

// tgMediaLine is the /status "미디어: ▶ 제목 — 아티스트 · Spotify" line, or ""
// unless something is playing — like the activity line, an idle PC needs
// no line saying so. It reads the stored sample (/status runs nothing in
// the user session).
func tgMediaLine(m stMedia, share bool) string {
	if m.Status != useraction.MediaPlaying {
		return ""
	}
	text := tgText("np_playing")
	if share {
		text = tgTrackText(useraction.NowPlaying{Status: m.Status, Title: m.Title, Artist: m.Artist, App: m.App}, text)
	}
	return tgText("st_media") + ": " + tgMediaGlyph(m.Status) + " " + text
}
