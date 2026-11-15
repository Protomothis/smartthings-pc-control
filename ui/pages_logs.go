package ui

// 로그: the service log, newest at the bottom, polled every 3s while open.
// Phase 2a shows the lines; filters, follow/pause, the service.log file
// when the service is gone (#140) and opening the file or folder are #165
// phase 2b and replace this file.

import (
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
)

type logsPage struct{}

func newLogsPage() screen { return &logsPage{} }

func (p *logsPage) title(u *UI) string { return u.t("tab.logs") }

func (p *logsPage) shown(u *UI, _ snapshot) { go u.loadLogs() }

func (p *logsPage) wantsLogs() bool { return true }

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
