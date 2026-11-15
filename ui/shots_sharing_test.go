package ui

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"gioui.org/layout"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsSharing draws the 공유 page: configured, everything off with an
// empty list, a slot pill open, the picker loading and listing, the list
// locked, a check failure and the edited state with the save bar — light
// and dark, ko at 480 and 1200dp, en at 480.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsSharing ./ui/
func TestShotsSharing(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	running := []string{"chrome.exe", "Code.exe", "Discord.exe", "explorer.exe", "obs64.exe", "Spotify.exe", "steam.exe", "steamwebhelper.exe"}
	for _, lang := range []gui.Lang{gui.LangKo, gui.LangEn} {
		for _, dark := range []bool{false, true} {
			sfx := string(lang)
			if dark {
				sfx += "-dark"
			}
			for _, w := range []int{480, 1200} {
				if lang == gui.LangEn && w == 1200 {
					continue
				}
				name := func(what string) string { return fmt.Sprintf("sharing-%s-%d-%s", what, w, sfx) }
				winHt := winH
				if w > 480 {
					winHt = 800
				}
				u, p := shotSharingUI(lang, dark)
				full := func(what string) { shotW(t, dir, name(what), w, 0, func(gtx C) D { return pageFull(gtx, u, p) }) }

				shotW(t, dir, name("window"), w, winHt, u.layout)
				full("configured")

				// a slot pill open, then the picker listing
				p.slotOpen = 1
				full("slot")
				p.slotOpen = -1
				p.picker.open, p.picker.loading = true, true
				full("picker-loading")
				p.picker.loading, p.picker.names = false, running
				full("picker")
				p.picker.search.SetText("steam")
				full("picker-search")
				p.picker.reset()

				// edited: a typed label and the save bar
				p.labelEd[0].SetText("스팀 게임")
				p.read()
				shotW(t, dir, name("dirty"), w, winHt, u.layout)
				// a check failure
				p.procEd[1].SetText(`C:\Apps\discord`)
				p.read()
				u.saveForm(p)
				full("error")
				u.revertForm(p)

				// locked: no local session
				u.trust.Store(int32(trustRefused))
				full("locked")
				u.trust.Store(int32(trustUnknown))

				// everything off, nothing listed
				u.setConfig(gui.Config{Port: 5001})
				u.syncForms(u.current())
				full("off")
				u.nav.stack = nil
			}
		}
	}
	usePalette(false)
}

// shotSharingUI is sampleUI with a watch list, the 공유 page open.
func shotSharingUI(lang gui.Lang, dark bool) (*UI, *sharePage) {
	u := sampleUI(lang, dark)
	u.api = shotProcAPI{}
	cfg := u.current().cfg
	cfg.SmartThings.ExposeSessionUser = false
	cfg.Activity = gui.ActivityConfig{Enabled: true, Watch: []gui.ActivityWatch{
		{Slot: 1, Process: "steam.exe", Label: "Steam"},
		{Slot: 2, Process: "Discord.exe", Label: "Discord"},
		{Slot: 4, Process: "obs64.exe", Label: "OBS"},
	}}
	u.setConfig(cfg)
	u.syncForms(u.current())
	u.open(pageSharing)
	return u, u.pages[pageSharing].(*sharePage)
}

// shotProcAPI is fakeAPI that lists nothing (the shots fill the picker).
type shotProcAPI struct{ fakeAPI }

func (shotProcAPI) RunningProcesses() ([]string, error) { return nil, errors.New("fake") }

// pageFull draws a page as the window's scrolling column does, but as
// tall as its content.
func pageFull(gtx C, u *UI, p screen) D {
	return centered(gtx, pageColumnW, func(gtx C) D {
		return layout.Inset{Left: gapM, Right: gapM, Top: 4, Bottom: gapL}.Layout(gtx, func(gtx C) D {
			return column(gtx,
				layout.Rigid(func(gtx C) D { return u.layoutNotice(gtx, p) }),
				layout.Rigid(func(gtx C) D { return p.body(gtx, u, u.current()) }),
			)
		})
	})
}
