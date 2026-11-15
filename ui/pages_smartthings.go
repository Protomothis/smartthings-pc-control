package ui

// SmartThings: the hub, discovery and WoL, and the allowed hubs. Phase 2a
// shows what the service reports, read-only, in the connection page
// template; the editor (allowed hubs, WoL adapter, machine id copy) is
// #165 phase 2b and replaces this file.

import (
	"fmt"
	"time"

	"gioui.org/font"
	"gioui.org/layout"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

type stPage struct{}

func newSmartThingsPage() screen { return &stPage{} }

func (p *stPage) title(u *UI) string { return u.t("tab.smartthings") }

func (p *stPage) shown(u *UI, _ snapshot) { go u.loadHub() }

// stStatus is the hub's state for the hero card and the home 연결 card's
// row: the hero, and the pill's text.
func (u *UI) stStatus(s snapshot) (hero, string) {
	h := s.hub
	switch {
	case !s.hubSet:
		return hero{icon: "hub", tone: toneOff, headline: u.t("ui.smartthings.hero.loading")}, u.t("ui.conn.waiting")
	case h.Connected:
		sub := h.IP
		if h.LastSeen != "" {
			sub += " · " + fmt.Sprintf(u.t("ui.smartthings.seen"), gui.RelativeTime(u.language(), h.LastSeen, time.Now()))
		}
		return hero{icon: "hub", tone: toneOK, headline: u.t("ui.smartthings.hero.on"), sub: sub}, u.t("ui.conn.on")
	}
	return hero{icon: "hub", tone: toneOff, headline: u.t("ui.smartthings.hero.off"), sub: u.t("ui.smartthings.off.sub")}, u.t("ui.conn.off")
}

func (p *stPage) body(gtx C, u *UI, s snapshot) D {
	h := s.hub
	th := u.th
	hr, _ := u.stStatus(s)
	ip, driver, seen := "—", "—", "—"
	if h.Connected {
		ip, driver, seen = h.IP, "v"+h.DriverVersion, gui.RelativeTime(u.language(), h.LastSeen, time.Now())
	}
	ssdp := u.t("ui.smartthings.search.off")
	if h.SSDP.Running {
		ssdp = u.t("ui.smartthings.search.on")
	}
	wol := u.t("st.wol.none")
	if sel := h.WoL.Selected; sel != nil {
		wol = sel.Name
	}
	return sections(gtx,
		func(gtx C) D { return heroCard(gtx, th, hr, nil) },
		section(th, u.t("ui.smartthings.hub"), func(gtx C) D {
			return infoRows(gtx,
				infoRow(th, "IP", ip),
				infoRow(th, u.t("home.st.driver"), driver),
				infoRow(th, u.t("home.st.seen"), seen),
			)
		}),
		section(th, u.t("ui.smartthings.discovery"), func(gtx C) D {
			return infoRows(gtx,
				infoRow(th, u.t("home.st.search"), ssdp),
				infoRow(th, u.t("st.wol"), wol),
			)
		}),
		section(th, u.t("st.hubs"), func(gtx C) D {
			hubs := s.cfg.SmartThings.AllowedHubs
			if len(hubs) == 0 {
				return label(th, textM, u.t("st.hubs.hint"), colSub, font.Normal).Layout(gtx)
			}
			ws := make([]layout.Widget, len(hubs))
			for i, ip := range hubs {
				ws[i] = line(th, textL, ip, colText, font.Normal).Layout
			}
			return rows(gtx, ws...)
		}),
		pendingNote(u),
	)
}
