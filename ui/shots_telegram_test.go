package ui

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"gioui.org/layout"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsTelegram draws the 텔레그램 page: configured, unconfigured,
// edited (save bar), and with its warnings, a failed test, a token about
// to be removed and the found chats open — light and dark, ko (and en at
// 480), at 480 and 1200dp.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsTelegram ./ui/
func TestShotsTelegram(t *testing.T) {
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
				name := func(what string) string { return fmt.Sprintf("telegram-%s-%d-%s", what, w, sfx) }
				winHt := winH
				if w > 480 {
					winHt = 800
				}
				u := sampleUI(lang, dark)
				u.update(func(s *snapshot) {
					s.cfg.Telegram.BotToken = "****6789"
					s.cfg.Telegram.AllowedChatIDs = []string{"123456789", "-1001234567890"}
					s.cfg.Telegram.QuietHours = gui.QuietHours{Enabled: true, Start: "23:00", End: "07:00", Digest: true}
					s.cfgGen++
				})
				u.syncForms(u.current())
				u.open(pageTelegram)
				p := u.pages[pageTelegram].(*tgPage)
				p.bot = tgResult{tone: toneOK, text: fmt.Sprintf(u.t("notify.bot.connected"), "my_pc_bot")}
				page := func(gtx C) D {
					return layout.UniformInset(gapM).Layout(gtx, func(gtx C) D { return p.body(gtx, u, u.current()) })
				}
				shotW(t, dir, name("full"), w, 0, page)
				shotW(t, dir, name("window"), w, winHt, u.layout)

				// edited: a new token, the save bar
				p.token.ed.SetText("123456:ABCDEFGHIJ")
				p.read()
				shotW(t, dir, name("dirty"), w, winHt, u.layout)
				u.revertForm(p)

				// warnings, a failed test, a removal pending, found chats
				u.update(func(s *snapshot) { s.tg.Conflict = true })
				p.clearToken = true
				p.test = tgResult{tone: toneBad, text: "Bad Request: chat not found"}
				p.showChats([]gui.TelegramChat{
					{ChatID: "123456789", Title: "", Username: "protomothis", Type: "private"},
					{ChatID: "-1001234567890", Title: "우리집 PC 알림방", Type: "supergroup"},
				}, nil)
				p.commands.open = true
				shotW(t, dir, name("states"), w, 0, page)
				p.showChats(nil, errors.New("Unauthorized"))
				p.clearToken = false
				p.test = tgResult{tone: toneOK, text: u.t("ui.telegram.test.sent")}
				shotW(t, dir, name("states2"), w, 0, page)
				u.nav.stack = nil

				// unconfigured: nothing stored, everything off
				u = sampleUI(lang, dark)
				u.update(func(s *snapshot) {
					s.cfg.Telegram = gui.TelegramConfig{}
					s.cfg.Notify = nil
					s.tg = gui.TelegramState{}
					s.cfgGen++
				})
				u.syncForms(u.current())
				u.open(pageTelegram)
				p = u.pages[pageTelegram].(*tgPage)
				page = func(gtx C) D {
					return layout.UniformInset(gapM).Layout(gtx, func(gtx C) D { return p.body(gtx, u, u.current()) })
				}
				shotW(t, dir, name("empty"), w, 0, page)
				u.nav.stack = nil
			}
		}
	}
	usePalette(false)
}
