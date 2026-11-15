package ui

// 텔레그램: the bot (token, chat ID, test message, finding the chat), what
// it may do (notifications, remote control and the chats allowed to),
// which events it reports, quiet hours and how messages look — the old
// 텔레그램 tab (gui/notify_tab.go) as a form. The model is gui.TelegramForm
// (gui/export_telegram.go); the widgets are read into p.form at the top of
// body, and fill, dirty and apply work on p.form only.
//
// The token field starts empty: an empty field keeps the stored token, a
// pasted one replaces it, 토큰 삭제 clears it on save. After a save the
// field is emptied again (saved).

import (
	"fmt"
	"image"
	"os"
	"slices"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

func init() { iconData["chevleft"] = icons.NavigationChevronLeft }

// tgAPI is the part of the client only this page uses: the helpers of
// /api/telegram/*. The client has them; a test fake may not, and the page
// then says the action is unavailable.
type tgAPI interface {
	TestTelegram(token, chatID string) error
	TelegramMe() (username, name string, err error)
	TelegramChats(token string) ([]gui.TelegramChat, error)
}

// tgResult is the outcome of an action shown in the page (a test message,
// the bot check): a tone and a text, "" for none.
type tgResult struct {
	tone tone
	text string
}

type tgPage struct {
	form   gui.TelegramForm
	masked string // the stored token as the service shows it ("****6789"), "" for none
	cat    []gui.TelegramCategory
	host   string // the PC name's placeholder

	// 봇
	token      secretField
	clearToken bool // 토큰 삭제 pressed: the save sends "-"
	clearBtn   widget.Clickable
	undoClear  widget.Clickable
	chat       widget.Editor
	findBtn    widget.Clickable
	testBtn    widget.Clickable
	linkBtn    widget.Clickable
	bot        tgResult // getMe for the stored token
	test       tgResult
	testBusy   bool
	finding    bool
	found      *[]gui.TelegramChat // nil: the list is closed
	findErr    string
	closeFound widget.Clickable
	pick       []widget.Clickable // one per found chat: use as the chat ID
	allowPick  []widget.Clickable // one per found chat: allow control

	// 기능
	enabled, control widget.Bool
	allowed          []string
	allowedList      listEditor
	addID            widget.Editor
	commands         expander

	// 받을 알림
	masters      []widget.Bool // one per category
	kindClk      [][]widget.Clickable
	allOn, allOf widget.Clickable

	// 조용한 시간대
	quiet, quietSec, quietDigest widget.Bool
	startDown, startUp           widget.Clickable
	endDown, endUp               widget.Clickable

	// 표시
	detail     chipGroup
	detailVals []string
	pcName     widget.Editor
}

func newTelegramPage() screen {
	host, _ := os.Hostname()
	cat := gui.TelegramCatalogue()
	p := &tgPage{cat: cat, host: host, detailVals: gui.TelegramDetailOptions()}
	p.masters = make([]widget.Bool, len(cat))
	p.kindClk = make([][]widget.Clickable, len(cat))
	for i, c := range cat {
		p.kindClk[i] = make([]widget.Clickable, len(c.Kinds))
	}
	p.form = gui.NewTelegramForm(gui.Config{})
	return p
}

func (p *tgPage) title(u *UI) string { return u.t("tab.telegram") }

func (p *tgPage) shown(u *UI, s snapshot) {
	go u.loadHub()
	p.test = tgResult{}
	p.found, p.findErr = nil, ""
	p.checkBot(u, s.cfg.Telegram.BotTokenSet)
}

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

// --- form ----------------------------------------------------------------------

// fill puts cfg into the widgets. The token field starts empty (keep).
func (p *tgPage) fill(_ *UI, cfg gui.Config) {
	p.form = gui.NewTelegramForm(cfg)
	p.masked = p.form.Token
	p.form.Token = ""
	p.token.ed.SetText("")
	p.clearToken = false
	p.chat.SetText(p.form.ChatID)
	p.enabled.Value = p.form.Enabled
	p.control.Value = p.form.ControlEnabled
	p.allowed = gui.ParseChatIDs(p.form.AllowedChatIDs)
	p.addID.SetText("")
	for i, c := range p.cat {
		p.masters[i].Value = p.form.Masters[c.Cat]
	}
	p.quiet.Value = p.form.Quiet.Enabled
	p.quietSec.Value = p.form.Quiet.SecurityBypass
	p.quietDigest.Value = p.form.Quiet.Digest
	p.pcName.SetText(p.form.PCName)
}

// read copies the widgets into the form.
func (p *tgPage) read() {
	p.form.Token = strings.TrimSpace(p.token.ed.Text())
	if p.clearToken && p.form.Token == "" {
		p.form.Token = "-"
	}
	p.form.ChatID = p.chat.Text()
	p.form.Enabled = p.enabled.Value
	p.form.ControlEnabled = p.control.Value
	p.form.AllowedChatIDs = strings.Join(p.allowed, ", ")
	for i, c := range p.cat {
		p.form.Masters[c.Cat] = p.masters[i].Value
	}
	p.form.Quiet.Enabled = p.quiet.Value
	p.form.Quiet.SecurityBypass = p.quietSec.Value
	p.form.Quiet.Digest = p.quietDigest.Value
	p.form.PCName = p.pcName.Text()
}

func (p *tgPage) dirty(_ *UI, base gui.Config) bool { return p.form.Dirty(base) }

func (p *tgPage) apply(u *UI, cfg *gui.Config) error {
	p.form.ApplyTo(cfg, u.language())
	return nil
}

// saved empties the token field (the token is stored now, or cleared) and
// checks the bot again.
func (p *tgPage) saved(u *UI, _ gui.SaveReply) {
	p.token.ed.SetText("")
	p.token.shown = false
	p.clearToken = false
	p.form.Token = ""
	set := false
	if b := u.formSt.base; b != nil {
		p.masked = b.Telegram.BotToken
		set = b.Telegram.BotTokenSet || b.Telegram.BotToken != ""
	}
	p.checkBot(u, set)
}

// --- the event catalogue ---------------------------------------------------------

// kindOn reports whether kind of category i shows as on.
func (p *tgPage) kindOn(i int, kind string) bool { return p.form.Kinds[p.cat[i].Cat][kind] }

// toggleKind flips one kind; the last one off takes its category off.
func (p *tgPage) toggleKind(i int, kind string) {
	kinds := p.form.Kinds[p.cat[i].Cat]
	kinds[kind] = !kinds[kind]
	p.masters[i].Value = anyKindOn(kinds)
}

// masterTurned follows a category switch: turned on with every kind off,
// it starts with all of them on (or it would derive straight back off).
func (p *tgPage) masterTurned(i int) {
	kinds := p.form.Kinds[p.cat[i].Cat]
	if p.masters[i].Value && !anyKindOn(kinds) {
		for k := range kinds {
			kinds[k] = true
		}
	}
}

// setAll is 모두 켬 / 모두 끔.
func (p *tgPage) setAll(on bool) {
	for i, c := range p.cat {
		for k := range p.form.Kinds[c.Cat] {
			p.form.Kinds[c.Cat][k] = on
		}
		p.masters[i].Value = on
	}
}

func anyKindOn(m map[string]bool) bool {
	for _, v := range m {
		if v {
			return true
		}
	}
	return false
}

// --- quiet hours -------------------------------------------------------------------

// stepTime moves an "HH:MM" to the previous (dir < 0) or next 30-minute
// mark, around midnight. A time off the grid ("22:15", set through the
// API) moves to the nearest mark in that direction; garbage becomes 00:00.
func stepTime(v string, dir int) string {
	h, m, ok := strings.Cut(v, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return "00:00"
	}
	t := hh*60 + mm
	if dir > 0 {
		t = (t/30 + 1) * 30
	} else {
		t = (t+29)/30*30 - 30
	}
	t = (t%1440 + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", t/60, t%60)
}

// --- actions -------------------------------------------------------------------

// checkBot asks which bot the stored token belongs to.
func (p *tgPage) checkBot(u *UI, set bool) {
	if !set {
		p.bot = tgResult{tone: toneOff, text: u.t("notify.bot.none")}
		return
	}
	a, ok := u.api.(tgAPI)
	if !ok {
		p.bot = tgResult{}
		return
	}
	p.bot = tgResult{tone: toneOff, text: u.t("notify.bot.checking")}
	go func() {
		name, err := withLocalSession(u, func() (string, error) {
			n, _, err := a.TelegramMe()
			return n, err
		})
		u.onWindow(func() {
			if err != nil {
				p.bot = tgResult{tone: toneBad, text: fmt.Sprintf(u.t("notify.bot.error"), err.Error())}
				return
			}
			p.bot = tgResult{tone: toneOK, text: fmt.Sprintf(u.t("notify.bot.connected"), name)}
		})
	}()
}

// tokenForLookup is the token a test or a chat lookup uses: the one typed,
// else the stored one (the service resolves the masked value).
func (p *tgPage) tokenForLookup() string {
	return gui.TelegramTokenToSend(strings.TrimSpace(p.token.ed.Text()), p.masked)
}

// sendTest sends one test message with the page's current, unsaved token
// and chat ID.
func (p *tgPage) sendTest(u *UI) {
	a, ok := u.api.(tgAPI)
	if !ok {
		p.test = tgResult{tone: toneBad, text: u.t("ui.telegram.unavailable")}
		return
	}
	p.testBusy = true
	p.test = tgResult{}
	token, chatID := p.tokenForLookup(), strings.TrimSpace(p.chat.Text())
	go func() {
		_, err := withLocalSession(u, func() (struct{}, error) { return struct{}{}, a.TestTelegram(token, chatID) })
		u.onWindow(func() {
			p.testBusy = false
			if err != nil {
				p.test = tgResult{tone: toneBad, text: err.Error()}
				return
			}
			p.test = tgResult{tone: toneOK, text: u.t("ui.telegram.test.sent")}
		})
	}()
}

// findChats lists the chats that recently wrote to the bot, in the page.
func (p *tgPage) findChats(u *UI) {
	a, ok := u.api.(tgAPI)
	if !ok {
		p.found, p.findErr = &[]gui.TelegramChat{}, u.t("ui.telegram.unavailable")
		return
	}
	p.finding = true
	token := p.tokenForLookup()
	go func() {
		chats, err := withLocalSession(u, func() ([]gui.TelegramChat, error) { return a.TelegramChats(token) })
		u.onWindow(func() { p.showChats(chats, err) })
	}()
}

// showChats opens the found chats (or why there are none).
func (p *tgPage) showChats(chats []gui.TelegramChat, err error) {
	p.finding = false
	p.findErr = ""
	if err != nil {
		p.findErr = err.Error()
		chats = nil
	}
	p.found = &chats
	p.pick = make([]widget.Clickable, len(chats))
	p.allowPick = make([]widget.Clickable, len(chats))
}

// addAllowed adds the chat IDs in text (comma-separated) not yet allowed.
func (p *tgPage) addAllowed(text string) {
	for _, id := range gui.ParseChatIDs(text) {
		if !slices.Contains(p.allowed, id) {
			p.allowed = append(p.allowed, id)
		}
	}
}

// --- body ----------------------------------------------------------------------

// events handles this frame's clicks before anything is drawn.
func (p *tgPage) events(gtx C, u *UI) {
	if p.clearBtn.Clicked(gtx) {
		p.clearToken = true
		p.token.ed.SetText("")
	}
	if p.undoClear.Clicked(gtx) {
		p.clearToken = false
	}
	if p.token.ed.Text() != "" {
		p.clearToken = false
	}
	if p.testBtn.Clicked(gtx) && !p.testBusy {
		p.sendTest(u)
	}
	if p.findBtn.Clicked(gtx) && !p.finding {
		p.findChats(u)
	}
	if p.closeFound.Clicked(gtx) {
		p.found, p.findErr = nil, ""
	}
	if p.found != nil {
		for i, c := range *p.found {
			if p.pick[i].Clicked(gtx) {
				p.chat.SetText(c.ChatID)
				p.found = nil
				break
			}
			if p.allowPick[i].Clicked(gtx) {
				p.addAllowed(c.ChatID)
			}
		}
	}
	if p.linkBtn.Clicked(gtx) {
		go func() { _ = openURL(gui.BotFatherURL) }()
	}
	if i := p.allowedList.Deleted(gtx, len(p.allowed)); i >= 0 {
		p.allowed = slices.Delete(slices.Clone(p.allowed), i, i+1)
	}
	if p.allowedList.Added(gtx) {
		p.addAllowed(p.addID.Text())
		p.addID.SetText("")
	}
	for i := range p.cat {
		if p.masters[i].Update(gtx) {
			p.masterTurned(i)
		}
		for j, kind := range p.cat[i].Kinds {
			if p.kindClk[i][j].Clicked(gtx) {
				p.toggleKind(i, kind)
			}
		}
	}
	if p.allOn.Clicked(gtx) {
		p.setAll(true)
	}
	if p.allOf.Clicked(gtx) {
		p.setAll(false)
	}
	for _, s := range []struct {
		clk *widget.Clickable
		v   *string
		dir int
	}{{&p.startDown, &p.form.Quiet.Start, -1}, {&p.startUp, &p.form.Quiet.Start, 1}, {&p.endDown, &p.form.Quiet.End, -1}, {&p.endUp, &p.form.Quiet.End, 1}} {
		if s.clk.Clicked(gtx) {
			*s.v = stepTime(*s.v, s.dir)
		}
	}
	if i := p.detail.Clicked(gtx, len(p.detailVals)); i >= 0 {
		p.form.Detail = p.detailVals[i]
	}
	for _, b := range []*widget.Bool{&p.enabled, &p.control, &p.quiet, &p.quietSec, &p.quietDigest} {
		b.Update(gtx)
	}
}

func (p *tgPage) body(gtx C, u *UI, s snapshot) D {
	p.events(gtx, u)
	p.read()
	th := u.th
	hr, _ := u.tgStatus(s)
	return sections(gtx,
		func(gtx C) D { return heroCard(gtx, th, hr, nil) },
		section(th, u.t("ui.telegram.bot"), func(gtx C) D { return p.botCard(gtx, u, s) }),
		section(th, u.t("ui.telegram.features"), func(gtx C) D { return p.featuresCard(gtx, u, s) }),
		section(th, u.t("notify.events"), func(gtx C) D { return p.eventsCard(gtx, u) }),
		section(th, u.t("notify.quiet"), func(gtx C) D { return p.quietCard(gtx, u) }),
		section(th, u.t("notify.display"), func(gtx C) D { return p.displayCard(gtx, u) }),
	)
}

// grey is a quiet line of explanation.
func grey(u *UI, txt string) layout.Widget {
	return label(u.th, textS, txt, colSub, font.Normal).Layout
}

// resultLine is a tgResult as one line in its tone (grey when neutral).
func resultLine(u *UI, r tgResult) layout.Widget {
	col := colSub
	switch r.tone {
	case toneOK:
		col = colOK
	case toneBad:
		col = colDanger
	}
	return label(u.th, textS, r.text, col, font.Medium).Layout
}

func (p *tgPage) botCard(gtx C, u *UI, s snapshot) D {
	th := u.th
	tokenHint := u.t("notify.token.placeholder")
	if p.masked != "" {
		tokenHint = fmt.Sprintf(u.t("ui.telegram.token.keep"), p.masked)
	}
	// the line under the token: what saving does, else the bot check
	status := func(gtx C) D {
		switch {
		case p.clearToken:
			return row(gtx,
				layout.Flexed(1, resultLine(u, tgResult{tone: toneBad, text: u.t("ui.telegram.token.clearing")})),
				layout.Rigid(smallButton(th, &p.undoClear, u.t("login.cancel"), colText).Layout),
			)
		case gui.TelegramTokenChanged(p.form.Token, p.masked):
			return resultLine(u, tgResult{tone: toneAccent, text: u.t("ui.telegram.token.new")})(gtx)
		}
		return row(gtx,
			layout.Flexed(1, resultLine(u, p.bot)),
			layout.Rigid(func(gtx C) D {
				if !s.cfg.Telegram.BotTokenSet && p.masked == "" {
					return D{}
				}
				return layout.Inset{Left: gapS}.Layout(gtx, smallButton(th, &p.clearBtn, u.t("ui.telegram.token.clear"), colDanger).Layout)
			}),
		)
	}
	testTxt := u.t("notify.test")
	if p.testBusy {
		testTxt = u.t("notify.test.sending")
	}
	findTxt := u.t("notify.chatid.find")
	if p.finding {
		findTxt = u.t("ui.telegram.find.busy")
	}
	return column(gtx,
		fieldLabel(th, u.t("notify.token")),
		layout.Rigid(func(gtx C) D { return p.token.Layout(gtx, th, tokenHint) }),
		vspace(8),
		layout.Rigid(status),
		vspace(gapM),
		fieldLabel(th, u.t("notify.chatid")),
		layout.Rigid(field(th, &p.chat, "123456789")),
		vspace(gapS),
		layout.Rigid(func(gtx C) D {
			return wrap(gtx, gapXS, gapXS,
				smallButton(th, &p.findBtn, findTxt, colAccent).Layout,
				smallButton(th, &p.testBtn, testTxt, colAccent).Layout,
			)
		}),
		layout.Rigid(func(gtx C) D {
			if p.test.text == "" {
				return D{}
			}
			return layout.Inset{Top: gapS}.Layout(gtx, func(gtx C) D { return banner(gtx, th, p.test.tone, p.test.text) })
		}),
		layout.Rigid(func(gtx C) D {
			if p.found == nil {
				return D{}
			}
			return layout.Inset{Top: gapS}.Layout(gtx, func(gtx C) D { return p.foundPanel(gtx, u) })
		}),
		vspace(gapM),
		layout.Rigid(divider),
		vspace(gapS),
		layout.Rigid(grey(u, u.t("ui.telegram.help"))),
		vspace(8),
		layout.Rigid(natural(smallButton(th, &p.linkBtn, u.t("ui.telegram.help.link"), colAccent).Layout)),
	)
}

// foundPanel is the chats the lookup found, in an inset panel: tap one to
// use it as the chat ID, or allow it to control the PC.
func (p *tgPage) foundPanel(gtx C, u *UI) D {
	th := u.th
	chats := *p.found
	return surface(gtx, colBg, radiusM, func(gtx C) D {
		return layout.UniformInset(gapS).Layout(gtx, func(gtx C) D {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return row(gtx,
						layout.Flexed(1, label(th, textM, u.t("notify.chatid.pick"), colText, font.SemiBold).Layout),
						layout.Rigid(smallButton(th, &p.closeFound, u.t("ui.telegram.find.close"), colText).Layout),
					)
				}),
				vspace(8),
			}
			switch {
			case p.findErr != "":
				children = append(children, layout.Rigid(func(gtx C) D { return banner(gtx, th, toneBad, p.findErr) }))
			case len(chats) == 0:
				children = append(children, layout.Rigid(grey(u, u.t("notify.chatid.none"))))
			}
			for i, c := range chats {
				if i > 0 {
					children = append(children, layout.Rigid(divider))
				}
				children = append(children, layout.Rigid(func(gtx C) D {
					return row(gtx,
						layout.Flexed(1, func(gtx C) D {
							return listRow(gtx, th, &p.pick[i], gui.TelegramChatTitle(c), c.ChatID, "")
						}),
						layout.Rigid(func(gtx C) D {
							if slices.Contains(p.allowed, c.ChatID) {
								return layout.Inset{Left: 8}.Layout(gtx, statusPill(th, u.t("ui.telegram.find.allowed"), toneOK))
							}
							return layout.Inset{Left: 8}.Layout(gtx, smallButton(th, &p.allowPick[i], u.t("ui.telegram.find.allow"), colAccent).Layout)
						}),
					)
				}))
			}
			return column(gtx, children...)
		})
	})
}

func (p *tgPage) featuresCard(gtx C, u *UI, s snapshot) D {
	th := u.th
	controlBody := func(gtx C) D {
		children := []layout.FlexChild{}
		if s.tg.Conflict {
			children = append(children,
				layout.Rigid(func(gtx C) D { return banner(gtx, th, toneWarn, u.t("notify.control.conflict")) }),
				vspace(8))
		}
		children = append(children,
			layout.Rigid(func(gtx C) D { return banner(gtx, th, toneWarn, u.t("notify.control.warn")) }),
			vspace(gapM),
			fieldLabel(th, u.t("notify.control.allowed")),
			layout.Rigid(func(gtx C) D {
				return p.allowedList.Layout(gtx, th, len(p.allowed), u.t("ui.telegram.allowed.empty"), "", func(gtx C, i int) D {
					return line(th, textL, p.allowed[i], colText, font.Normal).Layout(gtx)
				})
			}),
			vspace(gapS),
			layout.Rigid(func(gtx C) D {
				return row(gtx,
					layout.Flexed(1, field(th, &p.addID, u.t("ui.telegram.allowed.hint"))),
					hspace(8),
					layout.Rigid(smallButton(th, &p.allowedList.add, "+ "+u.t("ui.telegram.allowed.add"), colAccent).Layout),
				)
			}),
			vspace(gapS),
			layout.Rigid(func(gtx C) D {
				return p.commands.Layout(gtx, th, u.t("ui.telegram.commands"), grey(u, u.t("notify.control.hint")))
			}),
		)
		return column(gtx, children...)
	}
	return rows(gtx,
		func(gtx C) D {
			return toggleRowIcon(gtx, th, &p.enabled, "bell", toneAccent, u.t("ui.telegram.notify.title"), u.t("ui.telegram.notify.hint"))
		},
		func(gtx C) D {
			return column(gtx,
				layout.Rigid(func(gtx C) D {
					return toggleRowIcon(gtx, th, &p.control, "remote", toneAccent, u.t("ui.telegram.control.title"), u.t("ui.telegram.control.hint"))
				}),
				layout.Rigid(func(gtx C) D {
					if !p.control.Value {
						return D{}
					}
					return layout.Inset{Top: gapM}.Layout(gtx, controlBody)
				}),
			)
		},
	)
}

func (p *tgPage) eventsCard(gtx C, u *UI) D {
	th := u.th
	ws := []layout.Widget{func(gtx C) D {
		return row(gtx,
			layout.Flexed(1, grey(u, u.t("ui.telegram.events.hint"))),
			hspace(gapS),
			layout.Rigid(smallButton(th, &p.allOn, u.t("notify.all.on"), colAccent).Layout),
			hspace(gapXS),
			layout.Rigid(smallButton(th, &p.allOf, u.t("notify.all.off"), colText).Layout),
		)
	}}
	for i, c := range p.cat {
		ws = append(ws, func(gtx C) D {
			on := 0
			for _, k := range c.Kinds {
				if p.kindOn(i, k) {
					on++
				}
			}
			sub := ""
			if p.masters[i].Value {
				sub = fmt.Sprintf(u.t("ui.telegram.kinds.count"), on, len(c.Kinds))
			}
			return column(gtx,
				layout.Rigid(func(gtx C) D { return toggleRow(gtx, th, &p.masters[i], u.t("notify.cat."+c.Cat), sub) }),
				layout.Rigid(func(gtx C) D {
					if !p.masters[i].Value {
						return D{}
					}
					chips := make([]layout.Widget, len(c.Kinds))
					for j, k := range c.Kinds {
						chips[j] = checkChip(th, &p.kindClk[i][j], u.t("notify.kind."+c.Cat+"."+k), p.kindOn(i, k))
					}
					return layout.Inset{Top: gapS}.Layout(gtx, func(gtx C) D { return wrap(gtx, gapXS, gapXS, chips...) })
				}),
			)
		})
	}
	return rows(gtx, ws...)
}

// natural lays w out at its own width instead of the card's (a lone
// button in a column).
func natural(w layout.Widget) layout.Widget {
	return func(gtx C) D {
		gtx.Constraints.Min.X = 0
		return w(gtx)
	}
}

// checkChip is a chip of a multiple choice: grey while off, accent text
// on the accent tint with a check while on. A candidate for widgets.go
// next to chip.
func checkChip(th *material.Theme, clk *widget.Clickable, txt string, on bool) layout.Widget {
	return func(gtx C) D {
		return clk.Layout(gtx, func(gtx C) D {
			pointer.CursorPointer.Add(gtx.Ops)
			fg, bg, weight := colText, colGhost, font.Medium
			if on {
				fg, bg, weight = colAccent, colAccentDim, font.SemiBold
			}
			if clk.Hovered() {
				bg = mix(bg, fg, 0.08)
			}
			return layout.Stack{}.Layout(gtx,
				layout.Expanded(func(gtx C) D {
					r := image.Rectangle{Max: gtx.Constraints.Min}
					paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radiusS)).Op(gtx.Ops))
					return D{Size: gtx.Constraints.Min}
				}),
				layout.Stacked(func(gtx C) D {
					left := unit.Dp(12)
					if on {
						left = 8
					}
					return layout.Inset{Top: 7, Bottom: 7, Left: left, Right: 12}.Layout(gtx, func(gtx C) D {
						children := []layout.FlexChild{}
						if on {
							children = append(children,
								layout.Rigid(func(gtx C) D { return drawIcon(gtx, "check", 16, fg) }),
								hspace(4))
						}
						children = append(children, layout.Rigid(line(th, textM, txt, fg, weight).Layout))
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
					})
				}),
			)
		})
	}
}

func (p *tgPage) quietCard(gtx C, u *UI) D {
	th := u.th
	stepper := func(down, up *widget.Clickable, v string) layout.Widget {
		return func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return iconButton(gtx, down, "chevleft", 32, colText, colGhost) }),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Dp(64)
					return layout.Center.Layout(gtx, line(th, textL, v, colText, font.SemiBold).Layout)
				}),
				layout.Rigid(func(gtx C) D { return iconButton(gtx, up, "chevron", 32, colText, colGhost) }),
			)
		}
	}
	ws := []layout.Widget{func(gtx C) D {
		return toggleRowIcon(gtx, th, &p.quiet, "sleep", toneAccent, u.t("notify.quiet.enabled"), u.t("ui.telegram.quiet.sub"))
	}}
	if p.quiet.Value {
		ws = append(ws,
			func(gtx C) D {
				return settingRow(gtx, th, u.t("ui.telegram.quiet.start"), "", stepper(&p.startDown, &p.startUp, p.form.Quiet.Start))
			},
			func(gtx C) D {
				return settingRow(gtx, th, u.t("ui.telegram.quiet.end"), "", stepper(&p.endDown, &p.endUp, p.form.Quiet.End))
			},
			func(gtx C) D { return toggleRow(gtx, th, &p.quietSec, u.t("notify.quiet.security"), "") },
			func(gtx C) D { return toggleRow(gtx, th, &p.quietDigest, u.t("notify.quiet.digest"), "") },
			grey(u, u.t("ui.telegram.quiet.hint")),
		)
	}
	return rows(gtx, ws...)
}

func (p *tgPage) displayCard(gtx C, u *UI) D {
	th := u.th
	labels := make([]string, len(p.detailVals))
	for i, v := range p.detailVals {
		labels[i] = u.t("notify.detail." + v)
	}
	return rows(gtx,
		func(gtx C) D {
			return settingRow(gtx, th, u.t("notify.detail"), "", func(gtx C) D {
				return p.detail.Layout(gtx, th, labels, slices.Index(p.detailVals, p.form.Detail))
			})
		},
		func(gtx C) D {
			return column(gtx,
				fieldLabel(th, u.t("notify.pcname")),
				layout.Rigid(field(th, &p.pcName, gui.PCNamePlaceholder(p.host, u.t("notify.pcname.placeholder")))),
				vspace(6),
				layout.Rigid(grey(u, u.t("ui.telegram.pcname.hint"))),
			)
		},
		grey(u, u.t("notify.display.hint")),
	)
}
