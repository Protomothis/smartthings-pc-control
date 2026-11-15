package ui

// 텔레그램: the bot, the chats, remote control and what it reports. Phase
// 2a shows the state read-only in the connection page template; the editor
// (token, chat IDs, control allow list, the event catalogue, quiet hours,
// test and find-chat) is #165 phase 2b and replaces this file.

type tgPage struct{}

func newTelegramPage() screen { return &tgPage{} }

func (p *tgPage) title(u *UI) string { return u.t("tab.telegram") }

func (p *tgPage) shown(u *UI, _ snapshot) { go u.loadHub() }

// tgStatus is Telegram's state for the hero card and the home row.
func (u *UI) tgStatus(s snapshot) (hero, string) {
	tg := s.cfg.Telegram
	switch {
	case !tg.Enabled && !tg.ControlEnabled:
		return hero{icon: "telegram", tone: toneOff, headline: u.t("ui.telegram.hero.off"), sub: u.t("ui.telegram.off.sub")}, u.t("ui.conn.off")
	case !tg.BotTokenSet:
		return hero{icon: "telegram", tone: toneWarn, headline: u.t("ui.telegram.hero.notoken"), sub: u.tgLine(s)}, u.t("ui.conn.check")
	case s.tg.Conflict:
		return hero{icon: "telegram", tone: toneWarn, headline: u.t("ui.telegram.hero.conflict"), sub: u.t("notify.control.conflict")}, u.t("ui.conn.check")
	}
	return hero{icon: "telegram", tone: toneOK, headline: u.t("ui.telegram.hero.on"), sub: u.tgLine(s)}, u.t("ui.conn.on")
}

func (p *tgPage) body(gtx C, u *UI, s snapshot) D {
	tg := s.cfg.Telegram
	th := u.th
	hr, _ := u.tgStatus(s)
	chat := tg.ChatID
	if chat == "" {
		chat = "—"
	}
	token := u.t("ui.telegram.token.unset")
	if tg.BotTokenSet {
		token = u.t("ui.telegram.token.set")
	}
	return sections(gtx,
		func(gtx C) D { return heroCard(gtx, th, hr, nil) },
		section(th, u.t("ui.telegram.bot"), func(gtx C) D {
			return infoRows(gtx,
				infoRow(th, u.t("notify.token"), token),
				infoRow(th, u.t("notify.chatid"), chat),
			)
		}),
		section(th, u.t("ui.telegram.features"), func(gtx C) D {
			return infoRows(gtx,
				infoRow(th, u.t("ui.telegram.notify"), u.onOff(tg.Enabled)),
				infoRow(th, u.t("ui.telegram.control"), u.onOff(tg.ControlEnabled)),
				infoRow(th, u.t("home.tg.polling"), u.onOff(s.tg.Polling)),
			)
		}),
		pendingNote(u),
	)
}
