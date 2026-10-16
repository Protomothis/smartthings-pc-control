package config

import "testing"

func TestPrivacyDefaults(t *testing.T) {
	d := Default().WithDefaults().Privacy
	if d.Enabled || d.Guard != PrivacyGuardWarn {
		t.Fatalf("default privacy = %+v, want off with guard warn", d)
	}
	// A missing guard (an older client) is warn; an explicit one is kept.
	if g := (PrivacyConfig{Enabled: true}).WithDefaults().Guard; g != PrivacyGuardWarn {
		t.Errorf("missing guard = %q", g)
	}
	if g := (PrivacyConfig{Guard: PrivacyGuardBlock}).WithDefaults().Guard; g != PrivacyGuardBlock {
		t.Errorf("block kept as %q", g)
	}
}

func TestValidatePrivacy(t *testing.T) {
	for _, g := range []string{"", "off", "warn", "block"} {
		if msg := ValidatePrivacy(PrivacyConfig{Guard: g}); msg != "" {
			t.Errorf("%q refused: %s", g, msg)
		}
	}
	for _, g := range []string{"Block", "deny", "on"} {
		if msg := ValidatePrivacy(PrivacyConfig{Guard: g}); msg == "" {
			t.Errorf("%q accepted", g)
		}
	}
}

func TestLoadSanitizesPrivacyGuard(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"privacy": {"enabled": true, "guard": "nonsense"}}`)
	got := Load(dir).Privacy
	if !got.Enabled || got.Guard != PrivacyGuardWarn {
		t.Fatalf("loaded %+v, want enabled with guard warn", got)
	}
	dir = t.TempDir()
	writeConfig(t, dir, `{"privacy": {"enabled": true, "guard": "block"}}`)
	if got := Load(dir).Privacy; got.Guard != PrivacyGuardBlock {
		t.Fatalf("loaded %+v, want block", got)
	}
}
