package gui

// Screen texts and choices that do not depend on a toolkit (#165); the
// window (package ui) uses them directly.

import (
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// BaseURL is the WebUI address the client talks to now (trayexec, #164).
func (c *Client) BaseURL() string { return c.baseURL() }

// CookieJar holds the client's session cookie, which trayexec shares.
func (c *Client) CookieJar() http.CookieJar { return c.http.Jar }

// stRelKey maps the age of the last hub contact to the i18n key of its
// relative-time phrase and the number to format into it; n < 0 means the
// phrase takes no number ("just now").
func stRelKey(age time.Duration) (key string, n int) {
	switch {
	case age < time.Second: // includes a service clock running ahead
		return "st.rel.now", -1
	case age < time.Minute:
		return "st.rel.sec", int(age.Seconds())
	case age < time.Hour:
		return "st.rel.min", int(age.Minutes())
	case age < 24*time.Hour:
		return "st.rel.hour", int(age.Hours())
	default:
		return "st.rel.day", int(age.Hours() / 24)
	}
}

// RelativeTime renders an RFC3339 timestamp as "3초 전" / "3s ago"
// relative to now. An unparseable or empty value falls back to a neutral
// phrase.
func RelativeTime(l Lang, rfc3339 string, now time.Time) string {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return T(l, "st.rel.unknown")
	}
	key, n := stRelKey(now.Sub(ts))
	if n < 0 {
		return T(l, key)
	}
	return fmt.Sprintf(T(l, key), n)
}

// Languages lists the shipped translations.
func Languages() []Lang { return append([]Lang(nil), languages...) }

// LocalWebUIPort is the API port from tray.json next to the exe (5002
// when it has none).
func LocalWebUIPort() int { return localWebUIPort() }

// IsUnauthorized reports whether err is the service asking for a login.
func IsUnauthorized(err error) bool { return errors.Is(err, errUnauthorized) }

// FormatSeconds renders whole minutes as "5분"/"5 min" and anything
// shorter (or uneven) as seconds.
func FormatSeconds(l Lang, sec int) string {
	if sec >= 60 && sec%60 == 0 {
		return fmt.Sprintf(T(l, "duration.min"), sec/60)
	}
	return fmt.Sprintf(T(l, "duration.sec"), sec)
}

// FormatMinutes renders a delay in the largest unit that fits: "45분",
// "1시간 30분", "3일" (#89).
func FormatMinutes(l Lang, min int) string {
	switch {
	case min < 60:
		return fmt.Sprintf(T(l, "duration.min"), min)
	case min < 1440:
		if rest := min % 60; rest != 0 {
			return fmt.Sprintf(T(l, "duration.hourmin"), min/60, rest)
		}
		return fmt.Sprintf(T(l, "duration.hour"), min/60)
	default:
		// The odd minutes are noise at a day's distance.
		if hours := (min % 1440) / 60; hours != 0 {
			return fmt.Sprintf(T(l, "duration.dayhour"), min/1440, hours)
		}
		return fmt.Sprintf(T(l, "duration.day"), min/1440)
	}
}

// FormatCountdown renders a remaining time: "05:30", "3:15:00" and, from a
// day on, "2일 3:15:00" (#89).
func FormatCountdown(l Lang, d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	days, rest := total/86400, total%86400
	hh, mm, ss := rest/3600, rest%3600/60, rest%60
	switch {
	case days > 0:
		return fmt.Sprintf(T(l, "schedule.countdown.days"), days,
			fmt.Sprintf("%d:%02d:%02d", hh, mm, ss))
	case hh > 0:
		return fmt.Sprintf("%d:%02d:%02d", hh, mm, ss)
	}
	return fmt.Sprintf("%02d:%02d", mm, ss)
}

// BatteryText is the status bar's battery line: "배터리 80% · 충전 중",
// "배터리 100% · 전원 연결됨", "배터리 15%"; "" without a battery.
func BatteryText(l Lang, b Battery) string {
	if !b.Present {
		return ""
	}
	level := T(l, "battery.unknown")
	if b.Percent >= 0 {
		level = strconv.Itoa(b.Percent) + "%"
	}
	text := fmt.Sprintf(T(l, "battery.label"), level)
	switch {
	case b.Charging:
		text += " · " + T(l, "battery.charging")
	case b.AC:
		text += " · " + T(l, "battery.ac")
	}
	return text
}

// awakePresets are the keep-awake durations offered, in minutes; 0 is
// "until turned off". The service accepts 0–1440.
var awakePresets = []int{30, 60, 120, 240, 0}

// defaultAwakePreset is preselected until the service's default_minutes is
// known (and when that is not one of the presets).
const defaultAwakePreset = 60

// AwakePresets are the keep-awake durations offered, in minutes; 0 is
// "until turned off".
func AwakePresets() []int { return append([]int(nil), awakePresets...) }

// DefaultAwakePreset is preselected until the service's default is known.
const DefaultAwakePreset = defaultAwakePreset

// AwakePresetLabel names one preset: "30분", "1시간", "끌 때까지".
func AwakePresetLabel(l Lang, minutes int) string {
	if minutes == 0 {
		return T(l, "awake.forever")
	}
	return FormatMinutes(l, minutes)
}

// AwakeStatusText is the keep-awake line: "42분 남음 · 14:30까지",
// "켜짐 · 끌 때까지" or "꺼짐 — …".
func AwakeStatusText(l Lang, a Awake, now time.Time) string {
	if !a.On {
		return T(l, "awake.off")
	}
	until, err := time.Parse(time.RFC3339, a.Until)
	if a.Until == "" || err != nil {
		return T(l, "awake.on.forever")
	}
	until = until.In(now.Location())
	// Round up: "1분 남음" until the very end, never "0분".
	left := max((a.RemainingSeconds+59)/60, 1)
	at := until.Format("15:04")
	if y, m, d := until.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		at = fmt.Sprintf(T(l, "awake.tomorrow"), at)
	}
	return fmt.Sprintf(T(l, "awake.left"), FormatMinutes(l, left), at)
}

// IsAwakeUnsupported reports the error of a service without keep-awake.
func IsAwakeUnsupported(err error) bool { return errors.Is(err, errAwakeUnsupported) }

// Preset delays (minutes) for a schedule. These are the only choices:
// free-form minute entry was dropped in v0.3.4 (#52).
//
// #89: the same sixteen the Edge driver's list offers, up to three days
// (maxScheduleMinutes in service/server.go).
var schedulePresets = []int{5, 10, 15, 30, 45, 60, 90, 120, 180, 240, 360, 480, 720, 1440, 2880, 4320}

// defaultSchedulePreset is preselected for a new schedule.
const defaultSchedulePreset = 30

// Commands offered for scheduling.
var scheduleCommands = []struct {
	Name     string
	LabelKey string
}{
	{"shutdown", "cmd.shutdown"},
	{"forceshutdown", "cmd.forceshutdown"},
	{"restart", "cmd.restart"},
	{"hibernate", "cmd.hibernate"},
	{"suspend", "cmd.suspend"},
	{"lock", "cmd.lock"},
}

// SchedulePresets are the schedule delays offered, in minutes (#89).
func SchedulePresets() []int { return append([]int(nil), schedulePresets...) }

// DefaultSchedulePreset is preselected for a new schedule.
const DefaultSchedulePreset = defaultSchedulePreset

// ScheduleCommand is one command a schedule can run.
type ScheduleCommand struct {
	Name     string
	LabelKey string
}

// ScheduleCommands are the commands offered for scheduling.
func ScheduleCommands() []ScheduleCommand {
	out := make([]ScheduleCommand, len(scheduleCommands))
	for i, sc := range scheduleCommands {
		out[i] = ScheduleCommand{Name: sc.Name, LabelKey: sc.LabelKey}
	}
	return out
}

// CommandLabel is the localised name of a schedulable command, or the
// raw name.
func CommandLabel(l Lang, name string) string {
	for _, sc := range scheduleCommands {
		if sc.Name == name {
			return T(l, sc.LabelKey)
		}
	}
	return name
}

// OriginShortKey maps a schedule's wire origin to the key of its short
// label.
func OriginShortKey(origin string) string {
	switch origin {
	case "remote":
		return "origin.remote.short"
	case "telegram":
		return "origin.telegram.short"
	case "smartthings":
		return "origin.smartthings.short"
	}
	return "origin.ui.short"
}

// Media states of MediaInfo.Status.
const (
	mediaPlaying = "playing"
	mediaPaused  = "paused"
	mediaStopped = "stopped"
)

// MediaToggleCommand is what the ⏯ button sends: an explicit pause or play
// when the state is known, the toggle when it is not.
func MediaToggleCommand(status string) string {
	switch status {
	case mediaPlaying:
		return "pause"
	case mediaPaused, mediaStopped:
		return "play"
	}
	return "playpause"
}

// MediaLines splits the now-playing text for the card: the title on the
// first line, "artist · app" under it. Without a title the artist (or the
// state) moves up; without the opt-in (share) only the state is shown.
func MediaLines(l Lang, m MediaInfo, share bool) (string, string) {
	switch m.Status {
	case mediaPlaying, mediaPaused, mediaStopped:
	default:
		return T(l, "media.none"), ""
	}
	state := T(l, "media."+m.Status)
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

// MediaLevelText is the volume value, "—" while unknown.
func MediaLevelText(a MediaAudio) string {
	if !a.Available || a.Volume == nil {
		return "—"
	}
	return strconv.Itoa(*a.Volume) + "%"
}

// MediaReason is the one line that says why the media card is disabled,
// or "".
func MediaReason(l Lang, m MediaState, err error) string {
	switch {
	case errors.Is(err, errMediaUnsupported):
		return T(l, "media.reason.old")
	case err != nil:
		return ""
	case !m.Enabled:
		return T(l, "media.reason.disabled")
	case !m.Session:
		return T(l, "media.reason.nouser")
	}
	return ""
}

// iconBytes is the app icon as a 256 px PNG; trayICO the tray icon. Both
// are rendered from assets/logo by internal/tools/logo.
var (
	//go:embed icon.png
	iconBytes []byte
	//go:embed tray.ico
	trayICO []byte
)

// IconPNG is the app icon, 256 px. The exe and its window take theirs
// from icon.ico through the resource file.
func IconPNG() []byte { return iconBytes }

// TrayICO is the tray icon: frames for every small-icon size, plus each
// drawn at twice its size for the shell to halve (internal/tools/logo).
func TrayICO() []byte { return trayICO }

// SettingsForm is the service settings form as plain values
// (settings_model.go): port and secret text, browser access, media
// control, the grace period, PC notifications and debug mode.
type SettingsForm = settingsFormState

// NewSettingsForm is what the form shows for cfg.
func NewSettingsForm(cfg Config) SettingsForm { return settingsStateFromConfig(cfg) }

// ApplyTo writes the form over cfg and checks it; see applyTo.
func (s settingsFormState) ApplyTo(cfg *Config, l Lang) error { return s.applyTo(cfg, l) }

// Dirty reports whether saving would change base.
func (s settingsFormState) Dirty(base Config) bool { return s.dirty(base) }

// SettingsBaseline is cfg as the form compares against it: an older
// service's grace period without a length gets the default one.
func SettingsBaseline(cfg Config) Config { return withGraceFallback(cfg) }

// GraceOptions are the grace periods offered, in seconds; "off" comes
// before them.
func GraceOptions() []int { return append([]int(nil), graceOptions...) }
