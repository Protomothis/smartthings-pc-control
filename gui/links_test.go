package gui

import (
	"os"
	"regexp"
	"testing"
)

// inviteLink finds SmartThings channel invites in the docs.
var inviteLink = regexp.MustCompile(`https://bestow-regional\.api\.smartthings\.com/invite/[A-Za-z0-9]+`)

// The app's invite is the one the docs give: a new channel or link has to
// change both (the Wiki too, which lives outside this repository).
func TestDriverInviteMatchesDocs(t *testing.T) {
	for _, path := range []string{"../README.md", "../edge/README.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		found := inviteLink.FindAllString(string(data), -1)
		if len(found) == 0 {
			t.Errorf("%s: no channel invite link", path)
		}
		for _, link := range found {
			if link != driverInviteURL {
				t.Errorf("%s: invite %s, the app has %s", path, link, driverInviteURL)
			}
		}
	}
}

// The QR code is a whole code with its quiet zone: finder patterns in three
// corners, the fourth corner without one, and a white border all round.
func TestQRImage(t *testing.T) {
	img, err := qrImage(driverInviteURL)
	if err != nil {
		t.Fatal(err)
	}
	n := img.Bounds().Dx()
	size := n - 2*qrQuiet
	if img.Bounds().Dy() != n || size < 21 || (size-17)%4 != 0 {
		t.Fatalf("image %v: %d modules is not a QR version size", img.Bounds(), size)
	}
	dark := func(x, y int) bool { return img.GrayAt(x+qrQuiet, y+qrQuiet).Y == 0 }
	for i := -qrQuiet; i < size+qrQuiet; i++ {
		for _, p := range [][2]int{{i, -1}, {i, size}, {-1, i}, {size, i}} {
			if dark(p[0], p[1]) {
				t.Fatalf("quiet zone dark at %v", p)
			}
		}
	}
	// A finder pattern: dark 7×7 ring, light ring inside, dark 3×3 centre.
	finder := func(x0, y0 int) bool {
		for y := range 7 {
			for x := range 7 {
				ring := x == 0 || y == 0 || x == 6 || y == 6
				centre := x >= 2 && x <= 4 && y >= 2 && y <= 4
				if dark(x0+x, y0+y) != (ring || centre) {
					return false
				}
			}
		}
		return true
	}
	if !finder(0, 0) || !finder(size-7, 0) || !finder(0, size-7) {
		t.Error("a finder pattern is missing")
	}
	if finder(size-7, size-7) {
		t.Error("finder pattern in the bottom-right corner")
	}
}

// With no hub the SmartThings tab shows the install steps and QR code;
// once a hub is connected they make way for the [QR code] button.
func TestSTInviteFollowsTheHub(t *testing.T) {
	u, _ := newFakeServiceUI(t) // the fake's hub status is "none"
	st := u.st
	if !st.inviteHelp.Visible() || st.qrBtn.Visible() {
		t.Fatalf("no hub: steps shown %v, QR button shown %v", st.inviteHelp.Visible(), st.qrBtn.Visible())
	}
	u.showSTInviteHelp(stHubMissing(STHub{Connected: true, IP: "192.168.1.10"}))
	if st.inviteHelp.Visible() || !st.qrBtn.Visible() {
		t.Errorf("hub connected: steps shown %v, QR button shown %v", st.inviteHelp.Visible(), st.qrBtn.Visible())
	}
	if !stHubMissing(STHub{Connected: true}) {
		t.Error("a hub without an address counts as connected")
	}

	u.copyDriverInvite()
	if got := u.app.Clipboard().Content(); got != driverInviteURL {
		t.Errorf("clipboard %q, want the invite", got)
	}
}
