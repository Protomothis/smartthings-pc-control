package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// notAuditedKeys are the settings security.config_changed leaves out on
// purpose: comfort settings that let nobody reach or watch the PC.
// presets is audited by slot (PresetChangeKey). debug only decides whether
// a crash leaves a file on this PC (#133).
var notAuditedKeys = []string{
	"shutdown_grace",
	"grace_seconds",
	"notify",
	"awake.default_minutes",
	"awake.keep_display",
	"presets",
	"debug",
}

// leafKeys lists every config.json path, descending into the objects
// (quiet_hours counts as one setting).
func leafKeys(t reflect.Type, prefix string) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		key := prefix + jsonName(f)
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[notify.QuietHours]() {
			out = append(out, leafKeys(f.Type, key+".")...)
			continue
		}
		out = append(out, key)
	}
	return out
}

// A new setting must be classified: reported on change, or deliberately
// not.
func TestEveryKeyIsClassified(t *testing.T) {
	for _, key := range leafKeys(reflect.TypeFor[Config](), "") {
		if !slices.Contains(auditedKeys, key) && !slices.Contains(notAuditedKeys, key) {
			t.Errorf("config key %q is neither audited nor listed as not audited", key)
		}
	}
	for _, key := range append(slices.Clone(auditedKeys), notAuditedKeys...) {
		if _, err := jsonFieldIndex(reflect.TypeFor[Config](), key); err != nil {
			t.Error(err)
		}
	}
}

// Every audited key is reported alone when only it changes, in the
// documented order when several do.
func TestChangedKeysEachKey(t *testing.T) {
	base := Default().WithDefaults()
	changes := map[string]func(*Config){
		"secret":                          func(c *Config) { c.Secret = "x" },
		"port":                            func(c *Config) { c.Port = 6001 },
		"webui_remote":                    func(c *Config) { c.WebUIRemote = true },
		"telegram.enabled":                func(c *Config) { c.Telegram.Enabled = true },
		"telegram.bot_token":              func(c *Config) { c.Telegram.BotToken = "t" },
		"telegram.chat_id":                func(c *Config) { c.Telegram.ChatID = "1" },
		"telegram.control_enabled":        func(c *Config) { c.Telegram.ControlEnabled = true },
		"telegram.allowed_chat_ids":       func(c *Config) { c.Telegram.AllowedChatIDs = []string{"2"} },
		"telegram.detail":                 func(c *Config) { c.Telegram.Detail = "simple" },
		"telegram.lang":                   func(c *Config) { c.Telegram.Lang = "en" },
		"telegram.pc_name":                func(c *Config) { c.Telegram.PCName = "pc" },
		"telegram.quiet_hours":            func(c *Config) { c.Telegram.QuietHours.Enabled = true },
		"smartthings.allowed_hubs":        func(c *Config) { c.SmartThings.AllowedHubs = []string{"10.0.0.2"} },
		"smartthings.expose_session":      func(c *Config) { c.SmartThings.ExposeSession = true },
		"smartthings.expose_session_user": func(c *Config) { c.SmartThings.ExposeSessionUser = true },
		"smartthings.wol_mac":             func(c *Config) { c.SmartThings.WoLMAC = "B4-2E-99-45-B4-F5" },
		"activity.enabled":                func(c *Config) { c.Activity.Enabled = true },
		"activity.watch":                  func(c *Config) { c.Activity.Watch = []ActivityWatch{{Slot: 1, Process: "a.exe", Label: "a"}} },
		"media.enabled":                   func(c *Config) { c.Media.Enabled = false },
		"media.now_playing":               func(c *Config) { c.Media.NowPlaying = true },
		"notify_pc.enabled":               func(c *Config) { c.NotifyPC.Enabled = false },
	}
	if len(changes) != len(auditedKeys) {
		t.Fatalf("%d cases for %d audited keys", len(changes), len(auditedKeys))
	}
	all := base
	for _, key := range auditedKeys {
		c := base
		changes[key](&c)
		changes[key](&all)
		if got := ChangedKeys(base, c); !slices.Equal(got, []string{key}) {
			t.Errorf("%s: ChangedKeys = %v", key, got)
		}
	}
	if got := strings.Join(ChangedKeys(base, all), ","); got != strings.Join(auditedKeys, ",") {
		t.Errorf("all changed: %s", got)
	}
	// A watch entry's slot is its priority and routine condition (#123):
	// moving an entry to another slot alone is a change.
	a, b := base, base
	a.Activity.Watch = []ActivityWatch{{Slot: 1, Process: "a.exe", Label: "A"}, {Slot: 2, Process: "b.exe", Label: "B"}}
	b.Activity.Watch = []ActivityWatch{{Slot: 1, Process: "a.exe", Label: "A"}, {Slot: 3, Process: "b.exe", Label: "B"}}
	if got := ChangedKeys(a, b); !slices.Equal(got, []string{"activity.watch"}) {
		t.Errorf("slot change: ChangedKeys = %v", got)
	}
	// Not audited, and a missing list equals an empty one.
	quiet := base
	quiet.ShutdownGrace = !base.ShutdownGrace
	quiet.GraceSeconds = 30
	quiet.Awake.KeepDisplay = true
	quiet.Telegram.AllowedChatIDs = nil
	if got := ChangedKeys(base, quiet); len(got) != 0 {
		t.Errorf("unaudited changes reported: %v", got)
	}
}
