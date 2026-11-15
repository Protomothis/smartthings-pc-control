package main

import (
	"image"
	"image/color"
	"image/draw"
)

// contactSheet lays every size out on a light and a dark background, then
// the tray sizes zoomed 4× (nearest neighbour): the native small frames,
// and the tray frames halved the way the shell shows them.
func contactSheet(imgs, tray map[int]*image.NRGBA) *image.NRGBA {
	const w, pad = 1000, 16
	bgs := []color.NRGBA{{0xFF, 0xFF, 0xFF, 0xFF}, {0x20, 0x20, 0x24, 0xFF}}
	rowH := 256 + 2*pad
	zoomH := 4*32 + 2*pad
	sheet := image.NewNRGBA(image.Rect(0, 0, w, len(bgs)*(rowH+zoomH)))
	y := 0
	for _, bg := range bgs {
		draw.Draw(sheet, image.Rect(0, y, w, y+rowH+zoomH), &image.Uniform{bg}, image.Point{}, draw.Src)
		x := pad
		for _, n := range sizes {
			put(sheet, imgs[n], x, y+pad+256-n, 1)
			x += n + pad
		}
		y += rowH
		x = pad
		for _, n := range []int{16, 20, 24, 32} {
			put(sheet, imgs[n], x, y+pad, 4)
			x += 4*n + pad
		}
		x += 2 * pad
		for _, n := range []int{32, 40, 48, 64} {
			put(sheet, halve(tray[n]), x, y+pad, 4)
			x += 2*n + pad
		}
		y += zoomH
	}
	return sheet
}

// put draws src at (x, y) scaled by an integer factor, nearest neighbour.
func put(dst *image.NRGBA, src *image.NRGBA, x, y, k int) {
	b := src.Bounds()
	big := image.NewNRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for py := 0; py < b.Dy()*k; py++ {
		for px := 0; px < b.Dx()*k; px++ {
			big.SetNRGBA(px, py, src.NRGBAAt(px/k, py/k))
		}
	}
	draw.Draw(dst, big.Bounds().Add(image.Pt(x, y)), big, image.Point{}, draw.Over)
}

// halve box-filters src to half size (premultiplied).
func halve(src *image.NRGBA) *image.NRGBA {
	n := src.Bounds().Dx() / 2
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a float64
			for j := 0; j < 2; j++ {
				for i := 0; i < 2; i++ {
					c := src.NRGBAAt(2*x+i, 2*y+j)
					al := float64(c.A)
					r += float64(c.R) * al
					g += float64(c.G) * al
					b += float64(c.B) * al
					a += al
				}
			}
			if a > 0 {
				out.SetNRGBA(x, y, color.NRGBA{uint8(r/a + 0.5), uint8(g/a + 0.5), uint8(b/a + 0.5), uint8(a/4 + 0.5)})
			}
		}
	}
	return out
}
