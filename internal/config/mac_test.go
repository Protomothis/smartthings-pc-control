package config

import (
	"slices"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	const want = "B4-2E-99-45-B4-F5"
	for _, in := range []string{
		"B4-2E-99-45-B4-F5",
		"b4-2e-99-45-b4-f5",
		"b4:2e:99:45:b4:f5",
		"B4:2E:99:45:B4:F5",
		"b4.2e.99.45.b4.f5",
		"b42e9945b4f5",
		"  b4:2E:99:45:b4:F5  ",
	} {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything that is not a 6-byte MAC is "no MAC" rather than a value
	// that could never match an adapter.
	for _, in := range []string{"", "   ", "B4-2E-99-45-B4", "B4-2E-99-45-B4-F5-00", "not a mac", "B4-2E-99-45-B4-FG", "192.168.1.30"} {
		if got := NormalizeMAC(in); got != "" {
			t.Errorf("NormalizeMAC(%q) = %q, want \"\"", in, got)
		}
	}
}

// withDefaults is where a MAC typed by a user becomes the stored form, and
// where nonsense becomes "" (= automatic) rather than a value that could
// never match.
func TestSmartThingsConfigNormalisesWoLMAC(t *testing.T) {
	for in, want := range map[string]string{
		"b4:2e:99:45:b4:f5": "B4-2E-99-45-B4-F5",
		"B4-2E-99-45-B4-F5": "B4-2E-99-45-B4-F5",
		"  b42e9945b4f5  ":  "B4-2E-99-45-B4-F5",
		"":                  "",
		"auto":              "",
		"B4-2E-99-45-B4":    "",
	} {
		if got := (SmartThingsConfig{WoLMAC: in}).WithDefaults().WoLMAC; got != want {
			t.Errorf("withDefaults(%q).WoLMAC = %q, want %q", in, got, want)
		}
	}
	// A changed pin is a config change the security event reports.
	old := Config{SmartThings: SmartThingsConfig{}}
	new := Config{SmartThings: SmartThingsConfig{WoLMAC: "B4-2E-99-45-B4-F5"}}
	if !slices.Contains(ChangedKeys(old, new), "smartthings.wol_mac") {
		t.Errorf("configChangedKeys = %v, want smartthings.wol_mac", ChangedKeys(old, new))
	}
}
