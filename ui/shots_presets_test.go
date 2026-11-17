package ui

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsPresets draws the preset editor: the list, an open row with a
// test result and a service warning, a new empty row, an app link, a save
// error, the empty page, the locked page and the arrival from the home
// card — light and dark, at 480 and
// 1200dp (English at 480).
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsPresets ./ui/
func TestShotsPresets(t *testing.T) {
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
				name := func(what string) string { return fmt.Sprintf("presets-%s-%d-%s", what, w, sfx) }
				u := sampleUI(lang, dark)
				u.open(pagePresets)
				p := u.pages[pagePresets].(*presetsPage)
				shotW(t, dir, name("list"), w, 720, u.layout)

				// a tap on preset 3 in the home card, in a short window:
				// scrolled to it, opened, still glowing
				u.nav.stack = nil
				p.OpenSlot(u, 3)
				shotW(t, dir, name("arrive"), w, 420, u.layout)
				p.rows[2].glowUntil = time.Time{}
				u.nav.stack = nil
				u.open(pagePresets)
				p.rows[2].open = false

				// an open script row: a test result and a save warning
				p.openRow(p.rows[0])
				p.rows[0].status, p.rows[0].statusTone = u.t("presets.started"), toneOK
				p.warnings = []gui.PresetWarning{{Slot: 1, Code: "writable_by_others", Path: `C:\Users\me\Documents\game-mode.ps1`}}
				shotW(t, dir, name("open"), w, 1250, u.layout)
				p.warnings = nil
				p.rows[0].status = ""

				// an app link with no app behind it
				p.openRow(p.rows[1])
				p.rows[1].path.SetText("nosuchapp://run/1")
				shotW(t, dir, name("link"), w, 1000, u.layout)
				u.revertForm(p)

				// a new row (the save bar shows), then a refused save
				p.addRow(u)
				shotW(t, dir, name("new"), w, 1150, u.layout)
				u.saveForm(p)
				shotW(t, dir, name("error"), w, 1200, u.layout)
				u.revertForm(p)
				u.nav.stack = nil

				if w != 480 {
					continue
				}
				// no presets yet
				u.update(func(s *snapshot) { s.cfg.Presets = nil })
				u.snap.cfgGen++
				u.syncForms(u.current())
				u.open(pagePresets)
				shotW(t, dir, name("empty"), w, 600, u.layout)
				u.nav.stack = nil

				// locked: the service refused the local session
				u = sampleUI(lang, dark)
				u.trust.Store(int32(trustRefused))
				u.open(pagePresets)
				p = u.pages[pagePresets].(*presetsPage)
				p.openRow(p.rows[2])
				shotW(t, dir, name("locked"), w, 1000, u.layout)
				u.nav.stack = nil
			}
		}
	}
	usePalette(false)
}
