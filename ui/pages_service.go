package ui

// 서비스 설정: port, secret, browser access, the grace period, media
// control, PC notifications and debug mode — the old settings tab's form
// (gui.SettingsForm). It is the reference form (forms.go): the widgets are
// read into p.form at the top of body; fill, dirty and apply work on
// p.form only.

import (
	"slices"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

type servicePage struct {
	port                         widget.Editor
	secret                       secretField
	remote, media, notify, debug widget.Bool
	grace                        chipGroup
	graceVals                    []int // 0 is off
	graceSel                     int
	form                         gui.SettingsForm
}

func newServicePage() screen { return &servicePage{} }

func (p *servicePage) title(u *UI) string { return u.t("settings.service") }

// fill puts cfg into the widgets.
func (p *servicePage) fill(_ *UI, cfg gui.Config) {
	p.form = gui.NewSettingsForm(cfg)
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

func (p *servicePage) dirty(_ *UI, base gui.Config) bool { return p.form.Dirty(base) }

func (p *servicePage) apply(u *UI, cfg *gui.Config) error { return p.form.ApplyTo(cfg, u.language()) }

func (p *servicePage) body(gtx C, u *UI, _ snapshot) D {
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
	th := u.th
	return sections(gtx,
		section(th, u.t("ui.service.access"), func(gtx C) D {
			return rows(gtx,
				func(gtx C) D {
					return fieldRow(gtx, th, &p.port, u.t("settings.port"), u.t("ui.service.port.hint"), "5001", 96)
				},
				func(gtx C) D {
					return column(gtx,
						fieldLabel(th, u.t("settings.secret")),
						layout.Rigid(func(gtx C) D { return p.secret.Layout(gtx, th, u.t("login.secret")) }),
					)
				},
				func(gtx C) D {
					return toggleRowIcon(gtx, th, &p.remote, "open", toneAccent, u.t("ui.service.webui"), u.t("ui.service.webui.hint"))
				},
			)
		}),
		section(th, u.t("ui.service.remote"), func(gtx C) D {
			return column(gtx,
				layout.Rigid(titled(th, u.t("ui.service.grace"), u.t("ui.service.grace.hint"))),
				vspace(gapS),
				layout.Rigid(func(gtx C) D { return p.grace.Layout(gtx, th, graceLabels, p.graceSel) }),
			)
		}),
		section(th, u.t("ui.service.allowed"), func(gtx C) D {
			return rows(gtx,
				func(gtx C) D {
					return toggleRowIcon(gtx, th, &p.media, "volume", toneAccent, u.t("ui.service.media"), u.t("ui.service.media.hint"))
				},
				func(gtx C) D {
					return toggleRowIcon(gtx, th, &p.notify, "bell", toneAccent, u.t("ui.service.notify"), u.t("ui.service.notify.hint"))
				},
			)
		}),
		section(th, u.t("ui.service.advanced"), func(gtx C) D {
			return toggleRowIcon(gtx, th, &p.debug, "bug", toneOff, u.t("ui.service.debug"), u.t("ui.service.debug.hint"))
		}),
	)
}
