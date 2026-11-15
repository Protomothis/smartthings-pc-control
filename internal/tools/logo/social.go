package main

// docs/social-preview.png, the 1280×640 card GitHub shows for links: the
// logo and name, the tagline, the feature chips, and the desktop app's
// home screen (docs/gui-home.png). Text uses Segoe UI and Malgun Gothic
// from the Windows font folder, which are not redistributable and so not
// in the repository.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	colInk    = color.NRGBA{0x19, 0x1F, 0x28, 0xFF}
	colSub    = color.NRGBA{0x4E, 0x59, 0x68, 0xFF}
	colFaint  = color.NRGBA{0x8B, 0x95, 0xA1, 0xFF}
	colBlue   = color.NRGBA{0x31, 0x82, 0xF6, 0xFF}
	colChipBg = color.NRGBA{0xE8, 0xF3, 0xFF, 0xFF}
	colChipFg = color.NRGBA{0x1B, 0x64, 0xDA, 0xFF}
)

func renderSocial(root, fontDir string, logo *drawing) error {
	const w, h = 1280, 640
	face := func(file string, size float64) (font.Face, error) {
		b, err := os.ReadFile(filepath.Join(fontDir, file))
		if err != nil {
			return nil, err
		}
		f, err := opentype.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	}
	faces := map[string]font.Face{}
	for _, f := range []struct {
		key, file string
		size      float64
	}{
		{"title", "segoeuib.ttf", 66},
		{"tag", "seguisb.ttf", 30},
		{"ko", "malgun.ttf", 24},
		{"chip", "seguisb.ttf", 20},
		{"foot", "segoeui.ttf", 20},
	} {
		fc, err := face(f.file, f.size)
		if err != nil {
			return err
		}
		faces[f.key] = fc
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	// A soft vertical wash, white to a pale blue.
	for y := 0; y < h; y++ {
		t := float64(y) / h
		c := color.NRGBA{lerp(0xFF, 0xEA, t), lerp(0xFF, 0xF2, t), 0xFF, 0xFF}
		draw.Draw(img, image.Rect(0, y, w, y+1), &image.Uniform{c}, image.Point{}, draw.Src)
	}

	// Logo and name.
	draw.Draw(img, image.Rect(76, 84, 76+120, 84+120), logo.render(120, 8), image.Point{}, draw.Over)
	text(img, faces["title"], 220, 136, colInk, "SmartThings")
	text(img, faces["title"], 220, 200, colBlue, "PC Control")

	// Tagline.
	text(img, faces["tag"], 76, 278, colInk, "Control Windows PC power from")
	text(img, faces["tag"], 76, 318, colInk, "SmartThings & Telegram")
	text(img, faces["ko"], 76, 368, colSub, "Windows 서비스 + 트레이 앱 · 로그인 불필요")
	text(img, faces["ko"], 76, 402, colSub, "PCControl Edge 드라이버 호환 · 유예 · 예약 · 알림")

	// Feature chips.
	rows := [][]string{
		{"Grace period & cancel", "Scheduled shutdown"},
		{"Telegram notifications", "Telegram remote control"},
		{"Wake-on-LAN status", "Signed auto-update"},
	}
	y := 430
	for _, row := range rows {
		x := 76
		for _, label := range row {
			cw := font.MeasureString(faces["chip"], label).Ceil() + 36
			fillRoundRect(img, image.Rect(x, y, x+cw, y+40), 20, colChipBg)
			text(img, faces["chip"], x+18, y+27, colChipFg, label)
			x += cw + 10
		}
		y += 50
	}
	text(img, faces["foot"], 76, 612, colFaint, "github.com/Protomothis/smartthings-pc-control  ·  MIT  ·  Go + Gio")

	// The home screen, in a card with a soft shadow.
	f, err := os.Open(filepath.Join(root, "docs", "gui-home.png"))
	if err != nil {
		return err
	}
	shot, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	const sh = 580
	sw := sh * shot.Bounds().Dx() / shot.Bounds().Dy()
	card := image.Rect(w-64-sw, 30, w-64, 30+sh)
	for i := 24; i > 0; i -= 2 {
		a := uint8(6 * (24 - i) / 24)
		fillRoundRect(img, card.Inset(-i).Add(image.Pt(0, 8)), 28+float64(i), color.NRGBA{0x1A, 0x2B, 0x4A, a})
	}
	scaled := image.NewNRGBA(image.Rect(0, 0, sw, sh))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), shot, shot.Bounds(), xdraw.Src, nil)
	maskRoundRect(scaled, 24)
	draw.Draw(img, card, scaled, image.Point{}, draw.Over)

	writePNG(filepath.Join(root, "docs", "social-preview.png"), img)
	return nil
}

func lerp(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}

func text(dst draw.Image, f font.Face, x, y int, c color.NRGBA, s string) {
	d := font.Drawer{Dst: dst, Src: &image.Uniform{c}, Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// roundCov is the coverage of pixel (x, y) by the rounded rectangle r.
func roundCov(r image.Rectangle, rad float64, x, y int) float64 {
	const n = 4
	hit := 0
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			px := float64(x) + (float64(i)+0.5)/n
			py := float64(y) + (float64(j)+0.5)/n
			if inRoundRect(px, py, float64(r.Min.X), float64(r.Min.Y), float64(r.Dx()), float64(r.Dy()), rad) {
				hit++
			}
		}
	}
	return float64(hit) / (n * n)
}

func fillRoundRect(dst *image.NRGBA, r image.Rectangle, rad float64, c color.NRGBA) {
	clip := r.Intersect(dst.Bounds())
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		for x := clip.Min.X; x < clip.Max.X; x++ {
			cov := roundCov(r, rad, x, y)
			if cov == 0 {
				continue
			}
			src := color.NRGBA{c.R, c.G, c.B, uint8(math.Round(float64(c.A) * cov))}
			draw.Draw(dst, image.Rect(x, y, x+1, y+1), &image.Uniform{src}, image.Point{}, draw.Over)
		}
	}
}

func maskRoundRect(img *image.NRGBA, rad float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			c.A = uint8(math.Round(float64(c.A) * roundCov(b, rad, x, y)))
			img.SetNRGBA(x, y, c)
		}
	}
}
