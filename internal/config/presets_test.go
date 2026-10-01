package config

// Preset rules (#109). Moved here from the service package with the code
// (#127).

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidatePreset(t *testing.T) {
	ok := []Preset{
		{Slot: 1, Name: "게임 모드", Type: "program", Path: `C:\Games\Steam\steam.exe`, Args: []string{"-bigpicture"}},
		{Slot: 10, Name: strings.Repeat("가", 30), Type: "program", Path: `\\nas\apps\tool.exe`},
		{Slot: 2, Name: "대시보드", Type: "url", Path: "http://192.168.1.50:3000/d/home"},
		{Slot: 3, Name: "방송", Type: "url", Path: "https://example.com/?a=1&b=2"},
		{Slot: 4, Name: "PS", Type: "script", Path: `C:\Scripts\game.ps1`, Args: []string{"-Mode", `"on"`, "%x%"}},
		{Slot: 5, Name: "Batch", Type: "script", Path: `C:\Scripts\go.CMD`, Args: []string{"a&b", "two words"}},
		{Slot: 6, Name: "Bat", Type: "script", Path: `C:\Scripts\go.bat`},
	}
	for _, p := range ok {
		if err := ValidatePreset(p); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	tooMany := make([]string, 33)
	bad := map[string]Preset{
		"slot 0":           {Slot: 0, Name: "x", Type: "url", Path: "https://x.com"},
		"slot 11":          {Slot: 11, Name: "x", Type: "url", Path: "https://x.com"},
		"no name":          {Slot: 1, Name: "  ", Type: "url", Path: "https://x.com"},
		"name spaces":      {Slot: 1, Name: " x", Type: "url", Path: "https://x.com"},
		"name 31":          {Slot: 1, Name: strings.Repeat("a", 31), Type: "url", Path: "https://x.com"},
		"name control":     {Slot: 1, Name: "a\nb", Type: "url", Path: "https://x.com"},
		"type":             {Slot: 1, Name: "x", Type: "shell", Path: `C:\x.exe`},
		"type case":        {Slot: 1, Name: "x", Type: "Program", Path: `C:\x.exe`},
		"no path":          {Slot: 1, Name: "x", Type: "program", Path: ""},
		"path spaces":      {Slot: 1, Name: "x", Type: "program", Path: ` C:\x.exe`},
		"relative":         {Slot: 1, Name: "x", Type: "program", Path: `x.exe`},
		"no drive":         {Slot: 1, Name: "x", Type: "program", Path: `\x.exe`},
		"bat as program":   {Slot: 1, Name: "x", Type: "program", Path: `C:\x.bat`},
		"relative script":  {Slot: 1, Name: "x", Type: "script", Path: `x.ps1`},
		"vbs script":       {Slot: 1, Name: "x", Type: "script", Path: `C:\x.vbs`},
		"quote in bat arg": {Slot: 1, Name: "x", Type: "script", Path: `C:\x.bat`, Args: []string{`a"b`}},
		"% in cmd arg":     {Slot: 1, Name: "x", Type: "script", Path: `C:\x.cmd`, Args: []string{"%PATH%"}},
		"file url":         {Slot: 1, Name: "x", Type: "url", Path: "file:///C:/Windows/System32/cmd.exe"},
		"javascript":       {Slot: 1, Name: "x", Type: "url", Path: "javascript:alert(1)"},
		"bare host":        {Slot: 1, Name: "x", Type: "url", Path: "example.com"},
		"url args":         {Slot: 1, Name: "x", Type: "url", Path: "https://x.com", Args: []string{"a"}},
		"33 args":          {Slot: 1, Name: "x", Type: "program", Path: `C:\x.exe`, Args: tooMany},
		"arg control":      {Slot: 1, Name: "x", Type: "program", Path: `C:\x.exe`, Args: []string{"a\x00"}},
	}
	for name, p := range bad {
		if err := ValidatePreset(p); err == nil {
			t.Errorf("%s: accepted %+v", name, p)
		}
	}
}

func TestPresetsDefault(t *testing.T) {
	cfg := Default().WithDefaults()
	if cfg.Presets == nil || len(cfg.Presets) != 0 {
		t.Errorf("presets default = %#v, want []", cfg.Presets)
	}
}

func TestValidatePresetsAndDrop(t *testing.T) {
	a := Preset{Slot: 1, Name: "a", Type: "url", Path: "https://a.example"}
	b := Preset{Slot: 2, Name: "b", Type: "url", Path: "https://b.example"}
	dup := Preset{Slot: 1, Name: "dup", Type: "url", Path: "https://c.example"}
	invalid := Preset{Slot: 3, Name: "bad", Type: "program", Path: "relative.exe"}

	if msg := ValidatePresets([]Preset{a, b}); msg != "" {
		t.Error(msg)
	}
	if msg := ValidatePresets([]Preset{a, dup}); !strings.Contains(msg, "slot 1 is used twice") {
		t.Errorf("duplicate: %q", msg)
	}
	if msg := ValidatePresets([]Preset{a, invalid}); !strings.Contains(msg, "slot 3") || !strings.Contains(msg, "absolute") {
		t.Errorf("invalid: %q", msg)
	}
	got := DropInvalidPresets([]Preset{a, invalid, dup, b})
	if !reflect.DeepEqual(got, []Preset{a, b}) {
		t.Errorf("dropInvalidPresets = %+v", got)
	}
	if got := DropInvalidPresets(nil); got == nil || len(got) != 0 {
		t.Errorf("nil list = %#v", got)
	}
}

func TestChangedPresetSlotsAndConfigKey(t *testing.T) {
	a := Preset{Slot: 1, Name: "a", Type: "url", Path: "https://a.example"}
	b := Preset{Slot: 2, Name: "b", Type: "program", Path: `C:\b.exe`, Args: []string{"x"}}
	b2 := b
	b2.Args = []string{"y"}
	c := Preset{Slot: 3, Name: "c", Type: "url", Path: "https://c.example"}

	if got := ChangedPresetSlots([]Preset{a, b}, []Preset{b, a}); len(got) != 0 {
		t.Errorf("reordered = %v, want no change", got)
	}
	if got := ChangedPresetSlots([]Preset{a, b}, []Preset{b2, c}); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("changed = %v, want [1 2 3]", got)
	}
	if got := PresetChangeKey([]int{1, 3}); got != "presets[1,3]" {
		t.Errorf("key = %q", got)
	}

	old := Default().WithDefaults()
	updated := old
	updated.Presets = []Preset{b}
	updated.NotifyPC.Enabled = false
	keys := strings.Join(ChangedKeys(old, updated), ", ")
	if keys != "notify_pc.enabled, presets[2]" {
		t.Errorf("keys = %q", keys)
	}
	// The key names slots only: never the path or the arguments.
	if strings.Contains(keys, "b.exe") {
		t.Error("config_changed leaks the preset path")
	}
}
