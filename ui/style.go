package ui

// Design tokens, copied from proto-easy-mount (internal/gui/style.go) so the
// two apps look alike: the 12-colour Toss palette in a light and a dark set,
// three corner radii, four gaps and three text sizes. Components (widgets.go)
// read only these.

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

// Palette: calm surfaces with one strong accent. usePalette switches the
// colours between the light and dark set; only the window's goroutine (or a
// test, before any window exists) may call it.
var (
	colBg, colCard, colText, colSub, colFaint, colLine  color.NRGBA
	colAccent, colAccentDim, colDanger, colOK, colGhost color.NRGBA
	colOnAccent                                         color.NRGBA // text on accent fills
	// Derived, not part of the shared 12: the danger tint behind a
	// destructive confirmation and the warning colour of a remote
	// schedule.
	colDangerDim, colWarn color.NRGBA
	// colScrim dims the window behind a bottom sheet; the dark set needs
	// far more of it to read as a dimmed layer on a near-black window.
	colScrim color.NRGBA
)

type palette [12]uint32

var (
	lightPalette = palette{0xf2f4f6, 0xffffff, 0x191f28, 0x6b7684, 0xb0b8c1, 0xe5e8eb,
		0x3182f6, 0xe8f3ff, 0xf04452, 0x00a661, 0xf2f4f6, 0xffffff}
	darkPalette = palette{0x17171c, 0x222228, 0xe8e8ea, 0x9e9ea4, 0x5c5c66, 0x2e2e36,
		0x4593fc, 0x1c2a40, 0xfa616d, 0x1cc47b, 0x2e2e36, 0xffffff}
)

func init() { usePalette(false) }

func usePalette(dark bool) {
	p := lightPalette
	if dark {
		p = darkPalette
	}
	for i, c := range []*color.NRGBA{&colBg, &colCard, &colText, &colSub, &colFaint, &colLine,
		&colAccent, &colAccentDim, &colDanger, &colOK, &colGhost, &colOnAccent} {
		*c = rgb(p[i])
	}
	if dark {
		colDangerDim, colWarn = rgb(0x3a2226), rgb(0xffb331)
		colScrim = color.NRGBA{A: 0xb8}
	} else {
		colDangerDim, colWarn = rgb(0xffeeee), rgb(0xe08600)
		colScrim = color.NRGBA{A: 0x66}
	}
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

// Corner radii.
const (
	radiusS unit.Dp = 8  // chips, small buttons
	radiusM unit.Dp = 12 // fields, primary buttons, inner panels
	radiusL unit.Dp = 16 // cards
)

// Gaps.
const (
	gapXS unit.Dp = 6
	gapS  unit.Dp = 12
	gapM  unit.Dp = 16
	gapL  unit.Dp = 20
)

// Text sizes: captions, secondary lines, body. Titles are the only larger
// ones (titleSize for a card, headSize for a page).
const (
	textS     unit.Sp = 12
	textM     unit.Sp = 13
	textL     unit.Sp = 15
	titleSize unit.Sp = 17
	headSize  unit.Sp = 22
)

func newTheme() *material.Theme {
	th := material.NewTheme()
	// System fonts are loaded too, so Hangul falls back to the OS font.
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	th.Face = "Malgun Gothic"
	setThemeColors(th)
	th.TextSize = textL
	return th
}

// setThemeColors copies the current palette into th.
func setThemeColors(th *material.Theme) {
	th.Palette = material.Palette{Bg: colBg, Fg: colText, ContrastBg: colAccent, ContrastFg: colOnAccent}
}

// label is a text in the theme's face. Korean wraps at spaces only
// (keepWords), so no syllable is left alone on a line.
func label(th *material.Theme, size unit.Sp, txt string, col color.NRGBA, weight font.Weight) material.LabelStyle {
	l := material.Label(th, size, keepWords(txt))
	l.Color = col
	l.Font.Weight = weight
	return l
}

// line is a one-line label that ends in "…" instead of wrapping.
func line(th *material.Theme, size unit.Sp, txt string, col color.NRGBA, weight font.Weight) material.LabelStyle {
	l := label(th, size, txt, col, weight)
	l.MaxLines = 1
	return l
}

// surface fills the available width with a rounded background behind w.
func surface(gtx C, bg color.NRGBA, radius unit.Dp, w layout.Widget) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radius)).Op(gtx.Ops))
			return D{Size: gtx.Constraints.Min}
		}),
		// Stack lays its children out from a zero minimum; give the content
		// the full width back so short content still gets a full panel.
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		}),
	)
}

// card draws a white rounded panel that fills the available width.
func card(gtx C, w layout.Widget) D {
	return surface(gtx, colCard, radiusL, func(gtx C) D {
		return layout.UniformInset(gapL).Layout(gtx, w)
	})
}

func vspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}

func hspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Width: dp}.Layout)
}

// column stacks children vertically.
func column(gtx C, children ...layout.FlexChild) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// row lays children out horizontally, centred on the cross axis.
func row(gtx C, children ...layout.FlexChild) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
}

// fillBg paints the window background.
func fillBg(gtx C) { paint.Fill(gtx.Ops, colBg) }
