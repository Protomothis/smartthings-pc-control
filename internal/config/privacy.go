package config

import "fmt"

// Privacy guard modes (#143, docs/design/v1.3-sensors.md): what a remote
// or scheduled shutdown, restart, sleep or hibernate does while the camera
// or the microphone is in use.
const (
	// PrivacyGuardOff runs the command as before.
	PrivacyGuardOff = "off"
	// PrivacyGuardWarn runs it, but the grace toast and the notification
	// say what is in use, and a schedule is pushed back once by the grace
	// period.
	PrivacyGuardWarn = "warn"
	// PrivacyGuardBlock refuses it (409 device_in_use).
	PrivacyGuardBlock = "block"
)

// PrivacyConfig is the "privacy" object in config.json (#143): camera and
// microphone in-use detection and the shutdown guard built on it. Opt-in:
// off by default, and while off the service does not read the user's
// consent store at all. Hot-reloaded: the scanner reads it on every tick.
type PrivacyConfig struct {
	// Enabled turns detection on: the status privacy block, the
	// privacy.changed push and the guard. Off by default.
	Enabled bool `json:"enabled"`
	// Guard is one of PrivacyGuardOff, PrivacyGuardWarn (the default) and
	// PrivacyGuardBlock. It only applies while Enabled is on.
	Guard string `json:"guard"`
}

// ValidPrivacyGuard reports whether g is one of the guard modes.
func ValidPrivacyGuard(g string) bool {
	switch g {
	case PrivacyGuardOff, PrivacyGuardWarn, PrivacyGuardBlock:
		return true
	}
	return false
}

// WithDefaults fills a missing guard (an older config.json, a client that
// does not know the key) with "warn". An unknown value is left for
// ValidatePrivacy to refuse on save; on load (SanitizePrivacy) it becomes
// "warn".
func (p PrivacyConfig) WithDefaults() PrivacyConfig {
	if p.Guard == "" {
		p.Guard = PrivacyGuardWarn
	}
	return p
}

// ValidatePrivacy is the save-time check (/api/config POST); "" means fine.
func ValidatePrivacy(p PrivacyConfig) string {
	if g := p.WithDefaults().Guard; !ValidPrivacyGuard(g) {
		return fmt.Sprintf("privacy.guard must be off, warn or block (got %q)", truncate(g, 16))
	}
	return ""
}

// SanitizePrivacy is the load-time counterpart: a hand-edited config.json
// with an unknown guard runs with the default rather than with no guard
// the user can see.
func SanitizePrivacy(p PrivacyConfig) PrivacyConfig {
	p = p.WithDefaults()
	if !ValidPrivacyGuard(p.Guard) {
		p.Guard = PrivacyGuardWarn
	}
	return p
}
