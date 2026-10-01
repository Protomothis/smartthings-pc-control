package stapi

import (
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

func TestSTPushDropsSecretValuedFields(t *testing.T) {
	got := pushData(map[string]string{"command": "shutdown", "leak": "s3cr3t"}, "s3cr3t")
	if _, ok := got["leak"]; ok {
		t.Errorf("a field holding the secret was kept: %v", got)
	}
	if got["command"] != "shutdown" {
		t.Errorf("data = %v", got)
	}
}

func TestSTPushEventSelection(t *testing.T) {
	exposed := config.SmartThingsConfig{ExposeSession: true}
	var hidden config.SmartThingsConfig

	for _, tc := range []struct {
		cat, kind string
		cfg       config.SmartThingsConfig
		want      bool
	}{
		{"power", "stopping", hidden, true},
		{"power", "started", hidden, true},
		{"power", "resumed", hidden, true},
		{"schedule", "created", hidden, true},
		{"schedule", "cancelled", hidden, true},
		{"remote", "received", hidden, true},
		{"remote", "grace_scheduled", hidden, true},
		{"system", "updated", hidden, true},
		{"system", "update_available", hidden, true},
		{"display", "changed", hidden, true},
		{"awake", "changed", hidden, true},
		{"battery", "changed", hidden, true},
		{"session", "locked", exposed, true},
		{"session", "unlocked", exposed, true},
		{"session", "locked", hidden, false},
		{"security", "unauthorized", hidden, false},
		{"system", "exec_failed", hidden, false},
		{"system", "digest", hidden, false},
	} {
		typ, ok := pushEventType(notify.Event{Category: tc.cat, Kind: tc.kind}, tc.cfg)
		if ok != tc.want {
			t.Errorf("%s.%s pushed = %v, want %v", tc.cat, tc.kind, ok, tc.want)
		}
		if ok && typ != tc.cat+"."+tc.kind {
			t.Errorf("type = %q, want %s.%s", typ, tc.cat, tc.kind)
		}
	}
}

func TestActivityPushEventSelection(t *testing.T) {
	if typ, ok := pushEventType(notify.Event{Category: "activity", Kind: "changed"}, config.SmartThingsConfig{}); !ok || typ != "activity.changed" {
		t.Errorf("activity.changed pushed = %v (%q)", ok, typ)
	}
	if _, ok := pushEventType(notify.Event{Category: "activity", Kind: "other"}, config.SmartThingsConfig{}); ok {
		t.Error("an unknown activity kind is pushed")
	}
}
