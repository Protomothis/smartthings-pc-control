package tgcontrol

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

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
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

// volume handles /vol [0-100|+n|-n].
func (c *Control) volume(ctx context.Context, args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) == 0 {
		a, err := c.d.Media.ReadAudio(ctx)
		if err != nil {
			return c.mediaError(err)
		}
		return c.audioState(a), nil, nil
	}
	name, value, ok := parseVolArg(args[0])
	if !ok {
		return c.text("vol_usage"), nil, fmt.Errorf("invalid /vol argument %q", args[0])
	}
	return c.mediaCommand(ctx, name, &value)
}

// mediaCommand runs one media command (action.IsMedia) and words the
// result: the audio state after a volume or mute command, what the media
// session did after a media command ("⏸ 일시정지했습니다 · Spotify"), or
// "⏯ 재생/일시정지 키를 보냈습니다" when only the media key could be pressed.
func (c *Control) mediaCommand(ctx context.Context, name string, value *int) (string, *telegram.InlineKeyboard, error) {
	res, err := c.d.Media.Run(ctx, name, value)
	if err != nil {
		return c.mediaError(err)
	}
	logx.Printf("Telegram: %s", name)
	if !action.IsAudio(name) {
		return c.mediaResult(name, res, c.d.Config().Media.NowPlaying), nil, nil
	}
	if res.Audio == nil {
		return c.mediaError(fmt.Errorf("%w: no audio block", session.ErrOutput))
	}
	reply := c.audioState(*res.Audio)
	if name == "unmute" {
		// The notification pause /unmute used to end is still on: say how
		// to end it now.
		if b := c.d.Bus(); b != nil {
			if until := b.MutedUntil(); !until.IsZero() {
				reply += "\n" + c.text("quiet_still", until.Format("15:04"))
			}
		}
	}
	return reply, nil, nil
}

// audioState is "볼륨 30% · 음소거 꺼짐 · 스피커"; a device without a name
// leaves the last part off.
func (c *Control) audioState(a useraction.Audio) string {
	muted := c.text("st_off")
	if a.Muted {
		muted = c.text("st_on")
	}
	s := c.text("vol_state", a.Volume, muted)
	if a.Device != "" {
		s += " · " + html.EscapeString(a.Device)
	}
	return s
}

// mediaLabel names a media key; play and pause send the play/pause
// toggle, so they are worded as what was actually pressed.
func (c *Control) mediaLabel(name string) string {
	switch name {
	case "play", "pause":
		name = "playpause"
	}
	return c.text("media_" + name)
}

// mediaError words a failed media command (Media.Run). Only real failures are
// returned as errors (and so logged by the poller); a switched-off
// setting or an empty PC is an answer, not a fault.
func (c *Control) mediaError(err error) (string, *telegram.InlineKeyboard, error) {
	var uaErr *session.ActionError
	var valErr *action.ValueError
	switch {
	case errors.Is(err, action.ErrMediaDisabled):
		return c.text("media_disabled"), nil, nil
	case errors.Is(err, session.ErrNoUserSession):
		return c.text("media_no_user"), nil, nil
	case errors.As(err, &valErr):
		return c.text("vol_usage"), nil, err
	case errors.Is(err, session.ErrTimeout):
		return c.text("media_timeout"), nil, err
	case errors.As(err, &uaErr) && uaErr.Code == useraction.CodeUnsupported:
		return c.text("media_unsupported", html.EscapeString(uaErr.Message)), nil, err
	case errors.As(err, &uaErr):
		return c.text("media_failed", html.EscapeString(uaErr.Message)), nil, err
	}
	// Start errors can carry paths and child output; those stay in the log.
	return c.text("media_failed", "user-action"), nil, err
}

// mediaResult words a media command's reply. The session backend says
// which state it left the session in, so play, pause and the toggle read
// as what happened; the app name follows with the media.now_playing
// opt-in. The key path only knows that a key was pressed.
func (c *Control) mediaResult(name string, res session.Result, share bool) string {
	status := res.ReplyString("status")
	if res.ReplyString("via") != "session" || !useraction.ValidMediaStatus(status) {
		return c.text("media_sent", c.mediaLabel(name))
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
	reply := c.text(key)
	if app := res.ReplyString("app"); share && app != "" {
		reply += " · " + html.EscapeString(app)
	}
	return reply
}

// nowPlaying handles /np: the session as the user session sees it now,
// not the stored sample, so it also works without the tray app.
func (c *Control) nowPlaying(ctx context.Context) (string, *telegram.InlineKeyboard, error) {
	np, err := c.d.Media.ReadNowPlaying(ctx)
	if err != nil {
		return c.mediaError(err)
	}
	return c.nowPlayingText(np, c.d.Config().Media.NowPlaying), nil, nil
}

// mediaGlyph is the status symbol of the /np and /status lines.
func mediaGlyph(status string) string {
	switch status {
	case useraction.MediaPlaying:
		return "▶"
	case useraction.MediaPaused:
		return "⏸"
	}
	return "⏹"
}

// nowPlayingText is "▶ 제목 — 아티스트 · Spotify", "⏸ …", or "재생 중인
// 미디어 없음". Without the opt-in only the state is told, with a note on
// why: "▶ 재생 중 (재생 정보 공유가 꺼져 있습니다)".
func (c *Control) nowPlayingText(np useraction.NowPlaying, share bool) string {
	if np.Status == useraction.MediaNone || !useraction.ValidMediaStatus(np.Status) {
		return c.text("np_none")
	}
	state := c.text("np_" + np.Status)
	if !share {
		return mediaGlyph(np.Status) + " " + state + " " + c.text("np_private")
	}
	return mediaGlyph(np.Status) + " " + trackText(np, state)
}

// trackText is "제목 — 아티스트 · 앱" with whatever parts are known;
// fallback stands in for a missing title and artist.
func trackText(np useraction.NowPlaying, fallback string) string {
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

// mediaLine is the /status "미디어: ▶ 제목 — 아티스트 · Spotify" line, or ""
// unless something is playing — like the activity line, an idle PC needs
// no line saying so. It reads the stored sample (/status runs nothing in
// the user session).
func (c *Control) mediaLine(m status.Media, share bool) string {
	if m.Status != useraction.MediaPlaying {
		return ""
	}
	text := c.text("np_playing")
	if share {
		text = trackText(useraction.NowPlaying{Status: m.Status, Title: m.Title, Artist: m.Artist, App: m.App}, text)
	}
	return c.text("st_media") + ": " + mediaGlyph(m.Status) + " " + text
}
