package ui

// The sub-pages: where they are registered, and the building blocks every
// page is made of.
//
// Adding or rewriting a page:
//
//  1. The page lives in ui/pages_<id>.go: a type implementing screen
//     (title, body), plus shower (shown: load its own data when opened),
//     form (fill, dirty, apply: it edits config fields, forms.go) and
//     logsWanter as needed, and a constructor new<Name>Page() screen.
//  2. Its id and constructor are registered once, below (pageIDs,
//     pageOrder, pageRegistry). Every page of #165 is registered already,
//     so a screen rewrite only replaces the contents of its own file.
//  3. Other screens open it with u.open(pageX).
//  4. Its new texts go in gui/locales/ko/<id>.json and
//     gui/locales/en/<id>.json (same keys in both), named "ui.<id>.…";
//     existing keys of the Fyne tabs ("notify.…", "st.…") may be reused as
//     they are. Keys are looked up with u.t("…") so the locale tests see
//     them.
//  5. Toolkit-free logic it needs from package gui is exported from a file
//     of its own there (gui/export_<id>.go), not from viewmodel.go, so two
//     screens never edit one file.
//  6. Icons a page needs beyond iconData are added from its own file:
//     func init() { iconData["name"] = icons.X }.
//
// A connection page (SmartThings, 텔레그램, 공유) starts with a heroCard
// (its state at a glance) and continues with sections: a small grey
// header over a card each. A settings page is sections of rows.

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// pageID names a sub-page; its texts are "ui.<id>.…".
type pageID string

const (
	pageService     pageID = "service"     // 서비스 설정: the reference form (pages_service.go)
	pageServiceCtl  pageID = "servicectl"  // 서비스 관리: install, start/stop, install location
	pageSmartThings pageID = "smartthings" // SmartThings: hub, search, WoL, allowed hubs
	pageTelegram    pageID = "telegram"    // 텔레그램: bot, chats, control, events, quiet hours
	pageSharing     pageID = "sharing"     // 공유: session, now playing, running apps
	pagePresets     pageID = "presets"     // 프리셋 편집
	pageLogs        pageID = "logs"        // 로그
	pageUpdate      pageID = "update"      // 업데이트
)

// pageOrder is every page, in the order forms are visited.
var pageOrder = []pageID{pageService, pageServiceCtl, pageSmartThings, pageTelegram, pageSharing, pagePresets, pageLogs, pageUpdate}

// pageRegistry builds each page. The one place pages are registered.
var pageRegistry = map[pageID]func() screen{
	pageService:     newServicePage,
	pageServiceCtl:  newServiceCtlPage,
	pageSmartThings: newSmartThingsPage,
	pageTelegram:    newTelegramPage,
	pageSharing:     newSharingPage,
	pagePresets:     newPresetsPage,
	pageLogs:        newLogsPage,
	pageUpdate:      newUpdatePage,
}

func buildPages() map[pageID]screen {
	out := make(map[pageID]screen, len(pageRegistry))
	for id, mk := range pageRegistry {
		out[id] = mk()
	}
	return out
}

// --- notices -------------------------------------------------------------------

// A notice is a banner at the top of a page: a failed check or save, the
// service's warnings after a save, anything a page wants to keep in view
// (unlike a toast, it stays until cleared or the page is left).
type notice struct {
	kind tone
	text string
}

// Notice kinds.
const (
	noticeInfo  = toneAccent
	noticeOK    = toneOK
	noticeWarn  = toneWarn
	noticeError = toneBad
)

// setNotice shows a banner at the top of page p. Window goroutine.
func (u *UI) setNotice(p screen, kind tone, text string) {
	u.notices[p] = notice{kind: kind, text: text}
}

// clearNotice removes p's banner. Window goroutine.
func (u *UI) clearNotice(p screen) { delete(u.notices, p) }

// layoutNotice draws p's banner, if it has one.
func (u *UI) layoutNotice(gtx C, p screen) D {
	n, ok := u.notices[p]
	if !ok {
		return D{}
	}
	return layout.Inset{Bottom: gapS}.Layout(gtx, func(gtx C) D { return banner(gtx, u.th, n.kind, n.text) })
}

// --- page building blocks ----------------------------------------------------

// hero is the state at the top of a connection page.
type hero struct {
	icon     string
	tone     tone
	headline string // "허브 연결됨"
	sub      string // "192.168.1.20 · 9초 전 확인"
}

// heroCard draws h large: a tinted icon tile, the headline and the grey
// line under it, with an optional control on the right (a test button).
func heroCard(gtx C, th *material.Theme, h hero, trailing layout.Widget) D {
	return card(gtx, func(gtx C) D {
		return row(gtx,
			layout.Rigid(func(gtx C) D { return iconTile(gtx, h.icon, h.tone, 52) }),
			hspace(gapM),
			layout.Flexed(1, func(gtx C) D {
				children := []layout.FlexChild{
					layout.Rigid(func(gtx C) D {
						return row(gtx,
							layout.Rigid(func(gtx C) D {
								fg, _ := h.tone.colors()
								return statusDot(gtx, fg, 8)
							}),
							hspace(8),
							layout.Flexed(1, label(th, heroSize, h.headline, colText, font.Bold).Layout),
						)
					}),
				}
				if h.sub != "" {
					children = append(children, vspace(4), layout.Rigid(label(th, textM, h.sub, colSub, font.Normal).Layout))
				}
				return column(gtx, children...)
			}),
			layout.Rigid(func(gtx C) D {
				if trailing == nil {
					return D{}
				}
				return layout.Inset{Left: gapS}.Layout(gtx, trailing)
			}),
		)
	})
}

// heroSize is the hero card's headline.
const heroSize unit.Sp = 19

// section is a small grey header over a card holding w. An empty header
// draws the card alone.
func section(th *material.Theme, header string, w layout.Widget) layout.Widget {
	return func(gtx C) D {
		if header == "" {
			return card(gtx, w)
		}
		return column(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: 4, Bottom: 8}.Layout(gtx, label(th, textM, header, colSub, font.SemiBold).Layout)
			}),
			layout.Rigid(func(gtx C) D { return card(gtx, w) }),
		)
	}
}

// sections stacks a page's blocks (heroCard, section …) with the gap
// between groups.
func sections(gtx C, ws ...layout.Widget) D {
	children := make([]layout.FlexChild, 0, 2*len(ws))
	for i, w := range ws {
		if i > 0 {
			children = append(children, vspace(gapL))
		}
		children = append(children, layout.Rigid(w))
	}
	return column(gtx, children...)
}

// rows stacks the rows of one card with a hairline between them.
func rows(gtx C, ws ...layout.Widget) D {
	children := make([]layout.FlexChild, 0, 3*len(ws))
	for i, w := range ws {
		if i > 0 {
			children = append(children, vspace(gapS), layout.Rigid(divider), vspace(gapS))
		}
		children = append(children, layout.Rigid(w))
	}
	return column(gtx, children...)
}

// infoRow is a read-only fact: a grey name on the left, the value right.
func infoRow(th *material.Theme, name, value string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: 2, Bottom: 2}.Layout(gtx, func(gtx C) D {
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
	}
}

// infoRows stacks read-only facts with a little air between them.
func infoRows(gtx C, ws ...layout.Widget) D {
	children := make([]layout.FlexChild, 0, 2*len(ws))
	for i, w := range ws {
		if i > 0 {
			children = append(children, vspace(10))
		}
		children = append(children, layout.Rigid(w))
	}
	return column(gtx, children...)
}

// pendingNote is a quiet line at the end of a page that only shows what
// it will soon edit (until its #165 phase-2 screen lands).
func pendingNote(u *UI) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Left: 4, Top: 2}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "info", 16, colFaint) }),
				hspace(6),
				layout.Flexed(1, label(u.th, textS, u.t("ui.page.pending"), colFaint, font.Normal).Layout),
			)
		})
	}
}

func (u *UI) onOff(on bool) string {
	if on {
		return u.t("home.on")
	}
	return u.t("home.off")
}
