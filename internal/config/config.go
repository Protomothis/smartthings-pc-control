// Package config is the service configuration: the config.json types,
// their defaults, the migration and validation rules, and the file itself
// (file.go). It knows nothing about the HTTP servers, Telegram or the
// scheduler; the service package holds the live copy and reacts to saves.
package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

const (
	// MaskedTokenPrefix is what secret.Mask produces; a POSTed token that
	// starts with it is the GET placeholder echoed back, not a new token.
	MaskedTokenPrefix = "****"
	// ClearTokenSentinel in a POSTed bot_token removes the stored token.
	ClearTokenSentinel = "-"
	// DefaultPort is the SmartThings command port when config.json has
	// none, or an invalid one. The WebUI/API listens on port+1.
	DefaultPort = 5001
)

// Grace period bounds (seconds). DefaultGraceSeconds applies when
// grace_seconds is missing from config.json; the app offers
// 10s/30s/1m/5m/10m/30m, the API accepts anything within the bounds.
const (
	DefaultGraceSeconds = 300
	MinGraceSeconds     = 5
	MaxGraceSeconds     = 3600
)

// Config holds the service configuration
type Config struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
	// WebUIRemote exposes the WebUI on all interfaces (LAN) instead of
	// 127.0.0.1 only. Requires a secret; applied on service restart.
	WebUIRemote bool `json:"webui_remote"`
	// ShutdownGrace defers power commands from SmartThings (shutdown,
	// restart, suspend, hibernate) by GraceSeconds so the user can cancel
	// from the tray app. forceshutdown always runs immediately.
	ShutdownGrace bool `json:"shutdown_grace"`
	// GraceSeconds is the length of that grace period. 0 (key missing in
	// an older config.json, or omitted by a client) means the default.
	GraceSeconds int `json:"grace_seconds"`
	// Telegram is the notification channel (v1.0, design doc §10).
	Telegram TelegramConfig `json:"telegram"`
	// SmartThings holds the Edge driver settings (edge-driver doc §3.7).
	SmartThings SmartThingsConfig `json:"smartthings"`
	// Notify says which Category.Kind events are sent. Missing entries
	// mean the catalogue default; Load/Save store the full map.
	Notify notify.Config `json:"notify"`
	// Awake holds the keep-awake defaults (#111, service/power/awake.go). The
	// on/off state itself is not configuration and is never saved.
	Awake AwakeConfig `json:"awake"`
	// Activity is the opt-in running-app detection (media-notify doc §11,
	// #110). Hot-reloaded: the scanner reads it on every tick.
	Activity ActivityConfig `json:"activity"`
	// Media gates the volume and media-key commands (#104, #105, see
	// service/media.go) on every path: /st/v1/command, Telegram and the
	// audio block of the status.
	Media MediaConfig `json:"media"`
	// NotifyPC controls PC notifications from SmartThings and Telegram
	// (#106, service/pc_notify.go).
	NotifyPC NotifyPCConfig `json:"notify_pc"`
	// Presets are the actions SmartThings and Telegram may run by slot
	// number (#109, presets.go). Invalid entries are refused on save and
	// dropped (with a log line) on load.
	Presets []Preset `json:"presets"`
	// Debug is the developer switch (#133): while on, the service, the
	// desktop app and the user-action runs record a crash to a file
	// (internal/crashdump). Off by default, and in an older config.json;
	// applied on save without a restart.
	Debug bool `json:"debug"`
}

// TelegramConfig is the "telegram" object in config.json (design doc §10).
type TelegramConfig struct {
	Enabled bool `json:"enabled"`
	// BotToken is stored as "dpapi:BASE64" (issue #65); plaintext is
	// accepted and re-encrypted on the next save. Consumers must call
	// secret.Unprotect (see the service's liveBotToken) — never use this
	// value directly.
	// /api/config POST: empty, omitted or the masked form keeps the current
	// token; "-" clears it (see Normalize).
	BotToken       string            `json:"bot_token"`
	ChatID         string            `json:"chat_id"`
	ControlEnabled bool              `json:"control_enabled"`
	AllowedChatIDs []string          `json:"allowed_chat_ids"` // empty: only chat_id
	Detail         string            `json:"detail"`           // "simple" | "full"
	Lang           string            `json:"lang"`             // "ko" | "en"
	PCName         string            `json:"pc_name"`          // empty: hostname
	QuietHours     notify.QuietHours `json:"quiet_hours"`
}

// SmartThingsConfig is the "smartthings" object in config.json
// (edge-driver doc §3.7). Hot-reloaded: every /st/v1 request reads the
// live value, so a save takes effect without a restart.
type SmartThingsConfig struct {
	// There is no "discovery" key any more (#95): the SSDP responder is
	// always on, because adding a device has no other path. An older
	// config.json that still carries one is read without error (the JSON
	// decoder ignores unknown keys) and loses the key on the next save;
	// Load logs that once.
	//
	// AllowedHubs restricts /st/v1/* to these source IPs. Empty (the
	// default) allows any source that knows the secret.
	AllowedHubs []string `json:"allowed_hubs"`
	// ExposeSession opts into the session block of GET /st/v1/status
	// (lock state and idle time); ExposeSessionUser additionally reveals
	// the user name. Both default to off.
	ExposeSession     bool `json:"expose_session"`
	ExposeSessionUser bool `json:"expose_session_user"`
	// WoLMAC pins the adapter the Edge driver addresses its magic packet
	// to (#96). Empty — the default — means the service chooses (see
	// selectWoLAdapter), and so does a value matching no adapter. Stored
	// in the upper-case dash form the adapter list reports
	// ("B4-2E-99-45-B4-F5"); WithDefaults normalises whatever a client
	// sends.
	WoLMAC string `json:"wol_mac"`
}

// WithDefaults normalises the slice field; the bools cannot be defaulted
// here (false is a legitimate value) and rely on decoding over defaults.
func (s SmartThingsConfig) WithDefaults() SmartThingsConfig {
	if s.AllowedHubs == nil {
		s.AllowedHubs = []string{}
	} else {
		s.AllowedHubs = slices.Clone(s.AllowedHubs)
	}
	// An unparseable MAC is stored as "" rather than kept verbatim: the
	// value only ever means "this adapter", and nothing it could match.
	s.WoLMAC = NormalizeMAC(s.WoLMAC)
	return s
}

// Default is the configuration of a PC without config.json. Every call
// returns a fresh copy, so no two configs share a slice.
func Default() Config {
	return Config{
		Port:          DefaultPort,
		Secret:        "",
		WebUIRemote:   false,
		ShutdownGrace: true, // missing key in config.json keeps this default
		GraceSeconds:  DefaultGraceSeconds,
		Telegram: TelegramConfig{
			AllowedChatIDs: []string{},
			Detail:         "full",
			Lang:           "ko",
			// Missing quiet_hours keys keep these because Load decodes
			// over Default (security_bypass/digest default true).
			QuietHours: notify.QuietHours{Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true},
		},
		SmartThings: SmartThingsConfig{
			// A missing "smartthings" object (an older config.json) leaves
			// everything off/empty; SSDP needs no key, it is always on (#95).
			AllowedHubs: []string{},
		},
		// A missing "awake" object keeps these (0 would mean "until turned off").
		Awake: AwakeConfig{DefaultMinutes: AwakeDefaultMinutes},
		// Running-app detection is opt-in (§11): off, with an empty list.
		Activity: ActivityConfig{Enabled: false, Watch: []ActivityWatch{}},
		// A missing "media" object keeps volume and media keys on (§4).
		Media: MediaConfig{Enabled: true},
		// A missing "notify_pc" object keeps notifications on (§4).
		NotifyPC: NotifyPCConfig{Enabled: true},
		// Presets stays nil here, like Notify: WithDefaults gives every copy
		// its own empty list.
		// Notify stays nil here (a nil map means "all defaults"); WithDefaults
		// materialises the catalogue.
	}
}

// WithDefaults fills the telegram/notify values a client or an older
// config.json may omit. It never aliases maps or slices of the receiver.
func (c Config) WithDefaults() Config {
	c.Telegram = c.Telegram.WithDefaults()
	c.SmartThings = c.SmartThings.WithDefaults()
	c.Awake = c.Awake.WithDefaults()
	c.Presets = NormalizePresets(c.Presets)
	c.Notify = c.Notify.WithDefaults()
	c.Activity = c.Activity.WithDefaults()
	return c
}

// WithDefaults normalises the string fields; the bools cannot be defaulted
// here (false is a legitimate value) and rely on decoding over defaults.
func (t TelegramConfig) WithDefaults() TelegramConfig {
	def := Default().Telegram
	if t.Detail != "simple" && t.Detail != "full" {
		t.Detail = def.Detail
	}
	if t.Lang != "ko" && t.Lang != "en" {
		t.Lang = def.Lang
	}
	if t.AllowedChatIDs == nil {
		t.AllowedChatIDs = []string{}
	} else {
		t.AllowedChatIDs = slices.Clone(t.AllowedChatIDs)
	}
	if t.QuietHours.Start == "" {
		t.QuietHours.Start = def.QuietHours.Start
	}
	if t.QuietHours.End == "" {
		t.QuietHours.End = def.QuietHours.End
	}
	return t
}

// ForUpdate returns a copy of the live config for a POST body to be
// decoded over, so keys the client omits (an older GUI knows nothing of
// telegram/notify) keep their current values. Reference fields are
// cleared first so decoding cannot write into maps/slices other
// goroutines are reading; Normalize restores them when still nil.
func (c Config) ForUpdate() Config {
	c.Notify = nil
	c.Telegram.AllowedChatIDs = nil
	c.SmartThings.AllowedHubs = nil
	c.Activity.Watch = nil
	c.Presets = nil
	return c
}

// GraceDuration returns the configured grace period, falling back to the
// default when the value is missing or out of range.
func (c Config) GraceDuration() time.Duration {
	if ValidateGraceSeconds(c.GraceSeconds) != "" {
		return time.Duration(DefaultGraceSeconds) * time.Second
	}
	return time.Duration(c.GraceSeconds) * time.Second
}

// ValidatePort checks if a port number is valid (1-65535).
// Returns an error message or empty string if valid.
func ValidatePort(port int) string {
	if port < 1 || port > 65535 {
		return fmt.Sprintf("port must be between 1 and 65535 (got %d)", port)
	}
	return ""
}

// ValidateGraceSeconds returns a message when the grace period is outside
// the accepted range. 0 is rejected here — callers that mean "default"
// normalise first (see Normalize).
func ValidateGraceSeconds(sec int) string {
	if sec < MinGraceSeconds || sec > MaxGraceSeconds {
		return fmt.Sprintf("grace_seconds must be between %d and %d (got %d)", MinGraceSeconds, MaxGraceSeconds, sec)
	}
	return ""
}

// Normalize fills in values a client may legitimately omit: a
// missing/zero grace_seconds keeps the current (or default) period so an
// older WebUI page or config.json does not silently reset it.
func Normalize(cfg Config, current Config) Config {
	if cfg.GraceSeconds == 0 {
		cfg.GraceSeconds = current.GraceSeconds
		if cfg.GraceSeconds == 0 {
			cfg.GraceSeconds = DefaultGraceSeconds
		}
	}
	// Bot token rules (design doc §10, issue #63): the GET side hands the
	// GUI a masked form ("****1234"), so an empty/omitted token or the
	// masked placeholder sent back means "keep the stored one". The literal
	// "-" clears it; anything else is a new (plaintext) token, which
	// Save encrypts.
	switch tok := cfg.Telegram.BotToken; {
	case tok == "" || strings.HasPrefix(tok, MaskedTokenPrefix):
		cfg.Telegram.BotToken = current.Telegram.BotToken
	case tok == ClearTokenSentinel:
		cfg.Telegram.BotToken = ""
	}
	// nil here means the key was absent from the body (ForUpdate cleared
	// it before decoding, and "[]"/"{}" decode to non-nil): keep current.
	if cfg.Telegram.AllowedChatIDs == nil {
		cfg.Telegram.AllowedChatIDs = current.Telegram.AllowedChatIDs
	}
	if cfg.SmartThings.AllowedHubs == nil {
		cfg.SmartThings.AllowedHubs = current.SmartThings.AllowedHubs
	}
	if cfg.Notify == nil {
		cfg.Notify = current.Notify
	}
	if cfg.Activity.Watch == nil {
		cfg.Activity.Watch = current.Activity.Watch
	}
	if cfg.Presets == nil {
		cfg.Presets = current.Presets
	}
	return cfg.WithDefaults()
}

// truncate shortens a value quoted in a validation message to max runes.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
