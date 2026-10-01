package config

import (
	"fmt"
	"reflect"
	"strings"
)

// auditedKeys are the security-relevant settings that security.config_changed
// reports, as config.json paths, in the order the notification lists them.
// Each path is resolved through the json tags (auditedFields), so a key
// name can never drift from the file; TestEveryKeyIsClassified makes sure a
// new setting is either here or in notAuditedKeys.
//
// What the hub learns about running programs (activity.*) is
// privacy-relevant too, and PC notifications and presets let the network
// act in the user's session (#106, #109). presets is reported separately,
// by slot (PresetChangeKey), never with what a slot runs.
var auditedKeys = []string{
	"secret",
	"port",
	"webui_remote",
	"telegram.enabled",
	"telegram.bot_token",
	"telegram.chat_id",
	"telegram.control_enabled",
	"telegram.allowed_chat_ids",
	"telegram.detail",
	"telegram.lang",
	"telegram.pc_name",
	"telegram.quiet_hours",
	"smartthings.allowed_hubs",
	"smartthings.expose_session",
	"smartthings.expose_session_user",
	"smartthings.wol_mac",
	"activity.enabled",
	"activity.watch",
	"media.enabled",
	"media.now_playing",
	"notify_pc.enabled",
}

// auditedField is one auditedKeys entry resolved to its struct field.
type auditedField struct {
	key   string
	index []int
}

// auditedFields is auditedKeys resolved once; a path that names no field
// panics at start-up, so every test of the package catches a typo.
var auditedFields = func() []auditedField {
	t := reflect.TypeFor[Config]()
	out := make([]auditedField, 0, len(auditedKeys))
	for _, key := range auditedKeys {
		index, err := jsonFieldIndex(t, key)
		if err != nil {
			panic(err)
		}
		out = append(out, auditedField{key: key, index: index})
	}
	return out
}()

// jsonFieldIndex finds the field a dotted config.json path names.
func jsonFieldIndex(t reflect.Type, path string) ([]int, error) {
	var index []int
	for _, name := range strings.Split(path, ".") {
		if t.Kind() != reflect.Struct {
			return nil, fmt.Errorf("config: %q: %s is not an object", path, t)
		}
		found := false
		for i := range t.NumField() {
			f := t.Field(i)
			if jsonName(f) == name {
				index = append(index, i)
				t = f.Type
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("config: %q names no field", path)
		}
	}
	return index, nil
}

// jsonName is a field's key in config.json.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}

// sameValue compares two settings: lists element by element (a missing
// list equals an empty one, like slices.Equal), everything else with ==.
func sameValue(a, b reflect.Value) bool {
	if a.Kind() == reflect.Slice {
		if a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			if !a.Index(i).Equal(b.Index(i)) {
				return false
			}
		}
		return true
	}
	return a.Equal(b)
}

// ChangedKeys lists the security-relevant settings that differ between
// old and new, for the security.config_changed event. Values are never
// included — only key names.
func ChangedKeys(old, new Config) []string {
	var keys []string
	o, n := reflect.ValueOf(old), reflect.ValueOf(new)
	for _, f := range auditedFields {
		if !sameValue(o.FieldByIndex(f.index), n.FieldByIndex(f.index)) {
			keys = append(keys, f.key)
		}
	}
	if slots := ChangedPresetSlots(old.Presets, new.Presets); len(slots) > 0 {
		keys = append(keys, PresetChangeKey(slots))
	}
	return keys
}
