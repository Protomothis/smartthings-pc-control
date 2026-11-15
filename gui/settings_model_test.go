package gui

import (
	"testing"
)

func TestNotifyPCStateRoundTrip(t *testing.T) {
	base := Config{Port: 5001, NotifyPC: NotifyPCConfig{Enabled: true}, Media: MediaConfig{Enabled: true}}
	s := notifyPCStateFromConfig(base)
	if s.dirty(base) {
		t.Error("an unedited section is dirty")
	}
	cfg := base
	s.applyTo(&cfg)
	if !cfg.NotifyPC.Enabled || !cfg.Media.Enabled {
		t.Errorf("applyTo = %+v", cfg)
	}
	s.Enabled = false
	if !s.dirty(base) {
		t.Error("notifications off is not dirty")
	}
	s.applyTo(&cfg)
	if cfg.NotifyPC.Enabled || !cfg.Media.Enabled {
		t.Errorf("applyTo off = %+v", cfg)
	}
}
