package config

const (
	// AwakeMaxMinutes caps one keep-awake period at a day; 0 (until turned
	// off) is the way to ask for longer.
	AwakeMaxMinutes = 1440
	// AwakeDefaultMinutes is awake.default_minutes when the key is missing.
	AwakeDefaultMinutes = 60
)

// AwakeConfig is the "awake" object in config.json (#111).
type AwakeConfig struct {
	// DefaultMinutes is the period used when a request names none (the
	// Telegram /awake without an argument, a driver command without a
	// value). 0 means until turned off. Missing key: 60.
	DefaultMinutes int `json:"default_minutes"`
	// KeepDisplay also keeps the display on (ES_DISPLAY_REQUIRED). Off by
	// default: the point is that the PC keeps working, not the monitor.
	KeepDisplay bool `json:"keep_display"`
}

// WithDefaults puts an out-of-range period back to the default. 0 is a
// legitimate value ("until turned off"), so a missing key relies on
// decoding over Default like the other numbers.
func (a AwakeConfig) WithDefaults() AwakeConfig {
	if a.DefaultMinutes < 0 || a.DefaultMinutes > AwakeMaxMinutes {
		a.DefaultMinutes = AwakeDefaultMinutes
	}
	return a
}

// Period resolves an optional keep-awake period: nil means
// default_minutes.
func (a AwakeConfig) Period(minutes *int) int {
	if minutes != nil {
		return *minutes
	}
	return a.WithDefaults().DefaultMinutes
}

// ValidAwakeMinutes reports whether minutes is a period keep-awake
// accepts: 0 (until turned off) through AwakeMaxMinutes.
func ValidAwakeMinutes(minutes int) bool {
	return minutes >= 0 && minutes <= AwakeMaxMinutes
}

// MediaConfig is the "media" object in config.json (media-notify doc §4).
// There is nothing to normalise: a missing key keeps the default (on)
// because Load decodes over Default.
type MediaConfig struct {
	// Enabled allows the volume, mute and media-key commands. Default on.
	Enabled bool `json:"enabled"`
	// NowPlaying (#117) shares the title, artist, album and app of the
	// playing media in status, pushes and Telegram. Opt-in, default off;
	// the playback status alone follows Enabled (service/nowplaying.go).
	NowPlaying bool `json:"now_playing"`
}

// NotifyPCConfig is the "notify_pc" object in config.json (§4). The
// "speak" and "voice" keys of the dropped read-aloud feature (2026-10-01)
// are not fields: an old config.json that has them loads as usual and the
// next save writes the object without them.
type NotifyPCConfig struct {
	// Enabled allows SmartThings and Telegram to show a toast on this PC
	// (default on). Off, /st/v1/notify answers 403 notify_disabled.
	Enabled bool `json:"enabled"`
}
