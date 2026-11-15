package ui

// The sub-pages. Phase 1 of #165: the service settings page is a real form
// with the save bar; SmartThings, Telegram, 공유 and 업데이트 show what the
// service reports, read-only, with a note that editing follows; the log
// page shows the live log.

import (
	"slices"
	"strings"
	"time"

	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

type pages struct {
	service *servicePage
	st      *stPage
	tg      *tgPage
	share   *sharePage
	update  *updatePage
	logs    *logsPage
}

func (p *pages) init() {
	*p = pages{
		service: &servicePage{},
		st:      &stPage{},
		tg:      &tgPage{},
		share:   &sharePage{},
		update:  &updatePage{},
		logs:    &logsPage{},
	}
}

// infoRow is a read-only fact: a grey name on the left, the value right.
func infoRow(th *material.Theme, name, value string) layout.FlexChild {
	return layout.Rigid(func(gtx C) D {
		return layout.Inset{Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Rigid(label(th, textM, name, colSub, font.Normal).Layout),
				hspace(gapS),
				layout.Flexed(1, func(gtx C) D {
					l := line(th, textM, value, colText, font.Medium)
					l.Alignment = textEnd
					return l.Layout(gtx)
				}),
			)
		})
	})
}

// laterNote says the page's editing comes in a later step.
func laterNote(u *UI) layout.FlexChild {
	return layout.Rigid(func(gtx C) D {
		return layout.Inset{Top: gapS}.Layout(gtx, func(gtx C) D {
			return surface(gtx, colAccentDim, radiusM, func(gtx C) D {
				return layout.UniformInset(gapM).Layout(gtx, label(u.th, textM, u.t("home.later"), colAccent, font.Medium).Layout)
			})
		})
	})
}

func (u *UI) onOff(on bool) string {
	if on {
		return u.t("home.on")
	}
	return u.t("home.off")
}

// cards stacks cards with the standard gap between them.
func cards(gtx C, ws ...layout.Widget) D {
	children := make([]layout.FlexChild, 0, 2*len(ws))
	for i, w := range ws {
		if i > 0 {
			children = append(children, vspace(gapS))
		}
		children = append(children, layout.Rigid(func(gtx C) D { return card(gtx, w) }))
	}
	return column(gtx, children...)
}

// --- 서비스 설정 ---

// servicePage edits port, secret, browser access, the grace period, media
// control, PC notifications and debug mode: the old settings tab's form
// (gui.SettingsForm), saved through the save bar.
type servicePage struct {
	port                         widget.Editor
	secret                       secretField
	remote, media, notify, debug widget.Bool
	grace                        chipGroup
	graceVals                    []int // 0 is off
	graceSel                     int
	form                         gui.SettingsForm
}

func (p *servicePage) title(u *UI) string { return u.t("settings.service") }

func (p *servicePage) shown(_ *UI, s snapshot) { p.revert(s) }

// revert puts the widgets back to the service's config.
func (p *servicePage) revert(s snapshot) {
	p.form = gui.NewSettingsForm(s.cfg)
	p.port.SetText(p.form.Port)
	p.secret.ed.SetText(p.form.Secret)
	p.remote.Value = p.form.Remote
	p.media.Value = p.form.Media
	p.notify.Value = p.form.NotifyPC.Enabled
	p.debug.Value = p.form.Debug
	p.graceVals = append([]int{0}, gui.GraceOptions()...)
	if p.form.GraceOn && !slices.Contains(p.graceVals, p.form.GraceSec) {
		p.graceVals = append(p.graceVals, p.form.GraceSec) // set through the API: keep it
	}
	p.graceSel = 0
	if p.form.GraceOn {
		p.graceSel = slices.Index(p.graceVals, p.form.GraceSec)
	}
}

// read copies the widgets into the form.
func (p *servicePage) read() {
	p.form.Port = p.port.Text()
	p.form.Secret = p.secret.ed.Text()
	p.form.Remote = p.remote.Value
	p.form.Media = p.media.Value
	p.form.NotifyPC.Enabled = p.notify.Value
	p.form.Debug = p.debug.Value
	p.form.GraceOn = p.graceSel > 0
	if p.form.GraceOn {
		p.form.GraceSec = p.graceVals[p.graceSel]
	}
}

func (p *servicePage) dirty(s snapshot) bool {
	return s.cfgSet && p.form.Dirty(gui.SettingsBaseline(s.cfg))
}

func (p *servicePage) save(u *UI, s snapshot) {
	cfg := s.cfg
	if err := p.form.ApplyTo(&cfg, u.language()); err != nil {
		u.toast.show(err.Error(), true, time.Now())
		return
	}
	restart := cfg.Port != s.cfg.Port || cfg.WebUIRemote != s.cfg.WebUIRemote
	msg := u.t("settings.saved.body")
	if restart {
		msg = u.t("settings.saved.restart")
	}
	u.act(func() error { _, err := u.api.SaveConfig(cfg); return err }, msg, u.loadConfig)
}

func (p *servicePage) body(gtx C, u *UI, s snapshot) D {
	if i := p.grace.Clicked(gtx, len(p.graceVals)); i >= 0 {
		p.graceSel = i
	}
	for _, b := range []*widget.Bool{&p.remote, &p.media, &p.notify, &p.debug} {
		b.Update(gtx)
	}
	p.read()
	graceLabels := make([]string, len(p.graceVals))
	for i, v := range p.graceVals {
		if v == 0 {
			graceLabels[i] = u.t("home.grace.off")
		} else {
			graceLabels[i] = gui.FormatSeconds(u.language(), v)
		}
	}
	return cards(gtx,
		func(gtx C) D {
			return column(gtx,
				fieldLabel(u.th, u.t("settings.port")),
				layout.Rigid(field(u.th, &p.port, "5001")),
				vspace(gapM),
				fieldLabel(u.th, u.t("settings.secret")),
				layout.Rigid(func(gtx C) D { return p.secret.Layout(gtx, u.th, u.t("login.secret")) }),
				vspace(gapM),
				layout.Rigid(func(gtx C) D {
					return toggleRow(gtx, u.th, &p.remote, u.t("settings.remote"), u.t("settings.remote.hint"))
				}),
			)
		},
		func(gtx C) D {
			return column(gtx,
				layout.Rigid(titled(u.th, u.t("settings.grace"), u.t("settings.grace.hint"))),
				vspace(gapS),
				layout.Rigid(func(gtx C) D { return p.grace.Layout(gtx, u.th, graceLabels, p.graceSel) }),
			)
		},
		func(gtx C) D {
			return column(gtx,
				layout.Rigid(func(gtx C) D {
					return toggleRow(gtx, u.th, &p.media, u.t("settings.media"), u.t("settings.media.hint"))
				}),
				vspace(gapM),
				layout.Rigid(func(gtx C) D {
					return toggleRow(gtx, u.th, &p.notify, u.t("notifypc.enabled"), u.t("notifypc.hint"))
				}),
			)
		},
		func(gtx C) D {
			return toggleRow(gtx, u.th, &p.debug, u.t("settings.debug"), u.t("settings.debug.hint"))
		},
	)
}

// --- SmartThings ---

type stPage struct{}

func (p *stPage) title(u *UI) string { return u.t("tab.smartthings") }

func (p *stPage) shown(u *UI, _ snapshot) { go u.loadHub() }

func (p *stPage) body(gtx C, u *UI, s snapshot) D {
	h := s.hub
	hub, ip, driver, seen := u.t("home.st.none"), "—", "—", "—"
	if !s.hubSet {
		hub = u.t("st.hub.loading")
	}
	if h.Connected {
		hub, ip, driver, seen = u.t("home.connected"), h.IP, "v"+h.DriverVersion, gui.RelativeTime(u.language(), h.LastSeen, time.Now())
	}
	ssdp := u.t("st.search.off")
	if h.SSDP.Running {
		ssdp = u.t("st.search.on")
	}
	wol := u.t("st.wol.none")
	if sel := h.WoL.Selected; sel != nil {
		wol = sel.Name
	}
	hubs := u.t("st.hubs.empty")
	if len(s.cfg.SmartThings.AllowedHubs) > 0 {
		hubs = strings.Join(s.cfg.SmartThings.AllowedHubs, ", ")
	}
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return card(gtx, func(gtx C) D {
				return column(gtx,
					infoRow(u.th, u.t("home.st.hub"), hub),
					infoRow(u.th, "IP", ip),
					infoRow(u.th, u.t("home.st.driver"), driver),
					infoRow(u.th, u.t("home.st.seen"), seen),
					infoRow(u.th, u.t("home.st.search"), ssdp),
					infoRow(u.th, u.t("st.wol"), wol),
					infoRow(u.th, u.t("st.hubs"), hubs),
				)
			})
		}),
		laterNote(u),
	)
}

// --- 텔레그램 ---

type tgPage struct{}

func (p *tgPage) title(u *UI) string { return u.t("tab.telegram") }

func (p *tgPage) shown(u *UI, _ snapshot) { go u.loadHub() }

func (p *tgPage) body(gtx C, u *UI, s snapshot) D {
	tg := s.cfg.Telegram
	chat := tg.ChatID
	if chat == "" {
		chat = "—"
	}
	state := u.onOff(s.tg.Polling)
	if s.tg.Conflict {
		state = u.t("notify.control.conflict")
	}
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return card(gtx, func(gtx C) D {
				return column(gtx,
					infoRow(u.th, u.t("notify.enabled"), u.onOff(tg.Enabled)),
					infoRow(u.th, u.t("notify.control.enabled"), u.onOff(tg.ControlEnabled)),
					infoRow(u.th, u.t("notify.token"), u.onOff(tg.BotTokenSet)),
					infoRow(u.th, u.t("notify.chatid"), chat),
					infoRow(u.th, u.t("home.tg.polling"), state),
				)
			})
		}),
		laterNote(u),
	)
}

// --- 공유 ---

type sharePage struct{}

func (p *sharePage) title(u *UI) string { return u.t("tab.share") }

func (p *sharePage) body(gtx C, u *UI, s snapshot) D {
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return card(gtx, func(gtx C) D {
				return column(gtx,
					layout.Rigid(label(u.th, textM, u.t("share.intro"), colSub, font.Normal).Layout),
					vspace(gapS),
					infoRow(u.th, u.t("share.session"), u.onOff(s.cfg.SmartThings.ExposeSession)),
					infoRow(u.th, u.t("share.media"), u.onOff(s.cfg.Media.NowPlaying)),
					infoRow(u.th, u.t("share.activity"), u.onOff(s.cfg.Activity.Enabled)),
					infoRow(u.th, u.t("settings.remote"), u.onOff(s.cfg.WebUIRemote)),
				)
			})
		}),
		laterNote(u),
	)
}

// --- 업데이트 ---

type updatePage struct{}

func (p *updatePage) title(u *UI) string { return u.t("update.manual") }

func (p *updatePage) body(gtx C, u *UI, _ snapshot) D {
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return card(gtx, func(gtx C) D {
				return column(gtx,
					infoRow(u.th, u.t("home.version"), u.version),
					infoRow(u.th, u.t("update.check"), u.onOff(u.prefs.Bool(prefCheckUpdates, true))),
				)
			})
		}),
		laterNote(u),
	)
}

// --- 로그 ---

// logsPage shows the service log, newest at the bottom, polled every 3s
// while open (u.wantLogs).
type logsPage struct {
	list widget.List
}

func (p *logsPage) title(u *UI) string { return u.t("tab.logs") }

func (p *logsPage) shown(u *UI, _ snapshot) { go u.loadLogs() }

func (p *logsPage) body(gtx C, u *UI, s snapshot) D {
	var msg string
	switch {
	case s.logsErr != nil:
		msg = s.logsErr.Error()
	case !s.logsSet:
		msg = u.t("st.hub.loading")
	case len(s.logs) == 0:
		msg = u.t("logs.empty")
	}
	return card(gtx, func(gtx C) D {
		if msg != "" {
			return label(u.th, textM, msg, colSub, font.Normal).Layout(gtx)
		}
		children := make([]layout.FlexChild, len(s.logs))
		for i, l := range s.logs {
			children[i] = layout.Rigid(func(gtx C) D {
				lb := label(u.th, textS, l, logColor(l), font.Normal)
				lb.Font.Typeface = "Consolas"
				return layout.Inset{Bottom: 2}.Layout(gtx, lb.Layout)
			})
		}
		return column(gtx, children...)
	})
}

// logColor tints warnings and errors.
func logColor(l string) color.NRGBA {
	switch {
	case strings.Contains(l, "ERROR") || strings.Contains(l, "error"):
		return colDanger
	case strings.Contains(l, "WARN"):
		return colWarn
	}
	return colText
}

