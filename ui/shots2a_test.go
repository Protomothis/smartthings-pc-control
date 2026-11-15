package ui

import (
	"fmt"
	"os"
	"testing"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsShared draws the phase-2a pieces: the home 연결 card, the
// connection page template on SmartThings, 텔레그램 and 공유 (and their
// off and warning states), the 서비스 설정 form with and without its save
// bar, a save notice, a toast with an action, a question sheet and the
// shared widgets — light and dark, at 480 and 1200dp.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsShared ./ui/
func TestShotsShared(t *testing.T) {
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
					continue // English only checks that the texts fit
				}
				name := func(what string) string { return fmt.Sprintf("%s-%d-%s", what, w, sfx) }
				winHt := winH
				if w > 480 {
					winHt = 800
				}
				u := sampleUI(lang, dark)
				shotW(t, dir, name("home"), w, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })

				for _, pg := range []struct {
					id   pageID
					file string
				}{{pageSmartThings, "smartthings"}, {pageTelegram, "telegram"}, {pageSharing, "sharing"}} {
					u.open(pg.id)
					shotW(t, dir, name(pg.file), w, winHt, u.layout)
					u.nav.stack = nil
				}

				// the service settings page: clean, then edited (save bar)
				u.open(pageService)
				shotW(t, dir, name("service"), w, 1000, u.layout)
				p := u.pages[pageService].(*servicePage)
				p.port.SetText("5011")
				p.read()
				shotW(t, dir, name("service-dirty"), w, winHt, u.layout)
				// scrolled to the end: the save bar sits under the last card
				u.pageList.Position = layout.Position{First: 0, Offset: 100000}
				shotW(t, dir, name("service-dirty-end"), w, winHt, u.layout)
				u.pageList.Position = layout.Position{}
				u.revertForm(p)
				u.nav.stack = nil

				if w != 480 {
					continue
				}
				// off and warning states of the connection pages
				u.update(func(s *snapshot) {
					s.hub = gui.STHub{}
					s.cfg.Telegram = gui.TelegramConfig{Enabled: true}
					s.cfg.SmartThings.ExposeSession, s.cfg.Media.NowPlaying = false, false
				})
				shotW(t, dir, name("home-off"), w, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
				for _, id := range []pageID{pageSmartThings, pageTelegram, pageSharing} {
					u.open(id)
					shotW(t, dir, name(string(id)+"-off"), w, winHt, u.layout)
					u.nav.stack = nil
				}

				// a save notice, a toast with an action, a question
				u = sampleUI(lang, dark)
				u.open(pageService)
				p = u.pages[pageService].(*servicePage)
				p.port.SetText("abc")
				p.read()
				u.saveForm(p)
				u.toast.showAction(u.t("settings.saved.restart"), u.t("settings.restart.now"), func() {}, time.Now())
				shotW(t, dir, name("service-notice"), w, winHt, u.layout)
				u.toast.dismiss()
				u.ask(askSpec{title: u.t("slotchange.title"), body: "프리셋 3: 게임 모드 → (비어 있음)\n\n" + u.t("slotchange.body"), ok: u.t("settings.save")})
				shotW(t, dir, name("ask"), w, winHt, u.layout)
				u.sheetW.open = nil
				u.nav.stack = nil

				// the shared widgets
				g := &gallery{}
				shotW(t, dir, name("widgets"), w, 0, func(gtx C) D { return g.layout(gtx, u) })
			}
		}
	}
	usePalette(false)
}

// gallery shows the shared widgets a screen builds from.
type gallery struct {
	list   listEditor
	copy   copyButton
	area   widget.Editor
	toggle widget.Bool
	port   widget.Editor
}

func (g *gallery) layout(gtx C, u *UI) D {
	th := u.th
	if g.area.Text() == "" {
		g.area.SetText("첫 줄\n두 번째 줄")
		g.port.SetText("5001")
	}
	return layout.UniformInset(gapM).Layout(gtx, func(gtx C) D {
		return sections(gtx,
			section(th, "banner", func(gtx C) D {
				return column(gtx,
					layout.Rigid(func(gtx C) D { return banner(gtx, th, noticeInfo, "정보: 다음 단계에서 편집해요") }),
					vspace(8),
					layout.Rigid(func(gtx C) D { return banner(gtx, th, noticeOK, "저장했어요") }),
					vspace(8),
					layout.Rigid(func(gtx C) D { return banner(gtx, th, noticeWarn, "프리셋 2의 경로가 없어요") }),
					vspace(8),
					layout.Rigid(func(gtx C) D { return banner(gtx, th, noticeError, "포트가 숫자가 아니에요: abc") }),
				)
			}),
			section(th, "listEditor · copyButton", func(gtx C) D {
				hubs := []string{"192.168.1.20", "192.168.1.21"}
				return column(gtx,
					layout.Rigid(func(gtx C) D {
						return g.list.Layout(gtx, th, len(hubs), "", "허브 추가", func(gtx C, i int) D {
							return line(th, textL, hubs[i], colText, font.Normal).Layout(gtx)
						})
					}),
					vspace(gapS),
					layout.Rigid(func(gtx C) D {
						return settingRow(gtx, th, "Machine ID", "4f2a…9c1e", func(gtx C) D {
							return g.copy.Layout(gtx, th, "4f2a9c1e", u.t("ui.copy"), u.t("ui.copied"))
						})
					}),
				)
			}),
			section(th, "rows", func(gtx C) D {
				return rows(gtx,
					func(gtx C) D { return fieldRow(gtx, th, &g.port, "포트", "WebUI는 +1", "5001", 96) },
					func(gtx C) D {
						return toggleRowIcon(gtx, th, &g.toggle, "bell", toneAccent, "PC 알림", "루틴과 /say")
					},
					func(gtx C) D { return settingRow(gtx, th, "상태", "", statusPill(th, "확인 필요", toneWarn)) },
				)
			}),
			section(th, "textArea", textArea(th, &g.area, "여러 줄", 3)),
			func(gtx C) D {
				return heroCard(gtx, th, hero{icon: "telegram", tone: toneWarn, headline: "다른 PC와 봇이 겹쳐요", sub: "PC마다 봇을 나누세요"}, nil)
			},
		)
	})
}
