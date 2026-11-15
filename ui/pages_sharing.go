package ui

// 공유: what the PC tells SmartThings and Telegram about itself. Phase 2a
// shows the switches read-only in the connection page template; the
// editor (session, now playing, running apps and the watch list with its
// process picker) is #165 phase 2b and replaces this file.

import (
	"fmt"

	"gioui.org/layout"
)

type sharePage struct{}

func newSharingPage() screen { return &sharePage{} }

func (p *sharePage) title(u *UI) string { return u.t("tab.share") }

// shareStatus is the sharing state for the hero card and the home row.
func (u *UI) shareStatus(s snapshot) (hero, string) {
	n := s.shareCount()
	if n == 0 {
		return hero{icon: "share", tone: toneOff, headline: u.t("home.share.none"), sub: u.t("ui.sharing.hero.sub")}, u.t("ui.conn.off")
	}
	return hero{icon: "share", tone: toneAccent, headline: fmt.Sprintf(u.t("home.share.some"), n), sub: u.t("ui.sharing.hero.sub")},
		fmt.Sprintf(u.t("ui.sharing.count"), n)
}

// shareRow is one kind of information with its on/off pill.
func (u *UI) shareRow(title, sub string, on bool) layout.Widget {
	return func(gtx C) D {
		t, txt := toneOff, u.t("home.off")
		if on {
			t, txt = toneOK, u.t("home.on")
		}
		return settingRow(gtx, u.th, title, sub, statusPill(u.th, txt, t))
	}
}

func (p *sharePage) body(gtx C, u *UI, s snapshot) D {
	th := u.th
	hr, _ := u.shareStatus(s)
	return sections(gtx,
		func(gtx C) D { return heroCard(gtx, th, hr, nil) },
		section(th, u.t("ui.sharing.sent"), func(gtx C) D {
			return rows(gtx,
				u.shareRow(u.t("share.session"), u.t("ui.sharing.session.sub"), s.cfg.SmartThings.ExposeSession),
				u.shareRow(u.t("share.media"), u.t("ui.sharing.media.sub"), s.cfg.Media.NowPlaying),
				u.shareRow(u.t("share.activity"), u.t("ui.sharing.activity.sub"), s.cfg.Activity.Enabled),
			)
		}),
		section(th, u.t("ui.sharing.browser"), u.shareRow(u.t("ui.service.webui"), u.t("ui.service.webui.hint"), s.cfg.WebUIRemote)),
		pendingNote(u),
	)
}
