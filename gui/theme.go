package gui

import (
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// koreanTheme wraps the default theme but serves Malgun Gothic (bundled with
// Windows) so Korean text renders instead of tofu. Fonts are loaded from the
// system at runtime — nothing is redistributed in the binary.
type koreanTheme struct {
	fyne.Theme
	regular fyne.Resource
	bold    fyne.Resource
}

func newKoreanTheme() fyne.Theme {
	t := &koreanTheme{Theme: theme.DefaultTheme()}
	fontsDir := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	if data, err := os.ReadFile(filepath.Join(fontsDir, "malgun.ttf")); err == nil {
		t.regular = fyne.NewStaticResource("malgun.ttf", data)
	}
	if data, err := os.ReadFile(filepath.Join(fontsDir, "malgunbd.ttf")); err == nil {
		t.bold = fyne.NewStaticResource("malgunbd.ttf", data)
	}
	return t
}

func (t *koreanTheme) Font(style fyne.TextStyle) fyne.Resource {
	// Keep the default monospace font; logs are ASCII.
	if style.Monospace {
		return t.Theme.Font(style)
	}
	if style.Bold && t.bold != nil {
		return t.bold
	}
	if t.regular != nil {
		return t.regular
	}
	return t.Theme.Font(style)
}
