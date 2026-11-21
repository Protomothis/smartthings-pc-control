package ui

import (
	"fmt"
	"os"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsReport draws 문제 보내기: the empty page, a description with the
// preview open, collecting, sent (the drag-in banner) and a failed save,
// plus the home settings card with its new row — light and dark, ko at
// 480 and 1200dp, en at 480dp.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsReport ./ui/
func TestShotsReport(t *testing.T) {
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
				name := func(what string) string { return fmt.Sprintf("report-%s-%d-%s", what, w, sfx) }
				f := &fakeReport{dir: t.TempDir()}
				u := sampleUI(lang, dark)
				p := u.pages[pageReport].(*reportPage)
				p.ops = f.ops()
				u.open(pageReport)
				waitFor(t, u, func() bool { return p.loaded })
				shotW(t, dir, name("empty"), w, 1050, u.layout)

				desc := "허브가 PC를 못 찾아요. 어제 업데이트한 뒤부터 SmartThings 앱에서 끄기를 눌러도 반응이 없어요."
				if lang == gui.LangEn {
					desc = "The hub cannot find my PC. Since yesterday's update, Turn off in the SmartThings app does nothing."
				}
				p.desc.SetText(desc)
				p.inc[incDiag].Value = false
				p.preview.open = true
				shotW(t, dir, name("preview"), w, 1900, u.layout)
				p.preview.open = false

				p.busy, p.busySend = true, true
				shotW(t, dir, name("busy"), w, 1050, u.layout)
				p.busy = false

				p.result = notice{noticeOK, fmt.Sprintf(u.t("ui.report.done.send"), `C:\Users\<user>\Desktop\stpc-report-20261121-1530.zip`)}
				shotW(t, dir, name("sent"), w, 1150, u.layout)
				p.result = notice{noticeError, fmt.Sprintf(u.t("ui.report.fail"), "access is denied")}
				shotW(t, dir, name("error"), w, 1150, u.layout)

				if w == 480 {
					u.nav.stack = nil
					shotW(t, dir, name("home"), w, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
					u.open(pageLogs)
					shotW(t, dir, name("logs"), w, 720, u.layout)
				}
			}
		}
	}
	usePalette(false)
}
