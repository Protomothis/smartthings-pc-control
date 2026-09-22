package notify

// catalogue is every Category.Kind the service emits (design doc §3) with
// its default on/off state. It is the source of DefaultConfig and the list
// the GUI renders as checkboxes.
var catalogue = []struct {
	cat, kind string
	on        bool
}{
	{"remote", "received", true},
	{"remote", "grace_scheduled", true},
	{"remote", "grace_cancelled", true},
	{"remote", "executed", true},
	{"remote", "force", true},

	{"schedule", "created", false},
	{"schedule", "cancelled", false},
	{"schedule", "executed", true},
	{"schedule", "replaced", true},

	{"power", "started", true},
	{"power", "resumed", true},
	// #87: on by default. It was off because the message only said
	// "stopping", which on a restart read like a fault; now it names the
	// reason (종료/재시작/절전/최대 절전), and "the PC is shutting down" is
	// the one power event people asked to be told about. A config.json
	// that already says false keeps saying false - Enabled/Over take the
	// explicit value and only fall back here when the key is absent.
	{"power", "stopping", true},

	{"security", "unauthorized", true},
	{"security", "login_limited", true},
	{"security", "unknown_command", true},
	{"security", "config_changed", true},
	{"security", "unknown_chat", true},

	{"system", "update_available", true},
	{"system", "updated", true},
	{"system", "exec_failed", true},
	{"system", "tray_wake_failed", true},
}

// Config is the "notify" object in config.json: Category → Kind → enabled.
// Entries may be missing (older config.json, partial POST); Enabled falls
// back to DefaultConfig for those, so a nil Config is valid and means
// "all defaults".
type Config map[string]map[string]bool

// DefaultConfig returns a fresh map holding the catalogue defaults:
// everything on except schedule.created and schedule.cancelled.
func DefaultConfig() Config {
	c := make(Config)
	for _, e := range catalogue {
		if c[e.cat] == nil {
			c[e.cat] = make(map[string]bool)
		}
		c[e.cat][e.kind] = e.on
	}
	return c
}

// defaultFor reports the catalogue default of cat.kind and whether the
// pair is in the catalogue at all.
func defaultFor(cat, kind string) (on, known bool) {
	for _, e := range catalogue {
		if e.cat == cat && e.kind == kind {
			return e.on, true
		}
	}
	return false, false
}

// Enabled reports whether cat.kind should be delivered. An explicit value
// in c wins; a missing key falls back to the catalogue default. Kinds the
// catalogue does not know (system.test, system.digest) are delivered —
// they are not user-toggleable and must never be swallowed silently.
func (c Config) Enabled(cat, kind string) bool {
	if kinds, ok := c[cat]; ok {
		if v, ok := kinds[kind]; ok {
			return v
		}
	}
	if on, known := defaultFor(cat, kind); known {
		return on
	}
	return true
}

// Over returns a fresh map with base's entries overridden by c's. Neither
// argument is modified or aliased, so the result is safe to store in a
// config that other goroutines read.
func (c Config) Over(base Config) Config {
	out := make(Config, len(base)+len(c))
	for cat, kinds := range base {
		out[cat] = make(map[string]bool, len(kinds))
		for kind, on := range kinds {
			out[cat][kind] = on
		}
	}
	for cat, kinds := range c {
		if out[cat] == nil {
			out[cat] = make(map[string]bool, len(kinds))
		}
		for kind, on := range kinds {
			out[cat][kind] = on
		}
	}
	return out
}

// WithDefaults returns a fresh copy of c in which every catalogue entry is
// present: c's own values win, missing ones come from DefaultConfig. Extra
// keys in c are kept. The service stores this form so config.json lists
// the whole catalogue.
func (c Config) WithDefaults() Config {
	return c.Over(DefaultConfig())
}
