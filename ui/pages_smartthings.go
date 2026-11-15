package ui

// SmartThings: how the Edge driver reaches this PC. The hub at the top,
// then this PC's id and command port, the discovery diagnostics, the WoL
// adapter (a pick list), the allowed hubs (a list editor) and the network
// adapters. The form owns smartthings.allowed_hubs and .wol_mac only
// (gui.STForm); the session switches are the 공유 page's, linked from here.
// SSDP has no switch: adding the device has no other path, so the
// responder always runs and the page reports on it.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

type stPage struct {
	form gui.STForm

	refresh widget.Clickable
	idCopy  copyButton
	wol     choiceList
	hubs    listEditor
	share   widget.Clickable
	secret  widget.Clickable
	adapter expander

	// net is the last /api/wol-status answer (the adapter list).
	net stNet
}

// stNet is the network adapter list as the page last read it.
type stNet struct {
	status gui.WoLStatus
	err    error
	set    bool // an answer (or an error) arrived
}

func newSmartThingsPage() screen { return &stPage{} }

func (p *stPage) title(u *UI) string { return u.t("tab.smartthings") }

func (p *stPage) shown(u *UI, _ snapshot) { p.reload(u) }

// reload re-reads the hub (which carries the id, discovery and WoL
// picture) and the adapter list. Window goroutine.
func (p *stPage) reload(u *UI) {
	go u.loadHub()
	go p.loadNetwork(u)
}

// loadNetwork reads the adapter list. Off the window goroutine.
func (p *stPage) loadNetwork(u *UI) {
	s, err := u.api.GetWoLStatus()
	u.onWindow(func() { p.net = stNet{status: s, err: err, set: true} })
}

// --- form ------------------------------------------------------------------

func (p *stPage) fill(_ *UI, cfg gui.Config) { p.form = gui.NewSTForm(cfg) }

func (p *stPage) dirty(_ *UI, base gui.Config) bool { return p.form.Dirty(base) }

func (p *stPage) apply(_ *UI, cfg *gui.Config) error {
	p.form.ApplyTo(cfg)
	return nil
}

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

// canAddHub reports whether the connected hub can go on the list.
func (p *stPage) canAddHub(h gui.STHub) bool {
	return h.Connected && h.IP != "" && !slices.Contains(p.form.Hubs, h.IP)
}

func (p *stPage) body(gtx C, u *UI, s snapshot) D {
	if p.refresh.Clicked(gtx) {
		p.reload(u)
	}
	if p.share.Clicked(gtx) {
		u.open(pageSharing)
	}
	if p.secret.Clicked(gtx) {
		u.open(pageService)
	}
	if i := p.hubs.Deleted(gtx, len(p.form.Hubs)); i >= 0 {
		p.form.Hubs = gui.RemoveHub(p.form.Hubs, p.form.Hubs[i])
	}
	if p.hubs.Added(gtx) && p.canAddHub(s.hub) {
		p.form.Hubs = gui.AddHub(p.form.Hubs, s.hub.IP)
	}
	_, macs := gui.STWoLOptions(u.language(), s.hub.WoL, p.form.WoLMAC)
	if i := p.wol.Clicked(gtx, len(macs)); i >= 0 && s.hubSet {
		p.form.WoLMAC = macs[i]
	}

	th := u.th
	hr, _ := u.stStatus(s)
	blocks := []layout.Widget{
		func(gtx C) D {
			return heroCard(gtx, th, hr, smallButton(th, &p.refresh, u.t("network.refresh"), colAccent).Layout)
		},
	}
	if s.cfgSet && s.cfg.Secret == "" {
		blocks = append(blocks, func(gtx C) D {
			return p.secret.Layout(gtx, func(gtx C) D {
				pointer.CursorPointer.Add(gtx.Ops)
				return banner(gtx, th, noticeWarn, u.t("ui.smartthings.secret"))
			})
		})
	}
	blocks = append(blocks,
		section(th, u.t("ui.smartthings.hub"), p.hubCard(u, s)),
		section(th, u.t("ui.smartthings.pc"), p.pcCard(u, s)),
		section(th, u.t("ui.smartthings.search"), p.searchCard(u, s)),
		section(th, u.t("st.wol"), p.wolCard(u, s)),
		section(th, u.t("st.hubs"), p.hubsCard(u, s)),
	)
	blocks = append(blocks, section(th, u.t("ui.smartthings.network"), p.netCard(u)))
	blocks = append(blocks, section(th, u.t("ui.smartthings.share"), func(gtx C) D {
		return listRow(gtx, th, &p.share, u.t("st.session"), u.t("ui.smartthings.share.sub"), u.onOff(s.cfg.SmartThings.ExposeSession))
	}))
	return sections(gtx, blocks...)
}

// hubCard: the connected hub's address, driver and last contact.
func (p *stPage) hubCard(u *UI, s snapshot) layout.Widget {
	th := u.th
	h := s.hub
	ip, driver, seen := "—", "—", "—"
	if h.Connected {
		ip, seen = h.IP, gui.RelativeTime(u.language(), h.LastSeen, time.Now())
		if h.DriverVersion != "" {
			driver = "v" + h.DriverVersion
		}
	}
	return func(gtx C) D {
		return infoRows(gtx,
			infoRow(th, "IP", ip),
			infoRow(th, u.t("home.st.driver"), driver),
			infoRow(th, u.t("home.st.seen"), seen),
		)
	}
}

// pcCard: this PC's discovery id (copied whole) and the command port's
// firewall rule (#139).
func (p *stPage) pcCard(u *UI, s snapshot) layout.Widget {
	th := u.th
	id := s.hub.MachineID
	ws := []layout.Widget{func(gtx C) D {
		sub := u.t("ui.smartthings.machineid.loading")
		if id != "" {
			sub = gui.STMachineIDShort(id) + " · " + u.t("ui.smartthings.machineid.sub")
		}
		return settingRow(gtx, th, u.t("ui.smartthings.machineid"), sub, func(gtx C) D {
			if id == "" {
				return D{}
			}
			return p.idCopy.Layout(gtx, th, id, u.t("ui.copy"), u.t("ui.copied"))
		})
	}}
	if c := s.hub.CommandPort; c != nil {
		saved := s.cfg.Port
		if b := u.formSt.base; b != nil {
			saved = b.Port
		}
		ws = append(ws, func(gtx C) D {
			title := fmt.Sprintf(u.t("ui.smartthings.cmdport"), c.Port)
			var sub string
			var pl layout.Widget
			switch {
			case !c.Checked:
				sub, pl = u.t("ui.smartthings.cmdport.checking"), statusPill(th, u.t("ui.smartthings.checking"), toneOff)
			case c.FirewallRule:
				sub, pl = u.t("ui.smartthings.cmdport.ok"), statusPill(th, u.t("ui.smartthings.fw.ok"), toneOK)
			default:
				sub, pl = u.t("ui.smartthings.cmdport.missing"), statusPill(th, u.t("ui.smartthings.fw.missing"), toneWarn)
			}
			if saved > 0 && saved != c.Port {
				sub = fmt.Sprintf(u.t("ui.smartthings.cmdport.pending"), saved)
			}
			return settingRow(gtx, th, title, sub, pl)
		})
	}
	return func(gtx C) D { return rows(gtx, ws...) }
}

// searchCard: the SSDP responder, its firewall rule and the last search.
func (p *stPage) searchCard(u *UI, s snapshot) layout.Widget {
	th := u.th
	ss := s.hub.SSDP
	return func(gtx C) D {
		if !s.hubSet {
			return label(th, textM, u.t("ui.smartthings.loading"), colSub, font.Normal).Layout(gtx)
		}
		ws := []layout.Widget{func(gtx C) D {
			if ss.Running {
				return settingRow(gtx, th, u.t("ui.smartthings.responder"), u.t("ui.smartthings.responder.on"),
					statusPill(th, u.t("ui.smartthings.search.on"), toneOK))
			}
			return settingRow(gtx, th, u.t("ui.smartthings.responder"), u.t("ui.smartthings.responder.off"),
				statusPill(th, u.t("ui.smartthings.search.off"), toneBad))
		}}
		if ss.Running {
			ws = append(ws,
				func(gtx C) D {
					if ss.FirewallRule {
						return settingRow(gtx, th, u.t("ui.smartthings.ssdpfw"), "", statusPill(th, u.t("ui.smartthings.fw.ok"), toneOK))
					}
					return settingRow(gtx, th, u.t("ui.smartthings.ssdpfw"), u.t("ui.smartthings.ssdpfw.missing"),
						statusPill(th, u.t("ui.smartthings.fw.missing"), toneWarn))
				},
				func(gtx C) D {
					last := u.t("ui.smartthings.lastsearch.none")
					if l := ss.LastSearch; l != nil && l.IP != "" {
						last = l.IP + " · " + gui.RelativeTime(u.language(), l.At, time.Now())
					}
					return infoRow(th, u.t("ui.smartthings.lastsearch"), last)(gtx)
				},
			)
		}
		return column(gtx,
			layout.Rigid(func(gtx C) D { return rows(gtx, ws...) }),
			vspace(gapS),
			layout.Rigid(label(th, textS, u.t("ui.smartthings.search.hint"), colSub, font.Normal).Layout),
		)
	}
}

// wolCard: the adapter the hub wakes this PC through (#96), as a pick
// list (automatic first), and whether waking through it works.
func (p *stPage) wolCard(u *UI, s snapshot) layout.Widget {
	th := u.th
	l := u.language()
	info := s.hub.WoL
	return func(gtx C) D {
		head := layout.Rigid(label(th, textS, u.t("ui.smartthings.wol.hint"), colSub, font.Normal).Layout)
		if !s.hubSet {
			return column(gtx, head, vspace(gapS),
				layout.Rigid(label(th, textM, u.t("ui.smartthings.wol.loading"), colSub, font.Normal).Layout))
		}
		_, macs := gui.STWoLOptions(l, info, p.form.WoLMAC)
		sel := slices.Index(macs, p.form.WoLMAC)
		items := make([]choiceItem, len(macs))
		for i, mac := range macs {
			items[i] = stWoLItem(u, info, mac, i == 0)
		}
		pick := gui.STWoLPick(info, p.form.WoLMAC)
		t := toneBad
		switch {
		case pick == nil || pick.MAC == "":
			t = toneWarn
		case pick.WoLEnabled:
			t = toneOK
		case pick.WoLCapable:
			t = toneWarn
		}
		return column(gtx,
			head,
			vspace(gapS),
			layout.Rigid(func(gtx C) D { return p.wol.Layout(gtx, th, items, sel) }),
			vspace(gapS),
			layout.Rigid(func(gtx C) D { return banner(gtx, th, t, gui.STWoLLine(l, pick)) }),
		)
	}
}

// stWoLItem is one choice of the WoL list: automatic (naming what it
// picks now), an adapter, or a saved MAC no adapter has any more.
func stWoLItem(u *UI, info gui.STWoLInfo, mac string, auto bool) choiceItem {
	l := u.language()
	if auto {
		sub := u.t("ui.smartthings.wol.auto.none")
		if a := info.Auto; a != nil && a.MAC != "" {
			sub = a.Name + " · " + a.MAC
		}
		return choiceItem{title: u.t("ui.smartthings.wol.auto"), sub: sub}
	}
	for _, a := range info.Adapters {
		if strings.EqualFold(a.MAC, mac) {
			t := toneBad
			switch {
			case a.WoLEnabled:
				t = toneOK
			case a.WoLCapable:
				t = toneWarn
			}
			sub := a.MAC
			if a.IP != "" {
				sub += " · " + a.IP
			}
			return choiceItem{title: a.Name, sub: sub, pill: gui.STWoLStateLabel(l, a.WoLEnabled, a.WoLCapable), tone: t}
		}
	}
	return choiceItem{title: u.t("ui.smartthings.wol.missing"), sub: mac, pill: u.t("ui.smartthings.wol.gone"), tone: toneWarn}
}

// hubsCard: the allowed hubs (empty: any hub), with the connected hub
// one tap away.
func (p *stPage) hubsCard(u *UI, s snapshot) layout.Widget {
	th := u.th
	return func(gtx C) D {
		add := ""
		if p.canAddHub(s.hub) {
			add = fmt.Sprintf(u.t("ui.smartthings.hubs.add"), s.hub.IP)
		}
		hubs := p.form.Hubs
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D {
				return p.hubs.Layout(gtx, th, len(hubs), u.t("ui.smartthings.hubs.empty"), add, func(gtx C, i int) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D { return drawIcon(gtx, "hub", 18, colSub) }),
						hspace(10),
						layout.Flexed(1, line(th, textL, hubs[i], colText, font.Normal).Layout),
					)
				})
			}),
		}
		if len(hubs) > 0 {
			children = append(children, vspace(gapS),
				layout.Rigid(label(th, textS, u.t("ui.smartthings.hubs.hint"), colSub, font.Normal).Layout))
		}
		return column(gtx, children...)
	}
}

// netCard: the network adapters (/api/wol-status): the external IP, the
// service's warning, and every adapter with its MAC, IPs, link and WoL.
func (p *stPage) netCard(u *UI) layout.Widget {
	th := u.th
	n := p.net
	return func(gtx C) D {
		switch {
		case !n.set:
			return label(th, textM, u.t("ui.smartthings.network.loading"), colSub, font.Normal).Layout(gtx)
		case n.err != nil:
			return banner(gtx, th, noticeError, n.err.Error())
		case n.status.Error != "":
			return banner(gtx, th, noticeError, n.status.Error)
		}
		st := n.status
		var children []layout.FlexChild
		add := func(w layout.Widget) {
			if len(children) > 0 {
				children = append(children, vspace(gapS))
			}
			children = append(children, layout.Rigid(w))
		}
		if st.Warning != "" {
			add(func(gtx C) D { return banner(gtx, th, noticeWarn, st.Warning) })
		}
		if st.ExternalIP != "" {
			add(infoRow(th, u.t("network.externalip"), st.ExternalIP))
		}
		add(func(gtx C) D {
			return p.adapter.Layout(gtx, th, fmt.Sprintf(u.t("ui.smartthings.adapters"), len(st.Adapters)), func(gtx C) D {
				ws := make([]layout.Widget, len(st.Adapters))
				for i, a := range st.Adapters {
					ws[i] = stAdapterRow(u, a)
				}
				return rows(gtx, ws...)
			})
		})
		return column(gtx, children...)
	}
}

// stAdapterRow is one network adapter: its name over its MAC and IPs, and
// its link and WoL state as pills.
func stAdapterRow(u *UI, a gui.WoLAdapter) layout.Widget {
	th := u.th
	return func(gtx C) D {
		sub := "MAC " + a.MacAddress
		if len(a.IPs) > 0 {
			sub += "\n" + strings.Join(a.IPs, ", ")
		}
		up, upT := u.t("network.adapter.down"), toneOff
		if a.Status == "Up" {
			up, upT = u.t("network.adapter.up"), toneOK
		}
		wol, wolT := u.t("network.wol.off"), toneOff
		if a.WoLEnabled {
			wol, wolT = u.t("network.wol.on"), toneOK
		}
		return layout.Flex{Alignment: layout.Start}.Layout(gtx,
			layout.Flexed(1, titled(th, a.Name, sub)),
			hspace(gapS),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
					layout.Rigid(statusPill(th, up, upT)),
					vspace(4),
					layout.Rigid(statusPill(th, wol, wolT)),
				)
			}),
		)
	}
}
