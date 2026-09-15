package gui

import (
	"image/color"
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

// sizeNameCountdown is the custom text size used by the schedule tab's
// remaining-time display (RichText can only pick sizes by theme name).
const sizeNameCountdown fyne.ThemeSizeName = "countdown"

// Color lifts the dark-variant "disabled" and placeholder greys. Fyne's
// defaults (~28% white) make hint text and disabled controls close to
// invisible on the dark background; ~55% keeps them clearly secondary but
// readable. Light mode is left as is.
func (t *koreanTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if variant == theme.VariantDark {
		switch name {
		case theme.ColorNameDisabled:
			return color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x8c}
		case theme.ColorNamePlaceHolder:
			return color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x99}
		}
	}
	return t.Theme.Color(name, variant)
}

func (t *koreanTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == sizeNameCountdown {
		return 40
	}
	return t.Theme.Size(name)
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
