// Command logo renders the app logo from the SVG masters in assets/logo
// into every raster the repository ships, so they can be regenerated
// after a design change. Run it from the repository root:
//
//	go run ./internal/tools/logo            # PNGs, icon.ico, gui/icon.png, gui/tray.ico
//	go run ./internal/tools/logo -social    # also docs/social-preview.png (needs Windows fonts)
//	go run ./internal/tools/logo -contact sheet.png   # a contact sheet to eyeball the sizes
//
// Afterwards rebuild rsrc_windows_amd64.syso from icon.ico (CONTRIBUTING.md).
//
// logo.svg is the design; logo-16/-20/-24.svg are its small variants (one
// wave at 20-24 px, none at 16 px) and logo-32.svg the large one hinted to
// whole pixels. Each size uses the master drawn for it.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

// sizes are the rasters written to assets/logo/png and put in icon.ico.
var sizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

// supersamples per pixel side.
const ss = 16

// masters are the drawings: logo.svg, and the variants hinted to the
// pixel grid of one size each (logo-16/-20/-24/-32.svg).
type masters struct {
	large  *drawing
	hinted map[int]*drawing
}

func loadMasters(dir string) (*masters, error) {
	m := &masters{hinted: map[int]*drawing{}}
	var err error
	if m.large, err = loadSVG(filepath.Join(dir, "logo.svg")); err != nil {
		return nil, err
	}
	for _, n := range []int{16, 20, 24, 32} {
		if m.hinted[n], err = loadSVG(filepath.Join(dir, fmt.Sprintf("logo-%d.svg", n))); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// forSize is the master drawn for an n-pixel icon.
func (m *masters) forSize(n int) *drawing {
	if d, ok := m.hinted[n]; ok {
		return d
	}
	return m.large
}

// trayFrames are the tray icon's frames. fyne.io/systray loads the icon
// with LR_DEFAULTSIZE, i.e. at SM_CXICON (32 px at 100 % scale, 40, 48,
// 64 at 125/150/200 %), and the shell shrinks that by exactly half to the
// small-icon size. So each frame is the small-size design drawn at twice
// its size, which halves cleanly; the native small frames are kept too.
var trayFrames = []struct{ size, design int }{
	{16, 16}, {20, 20}, {24, 24}, {32, 16}, {40, 20}, {48, 24}, {64, 32},
}

func main() {
	root := flag.String("root", ".", "repository root")
	social := flag.Bool("social", false, "also render docs/social-preview.png")
	fontDir := flag.String("fonts", `C:\Windows\Fonts`, "directory with segoeui*.ttf and malgun*.ttf (for -social)")
	contact := flag.String("contact", "", "write a contact sheet PNG here")
	flag.Parse()
	log.SetFlags(0)

	m, err := loadMasters(filepath.Join(*root, "assets", "logo"))
	if err != nil {
		log.Fatal(err)
	}
	imgs := map[int]*image.NRGBA{}
	pngDir := filepath.Join(*root, "assets", "logo", "png")
	if err := os.MkdirAll(pngDir, 0o755); err != nil {
		log.Fatal(err)
	}
	var frames []icoFrame
	for _, n := range sizes {
		img := m.forSize(n).render(n, ss)
		imgs[n] = img
		writePNG(filepath.Join(pngDir, fmt.Sprintf("logo-%d.png", n)), img)
		frames = append(frames, icoFrame{img: img, png: n >= 128})
	}
	writeFile(filepath.Join(*root, "icon.ico"), buildICO(frames))
	writePNG(filepath.Join(*root, "gui", "icon.png"), imgs[256])

	var tray []icoFrame
	trayImgs := map[int]*image.NRGBA{}
	for _, f := range trayFrames {
		img := m.forSize(f.design).render(f.size, ss)
		trayImgs[f.size] = img
		tray = append(tray, icoFrame{img: img})
	}
	writeFile(filepath.Join(*root, "gui", "tray.ico"), buildICO(tray))

	if *social {
		if err := renderSocial(*root, *fontDir, m.large); err != nil {
			log.Fatal(err)
		}
	}
	if *contact != "" {
		writePNG(*contact, contactSheet(imgs, trayImgs))
	}
}

func writePNG(path string, img image.Image) {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	writeFile(path, b.Bytes())
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s (%d bytes)", path, len(data))
}

type icoFrame struct {
	img *image.NRGBA
	png bool // store PNG-compressed instead of as a DIB
}

// buildICO writes a multi-image .ico: 32-bit DIBs (with an empty AND mask,
// alpha carries the shape) for the small frames, PNG for the large ones.
func buildICO(frames []icoFrame) []byte {
	le := binary.LittleEndian
	data := make([][]byte, len(frames))
	for i, f := range frames {
		if f.png {
			var b bytes.Buffer
			if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, f.img); err != nil {
				log.Fatal(err)
			}
			data[i] = b.Bytes()
			continue
		}
		data[i] = dib(f.img)
	}
	var b bytes.Buffer
	_ = binary.Write(&b, le, [3]uint16{0, 1, uint16(len(frames))})
	off := 6 + 16*len(frames)
	for i, f := range frames {
		n := f.img.Bounds().Dx()
		dim := byte(n)
		if n >= 256 {
			dim = 0
		}
		b.Write([]byte{dim, dim, 0, 0})
		_ = binary.Write(&b, le, [2]uint16{1, 32})
		_ = binary.Write(&b, le, [2]uint32{uint32(len(data[i])), uint32(off)})
		off += len(data[i])
	}
	for _, d := range data {
		b.Write(d)
	}
	return b.Bytes()
}

// dib is an icon image as a BITMAPINFOHEADER, bottom-up BGRA rows and an
// all-zero AND mask.
func dib(img *image.NRGBA) []byte {
	le := binary.LittleEndian
	n := img.Bounds().Dx()
	var b bytes.Buffer
	_ = binary.Write(&b, le, struct {
		Size                   uint32
		Width, Height          int32
		Planes, BitCount       uint16
		Compression, SizeImage uint32
		XPels, YPels           int32
		ClrUsed, ClrImportant  uint32
	}{Size: 40, Width: int32(n), Height: int32(2 * n), Planes: 1, BitCount: 32, SizeImage: uint32(n * n * 4)})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	maskRow := (n + 31) / 32 * 4
	b.Write(make([]byte, maskRow*n))
	return b.Bytes()
}
