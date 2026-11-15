package ui

// 프리셋: the presets SmartThings routines and Telegram /run start by
// slot. Phase 2a lists them read-only; the editor (name, kind, target,
// arguments, browse, test, the local-session lock) is #165 phase 2b and
// replaces this file. It will be a form (forms.go): the save asks about
// routine slots changing hands on its own.

import (
	"gioui.org/font"
	"gioui.org/layout"
)

type presetsPage struct{}

func newPresetsPage() screen { return &presetsPage{} }

func (p *presetsPage) title(u *UI) string { return u.t("tab.presets") }

func (p *presetsPage) body(gtx C, u *UI, s snapshot) D {
	th := u.th
	return sections(gtx,
		section(th, "", func(gtx C) D {
			if len(s.cfg.Presets) == 0 {
				return label(th, textM, u.t("home.presets.empty"), colSub, font.Normal).Layout(gtx)
			}
			ws := make([]layout.Widget, len(s.cfg.Presets))
			for i, pr := range s.cfg.Presets {
				ws[i] = func(gtx C) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D { return slotBadge(gtx, th, pr.Slot) }),
						hspace(gapS),
						layout.Flexed(1, titled(th, pr.Name, u.presetLine(pr))),
					)
				}
			}
			return rows(gtx, ws...)
		}),
		pendingNote(u),
	)
}
