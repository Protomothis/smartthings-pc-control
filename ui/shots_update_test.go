package ui

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// TestShotsUpdate draws the 업데이트 page in its states: a new version to
// install, up to date, skipped, unsigned (page only), a dev build,
// checking, a failed check, an update in progress, its confirmation and a
// failed update — light and dark, at 480 and 1200dp. Nothing is checked or
// downloaded: the page's state is set by hand.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsUpdate ./ui/
func TestShotsUpdate(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
				name := func(what string) string { return fmt.Sprintf("update-%s-%d-%s", what, w, sfx) }
				winHt := winH
				if w > 480 {
					winHt = 800
				}
				// state sets the page up on a fresh UI and shows it.
				state := func(version string, set func(u *UI, p *updatePage)) *UI {
					u := sampleUI(lang, dark)
					u.version = version
					p := u.pages[pageUpdate].(*updatePage)
					p.upd = &fakeUpdater{err: errors.New("no network in shots")}
					set(u, p)
					u.open(pageUpdate)
					return u
				}
				planned := func(p *updatePage, version string, m *release.Manifest, err error) {
					p.latest, p.checkedAt = sampleCheck("v1.2.5"), time.Now()
					p.plan, p.planTag = gui.PlanUpdate(p.latest, version, m, err), "v1.2.5"
				}

				u := state("v1.2.4", func(_ *UI, p *updatePage) { planned(p, "v1.2.4", goodManifest("v1.2.5"), nil) })
				shotW(t, dir, name("new"), w, 1300, u.layout)

				u = state("v1.2.5", func(_ *UI, p *updatePage) { p.latest, p.checkedAt = sampleCheck("v1.2.5"), time.Now() })
				shotW(t, dir, name("uptodate"), w, winHt, u.layout)

				u = state("v1.2.4", func(_ *UI, p *updatePage) { p.checking = true })
				shotW(t, dir, name("checking"), w, winHt, u.layout)

				if w != 480 {
					continue
				}
				u = state("v1.2.4", func(u *UI, p *updatePage) {
					planned(p, "v1.2.4", goodManifest("v1.2.5"), nil)
					u.prefs.Set(gui.PrefUpdateSkipped, "v1.2.5")
				})
				shotW(t, dir, name("skipped"), w, winHt, u.layout)

				u = state("v1.2.4", func(_ *UI, p *updatePage) { planned(p, "v1.2.4", nil, release.ErrNoManifest) })
				shotW(t, dir, name("unsigned"), w, winHt, u.layout)

				u = state("dev", func(_ *UI, p *updatePage) { planned(p, "dev", nil, nil) })
				shotW(t, dir, name("dev"), w, winHt, u.layout)

				u = state("v1.2.4", func(_ *UI, p *updatePage) {
					p.checkErr = errors.New(`Get "https://api.github.com/repos/…/releases": dial tcp: lookup api.github.com: no such host`)
					p.upd = &fakeUpdater{err: p.checkErr} // opening checks again: the same answer
				})
				settleShot(u)
				shotW(t, dir, name("error"), w, winHt, u.layout)

				u = state("v1.2.4", func(_ *UI, p *updatePage) {
					planned(p, "v1.2.4", goodManifest("v1.2.5"), nil)
					p.running, p.prog = true, gui.UpdateProgress{Stage: gui.StageDownloading, Done: 9 << 20, Total: 21 << 20}
				})
				shotW(t, dir, name("progress"), w, winHt, u.layout)

				u = state("v1.2.4", func(_ *UI, p *updatePage) {
					planned(p, "v1.2.4", goodManifest("v1.2.5"), nil)
					p.running, p.prog = true, gui.UpdateProgress{Stage: gui.StageApplying}
				})
				shotW(t, dir, name("applying"), w, winHt, u.layout)

				u = state("v1.2.4", func(_ *UI, p *updatePage) { planned(p, "v1.2.4", goodManifest("v1.2.5"), nil) })
				p := u.pages[pageUpdate].(*updatePage)
				p.askUpdate(u)
				shotW(t, dir, name("ask"), w, winHt, u.layout)
				u.sheetW.open = nil
				u.setNotice(p, noticeError, gui.UpdateFailedText(u.language(), errors.New("The operation was canceled by the user.")))
				shotW(t, dir, name("failed"), w, winHt, u.layout)
			}
		}
	}
	usePalette(false)
}

// settleShot lets the update page's check answer before the shot.
func settleShot(u *UI) {
	p := u.pages[pageUpdate].(*updatePage)
	for range 200 {
		u.runTasks()
		if !p.checking {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
