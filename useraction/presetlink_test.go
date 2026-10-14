package useraction

import (
	"errors"
	"testing"
)

func TestPresetURLScheme(t *testing.T) {
	for raw, want := range map[string]string{
		"https://example.com/d?token=abc":                      "https",
		"http://192.168.1.50:3000/d/home":                      "http",
		"HTTPS://Example.com":                                  "https",
		"steam://rungameid/570":                                "steam",
		"steam://run/570//-novid -console/":                    "steam",
		"spotify:track:4uLU6hMCjMI75M1A2tKUQC":                 "spotify",
		"ms-settings:display":                                  "ms-settings",
		"discord://-/channels/1/2":                             "discord",
		"com.epicgames.launcher://apps/Fortnite?action=launch": "com.epicgames.launcher",
	} {
		got, err := PresetURLScheme(raw)
		if err != nil || got != want {
			t.Errorf("PresetURLScheme(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for raw, want := range map[string]error{
		"example.com":                         ErrPresetURLInvalid,
		"https://":                            ErrPresetURLInvalid,
		"http:///path":                        ErrPresetURLInvalid,
		`C:\Games\x.exe`:                      ErrPresetURLInvalid,
		"c:/x":                                ErrPresetURLInvalid,
		"steam:":                              ErrPresetURLInvalid,
		"steam://":                            ErrPresetURLInvalid,
		"1steam://x":                          ErrPresetURLInvalid,
		"st eam://x":                          ErrPresetURLInvalid,
		":x":                                  ErrPresetURLInvalid,
		"":                                    ErrPresetURLInvalid,
		"file:///C:/Windows/System32/cmd.exe": ErrPresetURLBlocked,
		"FILE://server/share/x":               ErrPresetURLBlocked,
		"javascript:alert(1)":                 ErrPresetURLBlocked,
		"vbscript:x":                          ErrPresetURLBlocked,
		`search-ms:query=x&crumb=location:\\evil\s`: ErrPresetURLBlocked,
		"ms-msdt:/id PCWDiagnostic":                 ErrPresetURLBlocked,
		"ms-officecmd:{}":                           ErrPresetURLBlocked,
		"ms-word:ofe|u|https://evil/x.docx":         ErrPresetURLBlocked,
		"ms-appinstaller:?source=https://evil/x":    ErrPresetURLBlocked,
		"shell:startup":                             ErrPresetURLBlocked,
	} {
		if _, err := PresetURLScheme(raw); !errors.Is(err, want) {
			t.Errorf("PresetURLScheme(%q) error = %v, want %v", raw, err, want)
		}
	}
}

func TestParsePresetTakesAppLinks(t *testing.T) {
	req, err := Parse([]string{"preset", "--type", "url", "--path", "steam://rungameid/570"})
	if err != nil || req.Path != "steam://rungameid/570" {
		t.Fatalf("app link: %+v, %v", req, err)
	}
	_, err = Parse([]string{"preset", "--type", "url", "--path", "search-ms:query=x"})
	var ue *Error
	if !errors.As(err, &ue) || ue.Code != CodeBadArgs {
		t.Fatalf("blocked link: %v", err)
	}
}
