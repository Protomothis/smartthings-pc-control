package gui

import (
	"errors"
	"strconv"
	"strings"
)

// Pure form model of the settings tab, like notifyFormState and
// stFormState: the widgets are read into it, and the config round trip,
// the checks and the dirty test are unit-tested on it.

// Grace period choices (seconds) for remote power commands (#51). The
// settings select shows "Off" first, then these.
var graceOptions = []int{10, 30, 60, 300, 600, 1800}

// fallbackGraceSeconds is used when the service reports grace enabled but
// no period (an older service) — matches the service default.
const fallbackGraceSeconds = 300

// withGraceFallback mirrors what the grace select shows for a service
// predating grace_seconds (enabled, no period), so a config adopted as the
// baseline does not leave the settings form dirty.
func withGraceFallback(cfg Config) Config {
	if cfg.ShutdownGrace && cfg.GraceSeconds <= 0 {
		cfg.GraceSeconds = fallbackGraceSeconds
	}
	return cfg
}

// settingsFormState is the settings tab's contents as plain values.
type settingsFormState struct {
	// Port is the entry text; it only becomes a number on save.
	Port   string
	Secret string
	Remote bool
	Media  bool
	// GraceOn and GraceSec are the grace select: off, or a period.
	GraceOn  bool
	GraceSec int
	NotifyPC notifyPCState
	// Debug is the developer section's switch (#133).
	Debug bool
}

// settingsStateFromConfig is what the tab shows for cfg.
func settingsStateFromConfig(cfg Config) settingsFormState {
	s := settingsFormState{
		Port:     strconv.Itoa(cfg.Port),
		Secret:   cfg.Secret,
		Remote:   cfg.WebUIRemote,
		Media:    cfg.Media.Enabled,
		GraceOn:  cfg.ShutdownGrace,
		NotifyPC: notifyPCStateFromConfig(cfg),
		Debug:    cfg.Debug,
	}
	if s.GraceOn {
		s.GraceSec = cfg.GraceSeconds
		if s.GraceSec <= 0 {
			s.GraceSec = fallbackGraceSeconds
		}
	}
	return s
}

// applyTo writes the tab's fields over cfg, then checks them. "Off" keeps
// the period cfg already has, so switching grace back on restores it. On
// a bad port the old port stays and the error says why.
func (s settingsFormState) applyTo(cfg *Config, l Lang) error {
	cfg.Secret = s.Secret
	cfg.WebUIRemote = s.Remote
	cfg.Media.Enabled = s.Media
	cfg.Debug = s.Debug
	s.NotifyPC.applyTo(cfg)
	cfg.ShutdownGrace = s.GraceOn
	switch {
	case s.GraceOn:
		cfg.GraceSeconds = s.GraceSec
	case cfg.GraceSeconds <= 0:
		cfg.GraceSeconds = fallbackGraceSeconds
	}
	port, err := strconv.Atoi(strings.TrimSpace(s.Port))
	if err != nil {
		return errors.New(T(l, "settings.invalidport") + s.Port)
	}
	cfg.Port = port
	if s.Remote && s.Secret == "" {
		return errors.New(T(l, "settings.remote.needsecret"))
	}
	return nil
}

// dirty reports whether saving would change base.
func (s settingsFormState) dirty(base Config) bool {
	return strings.TrimSpace(s.Port) != strconv.Itoa(base.Port) ||
		s.Secret != base.Secret ||
		s.Remote != base.WebUIRemote ||
		s.Media != base.Media.Enabled ||
		s.GraceOn != base.ShutdownGrace ||
		(s.GraceOn && s.GraceSec != base.GraceSeconds) ||
		s.Debug != base.Debug ||
		s.NotifyPC.dirty(base)
}

// notifyPCState is the PC-notification part of the settings as plain
// values.
type notifyPCState struct {
	Enabled bool
}

func notifyPCStateFromConfig(cfg Config) notifyPCState {
	return notifyPCState{Enabled: cfg.NotifyPC.Enabled}
}

// applyTo writes the state into cfg.
func (s notifyPCState) applyTo(cfg *Config) {
	cfg.NotifyPC = NotifyPCConfig(s)
}

// dirty reports whether saving would change base.
func (s notifyPCState) dirty(base Config) bool {
	return s.Enabled != base.NotifyPC.Enabled
}
