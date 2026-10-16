package gui

// The project's public links (the About tab, #156) and the SmartThings
// driver channel invite (the SmartThings tab, #155), with the QR code that
// lets a phone open the invite.

import (
	"image"
	"image/color"
	"net/url"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
	"rsc.io/qr"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// driverInviteURL is the invite to the Protomothis driver channel (no
// expiry). README.md carries the same link; links_test.go keeps them equal.
const driverInviteURL = "https://bestow-regional.api.smartthings.com/invite/Kr2zNWYgpp2A"

const (
	repoURL      = "https://github.com/Protomothis/smartthings-pc-control"
	wikiURL      = repoURL + "/wiki"
	changelogURL = repoURL + "/blob/main/CHANGELOG.md"
	issuesURL    = repoURL + "/issues"
	licenseURL   = repoURL + "/blob/main/LICENSE"
	goModURL     = repoURL + "/blob/main/go.mod"
	sponsorURL   = "https://github.com/sponsors/Protomothis"
)

// mustURL parses one of the constants above.
func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic("gui: bad link " + raw + ": " + err.Error())
	}
	return u
}

// openLink opens an http(s) link in the default browser, through cmd's
// start like the rest of the app (systool: cmd.exe by absolute path). The
// links are constants without & or spaces, which cmd would split on. A
// variable so the tests can watch it.
var openLink = func(link string) error {
	return systool.Command(systool.Cmd, "/c", "start", "", link).Start()
}

// qrQuiet is the white border, in modules, that scanners need around a QR
// code.
const qrQuiet = 4

// qrImage draws text as a QR code, one pixel per module: black on white
// with the quiet zone, white even in the dark theme so phones can read it.
func qrImage(text string) (*image.Gray, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := c.Size + 2*qrQuiet
	img := image.NewGray(image.Rect(0, 0, n, n))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y := range c.Size {
		for x := range c.Size {
			if c.Black(x, y) {
				img.SetGray(x+qrQuiet, y+qrQuiet, color.Gray{})
			}
		}
	}
	return img, nil
}

// newQRCode shows text as a QR code of about 180 px, each module a whole
// number of pixels and scaled without smoothing so the edges stay sharp.
// Should the text not encode, the text itself stands in.
func newQRCode(text string) fyne.CanvasObject {
	img, err := qrImage(text)
	if err != nil {
		return widget.NewLabel(text)
	}
	n := img.Bounds().Dx()
	scale := max(1, 180/n)
	c := canvas.NewImageFromImage(img)
	c.ScaleMode = canvas.ImageScalePixels
	c.FillMode = canvas.ImageFillContain
	c.SetMinSize(fyne.NewSquareSize(float32(n * scale)))
	return c
}
