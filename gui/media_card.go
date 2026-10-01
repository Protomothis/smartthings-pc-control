package gui

// The command tab's media card (#117, media-notify.md §15 "UI 구성"):
//
//	미디어
//	[▶] Hype Boy                                    title, bold, cut with …
//	    NewJeans · Spotify                          artist · app, small
//	              [⏮]  [⏸]  [⏭]                    one row, centred
//	[🔊] ─────────●───────────────  42%
//	     스피커 (Realtek(R) Audio)
//	     설정 탭에서 '원격 볼륨·미디어 제어 허용'을 켜면 …   (only when disabled)
//
// Everything goes through the service (/api/media), like SmartThings and
// Telegram: the same media.enabled switch, the same opt-in, the same
// store. The card polls every 3s while the window is on screen; the tray's
// 3s change check (idle.go) keeps the service current in the meantime.

import (
	"errors"
	"fmt"
	"image/color"
	"strconv"
	"sync/atomic"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/sys/windows"
)

// mediaButtonSize is the size of each transport button.
var mediaButtonSize = fyne.NewSize(64, 38)

// mediaErrorShown is how long a failed command's message stays on the card
// (a poll would otherwise replace it within 3s).
const mediaErrorShown = 8 * time.Second

// Media states of MediaInfo.Status.
const (
	mediaPlaying = "playing"
	mediaPaused  = "paused"
	mediaStopped = "stopped"
)

// mediaCard is the card's widgets and the last state they show.
type mediaCard struct {
	icon    *widget.Icon
	line    *widget.Label // title (or the state), one line
	sub     *widget.Label // "artist · app", hidden when empty
	prev    *widget.Button
	play    *widget.Button
	next    *widget.Button
	mute    *widget.Button
	slider  *widget.Slider
	level   *widget.Label
	device  *widget.Label
	reason  *widget.Label
	lineTxt string

	state MediaState
	known bool
	// syncing is set while the widgets follow the service, so their
	// callbacks do not send the state straight back; dragging while the
	// slider is held, so a poll does not yank it away.
	syncing  bool
	dragging bool
	cmdErr   string
	cmdErrAt time.Time
}

// mediaPollBusy keeps a slow poll (a service mid-restart) from piling up
// behind the 3s ticker.
var mediaPollBusy atomic.Bool

// mediaStatusIcon is the glyph in front of the now-playing line.
func mediaStatusIcon(status string) fyne.Resource {
	switch status {
	case mediaPlaying:
		return theme.MediaPlayIcon()
	case mediaPaused:
		return theme.MediaPauseIcon()
	case mediaStopped:
		return theme.MediaStopIcon()
	}
	return theme.MediaMusicIcon()
}

// mediaPlayButtonIcon is ⏸ while playing and ▶ otherwise.
func mediaPlayButtonIcon(status string) fyne.Resource {
	if status == mediaPlaying {
		return theme.MediaPauseIcon()
	}
	return theme.MediaPlayIcon()
}

// mediaToggleCommand is what the ⏯ button sends: an explicit pause or play
// when the state is known (the session tells them apart), the toggle when
// it is not.
func mediaToggleCommand(status string) string {
	switch status {
	case mediaPlaying:
		return "pause"
	case mediaPaused, mediaStopped:
		return "play"
	}
	return "playpause"
}

// mediaLineText is the now-playing line: "Hype Boy — NewJeans · Spotify",
// "재생 중 · VLC" for a session without a title, just "재생 중" /
// "일시정지" without the opt-in, "재생 중인 미디어 없음" with no session.
func (u *ui) mediaLineText(m MediaInfo, share bool) string {
	switch m.Status {
	case mediaPlaying, mediaPaused, mediaStopped:
	default:
		return u.t("media.none")
	}
	state := u.t("media." + m.Status)
	if !share {
		return state
	}
	var text string
	switch {
	case m.Title != "" && m.Artist != "":
		text = m.Title + " — " + m.Artist
	case m.Title != "":
		text = m.Title
	case m.Artist != "":
		text = m.Artist
	default:
		text = state
	}
	if m.App != "" {
		text += " · " + m.App
	}
	return text
}

// mediaLines splits the now-playing text for the card: the title on the
// first line, "artist · app" under it. Without a title the artist (or the
// state) moves up; without the opt-in only the state is shown.
func (u *ui) mediaLines(m MediaInfo, share bool) (string, string) {
	switch m.Status {
	case mediaPlaying, mediaPaused, mediaStopped:
	default:
		return u.t("media.none"), ""
	}
	state := u.t("media." + m.Status)
	if !share {
		return state, ""
	}
	join := func(parts ...string) string {
		out := ""
		for _, p := range parts {
			if p == "" {
				continue
			}
			if out != "" {
				out += " · "
			}
			out += p
		}
		return out
	}
	switch {
	case m.Title != "":
		return m.Title, join(m.Artist, m.App)
	case m.Artist != "":
		return m.Artist, m.App
	}
	return state, m.App
}

// mediaLevelText is the volume value next to the slider, "—" while unknown.
func mediaLevelText(a MediaAudio) string {
	if !a.Available || a.Volume == nil {
		return "—"
	}
	return strconv.Itoa(*a.Volume) + "%"
}

// mediaReason is the one line that says why the card is disabled, or "".
func (u *ui) mediaReason(m MediaState, err error) string {
	switch {
	case errors.Is(err, errMediaUnsupported):
		return u.t("media.reason.old")
	case err != nil:
		return ""
	case !m.Enabled:
		return u.t("media.reason.disabled")
	case !m.Session:
		return u.t("media.reason.nouser")
	}
	return ""
}

// mediaErrorText words a failed command for the card.
func (u *ui) mediaErrorText(err error) string {
	var ce *mediaCommandError
	if !errors.As(err, &ce) {
		return fmt.Sprintf(u.t("media.err.failed"), err)
	}
	switch ce.Code {
	case "media_disabled":
		return u.t("media.reason.disabled")
	case "no_user_session":
		return u.t("media.reason.nouser")
	case "timeout":
		return u.t("media.err.timeout")
	case "unsupported":
		return fmt.Sprintf(u.t("media.err.unsupported"), ce.Message)
	case "failed":
		if ce.Message == "" {
			return fmt.Sprintf(u.t("media.err.failed"), "user-action")
		}
		return fmt.Sprintf(u.t("media.err.failed"), ce.Message)
	}
	return fmt.Sprintf(u.t("media.err.failed"), ce.Code)
}

// smallLabel is de-emphasised caption-sized text.
func smallLabel() *widget.Label {
	l := widget.NewLabel("")
	l.Importance = widget.LowImportance
	l.SizeName = theme.SizeNameCaptionText
	return l
}

// buildMediaCard creates the card for the command tab.
func (u *ui) buildMediaCard() fyne.CanvasObject {
	c := &mediaCard{}
	u.media = c

	c.icon = widget.NewIcon(theme.MediaMusicIcon())
	c.line = widget.NewLabelWithStyle(u.t("media.none"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.line.Truncation = fyne.TextTruncateEllipsis
	c.sub = smallLabel()
	c.sub.Truncation = fyne.TextTruncateEllipsis
	c.sub.Hide()

	c.prev = widget.NewButtonWithIcon("", theme.MediaSkipPreviousIcon(), func() { u.sendMediaCommand("prev", nil) })
	c.play = widget.NewButtonWithIcon("", theme.MediaPlayIcon(), func() {
		u.sendMediaCommand(mediaToggleCommand(c.state.Media.Status), nil)
	})
	c.play.Importance = widget.HighImportance
	c.next = widget.NewButtonWithIcon("", theme.MediaSkipNextIcon(), func() { u.sendMediaCommand("next", nil) })

	c.mute = widget.NewButtonWithIcon("", theme.VolumeUpIcon(), func() {
		cmd := "mute"
		if m := c.state.Audio.Muted; m != nil && *m {
			cmd = "unmute"
		}
		u.sendMediaCommand(cmd, nil)
	})
	c.slider = widget.NewSlider(0, 100)
	c.slider.Step = 1
	c.slider.OnChanged = func(v float64) {
		if c.syncing {
			return
		}
		c.dragging = true
		c.level.SetText(strconv.Itoa(int(v)) + "%")
	}
	// Only the release is sent: one command per gesture, not per pixel.
	c.slider.OnChangeEnded = func(v float64) {
		if c.syncing {
			return
		}
		c.dragging = false
		level := int(v)
		u.sendMediaCommand("volume", &level)
	}
	c.level = widget.NewLabel("—")
	c.level.Alignment = fyne.TextAlignTrailing
	c.device = smallLabel()
	c.device.Truncation = fyne.TextTruncateEllipsis
	c.reason = hint("")
	c.reason.Hide()

	// The device line starts under the slider, not under the mute button.
	indent := canvas.NewRectangle(color.Transparent)
	indent.SetMinSize(fyne.NewSize(c.mute.MinSize().Width, 0))

	nowPlaying := container.NewBorder(nil, nil, c.icon, nil, container.NewVBox(c.line, c.sub))
	// One fixed-size cell per button in an HBox: a GridWrap inside Center
	// is laid out at its own MinSize, which is one cell wide, so the three
	// buttons stacked vertically (rc2).
	cell := func(b *widget.Button) fyne.CanvasObject { return container.NewGridWrap(mediaButtonSize, b) }
	transport := container.NewCenter(container.NewHBox(cell(c.prev), cell(c.play), cell(c.next)))
	levelBox := container.NewGridWrap(fyne.NewSize(56, c.slider.MinSize().Height), c.level)
	volume := container.NewBorder(nil, nil, c.mute, levelBox, c.slider)

	if u.mediaLoaded {
		// A rebuild (language change): show the last state at once.
		u.applyMedia(u.lastMedia, u.lastMediaErr)
	} else {
		// Nothing known yet: inert, and no reason until the service says.
		for _, w := range []fyne.Disableable{c.prev, c.play, c.next, c.mute, c.slider} {
			w.Disable()
		}
	}
	return section(u.t("media.title"), container.NewVBox(
		nowPlaying,
		transport,
		volume,
		container.NewBorder(nil, nil, indent, nil, c.device),
		c.reason,
	))
}

// sendMediaCommand runs one command off the UI thread and shows the state
// the service answers with.
func (u *ui) sendMediaCommand(command string, value *int) {
	runAsync(nil, func() (MediaState, error) { return u.client.MediaCommand(command, value) },
		func(m MediaState, err error) {
			c := u.media
			if c == nil {
				return
			}
			if err != nil {
				c.cmdErr, c.cmdErrAt = u.mediaErrorText(err), time.Now()
				u.applyMedia(c.state, nil)
				return
			}
			c.cmdErr = ""
			u.applyMedia(m, nil)
		})
}

// mediaTick is the 3s media work: the tray's change check, then — while
// the window is on screen — the card's refresh. Runs off the UI thread.
func (u *ui) mediaTick() {
	if !mediaPollBusy.CompareAndSwap(false, true) {
		return
	}
	defer mediaPollBusy.Store(false)
	u.watchMediaChanges()
	if u.connected.Load() && u.onScreen() {
		u.loadMedia()
	}
}

// loadMedia polls the service. Runs off the UI thread.
func (u *ui) loadMedia() {
	m, err := u.client.GetMedia()
	if err != nil && !errors.Is(err, errMediaUnsupported) {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() { u.applyMedia(m, err) })
}

// applyMedia mirrors the state to the card. Must be called on the UI
// thread; err is errMediaUnsupported or nil.
func (u *ui) applyMedia(m MediaState, err error) {
	u.lastMedia, u.lastMediaErr, u.mediaLoaded = m, err, true
	c := u.media
	if c == nil {
		return
	}
	c.syncing = true
	defer func() { c.syncing = false }()
	c.state, c.known = m, err == nil

	status := m.Media.Status
	c.icon.SetResource(mediaStatusIcon(status))
	title, sub := u.mediaLines(m.Media, m.NowPlaying)
	c.lineTxt = title
	c.line.SetText(title)
	c.sub.SetText(sub)
	if sub == "" {
		c.sub.Hide()
	} else {
		c.sub.Show()
	}
	c.play.SetIcon(mediaPlayButtonIcon(status))

	muted := m.Audio.Muted != nil && *m.Audio.Muted
	if muted {
		c.mute.SetIcon(theme.VolumeMuteIcon())
	} else {
		c.mute.SetIcon(theme.VolumeUpIcon())
	}
	if !c.dragging {
		c.level.SetText(mediaLevelText(m.Audio))
		if m.Audio.Volume != nil {
			c.slider.SetValue(float64(*m.Audio.Volume))
		}
	}
	device := ""
	if m.Audio.Device != nil {
		device = *m.Audio.Device
	}
	if device == "" && m.Enabled && m.Session && !m.Audio.Available {
		device = u.t("media.audio.waiting")
	}
	c.device.SetText(device)

	reason := u.mediaReason(m, err)
	if reason == "" && c.cmdErr != "" && time.Since(c.cmdErrAt) < mediaErrorShown {
		reason = c.cmdErr
	}
	if reason != "" {
		c.reason.SetText(reason)
		c.reason.Show()
	} else {
		c.reason.Hide()
	}

	usable := c.known && m.Enabled && m.Session
	for _, b := range []*widget.Button{c.prev, c.play, c.next} {
		setEnabled(b, usable)
	}
	setEnabled(c.mute, usable && m.Audio.Available)
	setEnabled(c.slider, usable && m.Audio.Available)
}

// setEnabled enables or disables any disableable widget.
func setEnabled(w fyne.Disableable, on bool) {
	if on {
		w.Enable()
	} else {
		w.Disable()
	}
}

var (
	procFindWindowW     = modUser32.NewProc("FindWindowW")
	procIsWindowVisible = modUser32.NewProc("IsWindowVisible")
	procIsIconic        = modUser32.NewProc("IsIconic")
)

// windowOnScreen reports whether the app window is shown and not minimised:
// the card's polling is for someone looking at it. The title is fixed
// (singleinstance.go), which is also how a second launch finds the window.
func windowOnScreen() bool {
	title, err := windows.UTF16PtrFromString(windowTitle)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	iconic, _, _ := procIsIconic.Call(hwnd)
	return visible != 0 && iconic == 0
}
