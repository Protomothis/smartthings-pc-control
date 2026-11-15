package ui

// The home screen: one scrolling column of cards.
//
//	SmartThings PC Control                [배터리 80%]
//	● 서비스 연결됨
//	┌ 바로 실행 ─ 화면 끄기 · 절전 · 재시작 · 종료, 더 보기, 잠들지 않기 ┐
//	┌ 미디어 ─ 지금 재생 중, ⏮ ⏯ ⏭, 볼륨 ┐
//	┌ 예약 ─ 남은 시간 / 명령·시간 칩과 [예약 시작] ┐
//	┌ 프리셋 ─ 목록과 [실행], 추가는 카드 안에서 펼침 ┐
//	연결: SmartThings · 텔레그램 · 공유 (each → its page)
//	┌ 설정 ─ 테마, 언어, 자동 시작, 서비스 설정 →, 업데이트 → ┐
//	┌ 로그 → ┐

import (
	"fmt"
	"path/filepath"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// quickCommand is one command of the 바로 실행 card. Destructive ones ask
// in the card before they run, since they act on this very PC.
type quickCommand struct {
	name, labelKey, icon string
	destructive          bool
}

// quickTiles are the four big buttons; quickMore the rest of today's
// command tab, behind 더 보기.
var (
	quickTiles = []quickCommand{
		{"turnscreenoff", "cmd.screenoff", "screenoff", false},
		{"suspend", "cmd.suspend", "sleep", true},
		{"restart", "cmd.restart", "restart", true},
		{"shutdown", "cmd.shutdown", "shutdown", true},
	}
	quickMore = []quickCommand{
		{"lock", "cmd.lock", "", false},
		{"turnscreenon", "cmd.screenon", "", false},
		{"ping", "cmd.ping", "", false},
		{"hibernate", "cmd.hibernate", "", true},
		{"forceshutdown", "cmd.forceshutdown", "", true},
	}
)

// scheduleQuick is how many delays show before 더 보기 opens all of them.
const scheduleQuick = 6

// homeState is the home screen's widgets. Window goroutine only.
type homeState struct {
	secret  secretField
	loginGo widget.Clickable

	tiles     [4]widget.Clickable
	more      expander
	moreChips chipGroup
	armed     *quickCommand // a destructive command waiting for 실행
	confirm   widget.Clickable
	cancel    widget.Clickable

	awakeSw    widget.Bool
	awakeChips chipGroup
	awakeSel   int // index into gui.AwakePresets(); -1 until known

	prev, play, next, mute widget.Clickable
	vol                    widget.Float
	volPending             bool

	schedCmds     chipGroup
	schedCmdSel   int
	schedDelays   chipGroup
	schedDelaySel int
	schedAll      bool
	schedMore     widget.Clickable
	schedStart    widget.Clickable
	schedCancel   widget.Clickable

	presetRun []widget.Clickable
	presetAdd widget.Clickable

	stRow, tgRow, shareRow widget.Clickable

	themeChips    chipGroup
	langChips     chipGroup
	autostart     widget.Bool
	serviceRow    widget.Clickable
	serviceCtlRow widget.Clickable
	unreachOpen   widget.Clickable
	updateRow     widget.Clickable
	logsRow       widget.Clickable
}

func (h *homeState) init() {
	h.awakeSel = -1
	h.schedCmdSel = 0
	h.schedDelaySel = indexOf(gui.SchedulePresets(), gui.DefaultSchedulePreset)
	h.autostart.Value = gui.AutostartEnabled()
}

func indexOf(xs []int, x int) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

// homeItems are the home screen's cards for s, top to bottom, for cols
// columns. In one column they are today's stack: the 연결 heading over three
// cards, and the log card under the settings. In the grid the header and a
// login or service notice span the row; the three connections become one
// card, and the log row moves into the settings card, so every cell is a
// full card.
func (u *UI) homeItems(s snapshot, cols int) []gridItem {
	full := func(w layout.Widget) gridItem { return gridItem{span: true, w: w} }
	cell := func(w layout.Widget) gridItem { return gridItem{span: cols == 1, w: w} }
	items := []gridItem{full(func(gtx C) D { return u.header(gtx, s) })}
	switch s.conn {
	case connLogin:
		items = append(items, full(func(gtx C) D { return u.loginCard(gtx) }))
	case connLost:
		items = append(items, full(func(gtx C) D { return u.unreachableCard(gtx, s) }))
	}
	if s.conn == connOK || s.conn == connPending {
		items = append(items,
			cell(func(gtx C) D { return u.quickCard(gtx, s) }),
			cell(func(gtx C) D { return u.mediaCard(gtx, s) }),
			cell(func(gtx C) D { return u.scheduleCard(gtx, s) }),
			cell(func(gtx C) D { return u.presetsCard(gtx, s) }),
		)
		items = append(items, cell(func(gtx C) D { return u.connectionsCard(gtx, s) }))
	}
	items = append(items, cell(func(gtx C) D { return u.settingsCard(gtx, s, cols > 1) }))
	if cols == 1 {
		items = append(items, full(func(gtx C) D {
			return u.connCard(gtx, &u.home.logsRow, u.t("tab.logs"), u.t("home.logs.sub"), pageLogs)
		}))
	}
	return items
}

// homeInset is the margin around section i of n in the single column.
func homeInset(i, n int) layout.Inset {
	in := layout.Inset{Left: gapM, Right: gapM, Top: gapXS, Bottom: gapXS}
	if i == 0 {
		in.Top = gapL
	}
	if i == n-1 {
		in.Bottom = gapL
	}
	return in
}

// homeCols is the column count for the space gtx offers.
func homeCols(gtx C) int {
	return columnsFor(float32(gtx.Constraints.Max.X) / gtx.Metric.PxPerDp)
}

func (u *UI) layoutHome(gtx C, s snapshot) D {
	cols := homeCols(gtx)
	items := u.homeItems(s, cols)
	if cols == 1 {
		return material.List(u.th, &u.homeList).Layout(gtx, len(items), func(gtx C, i int) D {
			return homeInset(i, len(items)).Layout(gtx, items[i].w)
		})
	}
	return material.List(u.th, &u.homeList).Layout(gtx, 1, func(gtx C, _ int) D {
		return u.homeGrid(gtx, items, cols)
	})
}

// homeGrid is the wide home screen: the masonry grid, centred and capped.
func (u *UI) homeGrid(gtx C, items []gridItem, cols int) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.UniformInset(gapL).Layout(gtx, func(gtx C) D {
		return centered(gtx, maxContentW, func(gtx C) D { return masonryGrid(gtx, cols, gapM, items) })
	})
}

// homeColumn is the home screen unscrolled, as tall as its content (the
// screenshot test).
func (u *UI) homeColumn(gtx C, s snapshot) D {
	cols := homeCols(gtx)
	items := u.homeItems(s, cols)
	if cols > 1 {
		return u.homeGrid(gtx, items, cols)
	}
	children := make([]layout.FlexChild, len(items))
	for i, it := range items {
		children[i] = layout.Rigid(func(gtx C) D { return homeInset(i, len(items)).Layout(gtx, it.w) })
	}
	return column(gtx, children...)
}

// connectionsCard is the 연결 card: SmartThings, 텔레그램 and 공유, each an
// icon tile, its one-line summary and a status pill, opening its page.
func (u *UI) connectionsCard(gtx C, s snapshot) D {
	h := &u.home
	stHero, stPill := u.stStatus(s)
	tgHero, tgPill := u.tgStatus(s)
	shHero, shPill := u.shareStatus(s)
	conns := []struct {
		clk        *widget.Clickable
		page       pageID
		title, sub string
		hero       hero
		status     string
	}{
		{&h.stRow, pageSmartThings, u.t("tab.smartthings"), u.stLine(s), stHero, stPill},
		{&h.tgRow, pageTelegram, u.t("tab.telegram"), u.tgLine(s), tgHero, tgPill},
		{&h.shareRow, pageSharing, u.t("tab.share"), u.shareLine(s), shHero, shPill},
	}
	ws := make([]layout.Widget, len(conns))
	for i, c := range conns {
		if c.clk.Clicked(gtx) {
			u.open(c.page)
		}
		iconTone := toneAccent
		if c.hero.tone == toneOff {
			iconTone = toneOff
		}
		ws[i] = func(gtx C) D {
			return connRow(gtx, u.th, c.clk, c.hero.icon, iconTone, c.title, c.sub, c.status, c.hero.tone)
		}
	}
	return card(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("home.connections"), nil) }),
			vspace(gapS),
			layout.Rigid(func(gtx C) D { return rows(gtx, ws...) }),
		)
	})
}

// --- header and connection ---

// statusText is the connection line (also the tray's status entry).
func (u *UI) statusText(s snapshot) string {
	switch s.conn {
	case connOK:
		return u.t("status.connected")
	case connLost:
		return fmt.Sprintf(u.t("status.unreachable"), s.port)
	case connLogin:
		return u.t("login.required")
	}
	return u.t("status.connecting")
}

func (u *UI) header(gtx C, s snapshot) D {
	dot := colFaint
	switch s.conn {
	case connOK:
		dot = colOK
	case connLost:
		dot = colDanger
	case connLogin:
		dot = colWarn
	}
	battery := gui.BatteryText(u.language(), s.battery)
	return layout.Inset{Left: 4, Right: 4, Bottom: gapS}.Layout(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D {
				return row(gtx,
					layout.Flexed(1, line(u.th, headSize, windowTitle, colText, font.Bold).Layout),
					layout.Rigid(func(gtx C) D {
						if battery == "" {
							return D{}
						}
						return layout.Inset{Left: 8}.Layout(gtx, pill(u.th, battery, colSub, colCard))
					}),
				)
			}),
			vspace(6),
			layout.Rigid(func(gtx C) D {
				return row(gtx,
					layout.Rigid(func(gtx C) D { return statusDot(gtx, dot, 8) }),
					hspace(8),
					layout.Flexed(1, line(u.th, textM, u.statusText(s), colSub, font.Medium).Layout),
				)
			}),
		)
	})
}

// loginCard asks for the secret in place of the old login dialog.
func (u *UI) loginCard(gtx C) D {
	h := &u.home
	if h.loginGo.Clicked(gtx) {
		secret := h.secret.ed.Text()
		if secret == "" {
			u.toast.show(u.t("login.empty"), true, gtx.Now)
		} else {
			u.act(func() error { return u.api.Login(secret) }, "", u.connect)
		}
	}
	return card(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("login.title"), nil) }),
			vspace(4),
			layout.Rigid(label(u.th, textM, u.t("login.required"), colSub, font.Normal).Layout),
			vspace(gapS),
			layout.Rigid(func(gtx C) D { return h.secret.Layout(gtx, u.th, u.t("login.secret")) }),
			vspace(gapS),
			layout.Rigid(fill(primaryButton(u.th, &h.loginGo, u.t("login.ok")).Layout)),
		)
	})
}

func (u *UI) unreachableCard(gtx C, s snapshot) D {
	if u.home.unreachOpen.Clicked(gtx) {
		u.open(pageServiceCtl)
	}
	return card(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("svc.section"), nil) }),
			vspace(4),
			layout.Rigid(label(u.th, textM, fmt.Sprintf(u.t("status.unreachable"), s.port), colSub, font.Normal).Layout),
			vspace(gapS),
			layout.Rigid(fill(secondaryButton(u.th, &u.home.unreachOpen, u.t("ui.home.unreachable.open")).Layout)),
		)
	})
}

// --- 바로 실행 ---

// runCommand sends a command right away.
func (u *UI) runCommand(c quickCommand) {
	label := u.t(c.labelKey)
	u.act(func() error { _, err := u.api.TestCommand(c.name); return err },
		fmt.Sprintf(u.t("cmd.sent"), label), nil)
}

// pick runs a safe command, or arms a destructive one for the in-card
// confirmation.
func (u *UI) pick(c *quickCommand) {
	if c.destructive {
		u.home.armed = c
		return
	}
	u.runCommand(*c)
}

func (u *UI) quickCard(gtx C, s snapshot) D {
	h := &u.home
	for i := range quickTiles {
		if h.tiles[i].Clicked(gtx) {
			u.pick(&quickTiles[i])
		}
	}
	if i := h.moreChips.Clicked(gtx, len(quickMore)); i >= 0 {
		u.pick(&quickMore[i])
	}
	if h.cancel.Clicked(gtx) {
		h.armed = nil
	}
	if h.confirm.Clicked(gtx) && h.armed != nil {
		u.runCommand(*h.armed)
		h.armed = nil
	}
	return card(gtx, func(gtx C) D {
		tiles := make([]layout.Widget, len(quickTiles))
		for i, c := range quickTiles {
			fg, bg := colAccent, colAccentDim
			if c.destructive {
				fg, bg = colSub, colGhost
			}
			if c.name == "shutdown" {
				fg, bg = colDanger, colDangerDim
			}
			tiles[i] = func(gtx C) D { return tile(gtx, u.th, &h.tiles[i], c.icon, u.t(c.labelKey), fg, bg) }
		}
		return column(gtx,
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("home.quick"), nil) }),
			vspace(gapM),
			layout.Rigid(func(gtx C) D { return grid(gtx, 8, tiles...) }),
			layout.Rigid(func(gtx C) D {
				if h.armed == nil {
					return D{}
				}
				return layout.Inset{Top: gapM}.Layout(gtx, u.confirmPanel)
			}),
			vspace(gapS),
			layout.Rigid(func(gtx C) D {
				return h.more.Layout(gtx, u.th, u.t("home.more"), func(gtx C) D {
					labels := make([]string, len(quickMore))
					for i, c := range quickMore {
						labels[i] = u.t(c.labelKey)
					}
					return h.moreChips.Layout(gtx, u.th, labels, -1)
				})
			}),
			vspace(gapS),
			layout.Rigid(divider),
			vspace(gapM),
			layout.Rigid(func(gtx C) D { return u.awakeSection(gtx, s) }),
		)
	})
}

// confirmPanel asks before a destructive command, inside the card.
func (u *UI) confirmPanel(gtx C) D {
	h := &u.home
	return surface(gtx, colDangerDim, radiusM, func(gtx C) D {
		return layout.UniformInset(gapM).Layout(gtx, func(gtx C) D {
			return column(gtx,
				layout.Rigid(label(u.th, textL, fmt.Sprintf(u.t("cmd.confirm.body"), u.t(h.armed.labelKey)), colText, font.Medium).Layout),
				vspace(4),
				layout.Rigid(label(u.th, textS, u.t("home.immediate"), colSub, font.Normal).Layout),
				vspace(gapS),
				layout.Rigid(func(gtx C) D {
					return grid(gtx, 8,
						fill(greyButton(u.th, &h.cancel, u.t("login.cancel")).Layout),
						fill(dangerButton(u.th, &h.confirm, u.t("home.run")).Layout),
					)
				}),
			)
		})
	})
}

// awakeSection is the keep-awake switch and its durations (#111).
func (u *UI) awakeSection(gtx C, s snapshot) D {
	h := &u.home
	l := u.language()
	presets := gui.AwakePresets()
	if h.awakeSel < 0 && s.awakeSet {
		// Follow awake.default_minutes once.
		h.awakeSel = indexOf(presets, s.awake.DefaultMinutes)
	}
	if h.awakeSel < 0 {
		h.awakeSel = indexOf(presets, gui.DefaultAwakePreset)
	}
	unsupported := gui.IsAwakeUnsupported(s.awakeErr)
	if h.awakeSw.Update(gtx) && !unsupported {
		u.sendAwake(h.awakeSw.Value, presets[h.awakeSel])
	}
	if i := h.awakeChips.Clicked(gtx, len(presets)); i >= 0 && i != h.awakeSel {
		h.awakeSel = i
		if s.awake.On {
			u.sendAwake(true, presets[i]) // a new period from now
		}
	}
	h.awakeSw.Value = s.awake.On
	status := gui.AwakeStatusText(l, s.awake, time.Now())
	if unsupported {
		status = u.t("awake.unsupported")
	}
	labels := make([]string, len(presets))
	for i, m := range presets {
		labels[i] = gui.AwakePresetLabel(l, m)
	}
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D {
					return iconBadge(gtx, "awake", colWarn, mix(colCard, colWarn, 0.14))
				}),
				hspace(gapS),
				layout.Flexed(1, titled(u.th, u.t("awake.toggle"), status)),
				hspace(gapS),
				layout.Rigid(toggle(u.th, &h.awakeSw, u.t("awake.toggle")).Layout),
			)
		}),
		vspace(gapS),
		layout.Rigid(func(gtx C) D { return h.awakeChips.Layout(gtx, u.th, labels, h.awakeSel) }),
	)
}

func (u *UI) sendAwake(on bool, minutes int) {
	u.update(func(s *snapshot) { s.awake.On = on }) // the switch follows at once
	u.act(func() error {
		var a gui.Awake
		var err error
		if on {
			a, err = u.api.SetAwake(minutes)
		} else {
			a, err = u.api.AwakeOff()
		}
		if err == nil {
			u.update(func(s *snapshot) { s.awake, s.awakeSet = a, true })
		}
		return err
	}, "", func() { u.loadAwake() })
}

// --- 미디어 ---

func (u *UI) mediaCard(gtx C, s snapshot) D {
	h := &u.home
	l := u.language()
	m := s.media
	reason := gui.MediaReason(l, m, s.mediaErr)
	usable := reason == "" && s.mediaErr == nil && s.mediaSet
	if usable {
		if h.prev.Clicked(gtx) {
			u.mediaCommand("prev", nil)
		}
		if h.play.Clicked(gtx) {
			u.mediaCommand(gui.MediaToggleCommand(m.Media.Status), nil)
		}
		if h.next.Clicked(gtx) {
			u.mediaCommand("next", nil)
		}
		if h.mute.Clicked(gtx) {
			cmd := "mute"
			if m.Audio.Muted != nil && *m.Audio.Muted {
				cmd = "unmute"
			}
			u.mediaCommand(cmd, nil)
		}
		if h.vol.Update(gtx) {
			h.volPending = true
		}
		if h.volPending && !h.vol.Dragging() {
			h.volPending = false
			level := int(h.vol.Value*100 + 0.5)
			u.mediaCommand("volume", &level)
		}
	}
	if !h.volPending && !h.vol.Dragging() && m.Audio.Volume != nil {
		h.vol.Value = float32(*m.Audio.Volume) / 100
	}
	title, sub := gui.MediaLines(l, m.Media, m.NowPlaying)
	playIcon := "play"
	if m.Media.Status == "playing" {
		playIcon = "pause"
	}
	muteIcon := "volume"
	if m.Audio.Muted != nil && *m.Audio.Muted {
		muteIcon = "muted"
	}
	fg := colText
	if !usable {
		fg = colFaint
	}
	return card(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("media.title"), nil) }),
			vspace(gapS),
			layout.Rigid(line(u.th, textL, title, colText, font.SemiBold).Layout),
		}
		if sub != "" {
			children = append(children, vspace(2), layout.Rigid(line(u.th, textM, sub, colSub, font.Normal).Layout))
		}
		children = append(children,
			vspace(gapM),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle, Spacing: layout.SpaceSides}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return iconButton(gtx, &h.prev, "prev", 44, fg, colGhost) }),
					hspace(gapM),
					layout.Rigid(func(gtx C) D {
						bg, c := colAccent, colOnAccent
						if !usable {
							bg, c = colGhost, colFaint
						}
						return iconButton(gtx, &h.play, playIcon, 56, c, bg)
					}),
					hspace(gapM),
					layout.Rigid(func(gtx C) D { return iconButton(gtx, &h.next, "next", 44, fg, colGhost) }),
				)
			}),
			vspace(gapM),
			layout.Rigid(func(gtx C) D {
				return row(gtx,
					layout.Rigid(func(gtx C) D { return iconButton(gtx, &h.mute, muteIcon, 32, colSub, colCard) }),
					hspace(4),
					layout.Flexed(1, func(gtx C) D {
						sl := material.Slider(u.th, &h.vol)
						sl.Color = colAccent
						if !usable {
							sl.Color = colFaint
							gtx = gtx.Disabled()
						}
						return sl.Layout(gtx)
					}),
					hspace(8),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(40)
						return label(u.th, textM, gui.MediaLevelText(m.Audio), colSub, font.Medium).Layout(gtx)
					}),
				)
			}),
		)
		if m.Audio.Device != nil && *m.Audio.Device != "" {
			children = append(children, layout.Rigid(line(u.th, textS, *m.Audio.Device, colSub, font.Normal).Layout))
		}
		if reason != "" {
			children = append(children, vspace(gapS), layout.Rigid(label(u.th, textS, reason, colWarn, font.Normal).Layout))
		}
		return column(gtx, children...)
	})
}

func (u *UI) mediaCommand(cmd string, value *int) {
	u.act(func() error {
		m, err := u.api.MediaCommand(cmd, value)
		if err == nil {
			u.update(func(s *snapshot) { s.media, s.mediaErr, s.mediaSet = m, nil, true })
		}
		return err
	}, "", nil)
}

// --- 예약 ---

// scheduleLine is the countdown sentence ("'종료' 실행까지 05:30 남음"),
// or "" without a schedule.
func (u *UI) scheduleLine(s snapshot) string {
	if !s.sched.Active {
		return ""
	}
	l := u.language()
	key := "schedule.countdown"
	if s.sched.IsRemote() {
		key = "schedule.countdown.remote"
	}
	return fmt.Sprintf(u.t(key), gui.CommandLabel(l, s.sched.Command), gui.FormatCountdown(l, s.remaining(time.Now())))
}

func (u *UI) scheduleCard(gtx C, s snapshot) D {
	if s.sched.Active {
		return u.scheduleActive(gtx, s)
	}
	h := &u.home
	l := u.language()
	cmds := gui.ScheduleCommands()
	delays := gui.SchedulePresets()
	if i := h.schedCmds.Clicked(gtx, len(cmds)); i >= 0 {
		h.schedCmdSel = i
	}
	shown := delays
	if !h.schedAll {
		shown = delays[:scheduleQuick]
	}
	if i := h.schedDelays.Clicked(gtx, len(shown)); i >= 0 {
		h.schedDelaySel = i
	}
	if h.schedMore.Clicked(gtx) {
		h.schedAll = !h.schedAll
	}
	if h.schedStart.Clicked(gtx) && h.schedDelaySel >= 0 {
		cmd, minutes := cmds[h.schedCmdSel].Name, delays[h.schedDelaySel]
		u.act(func() error { return u.api.SetSchedule(cmd, minutes) }, "", u.loadSchedule)
	}
	cmdLabels := make([]string, len(cmds))
	for i, c := range cmds {
		cmdLabels[i] = u.t(c.LabelKey)
	}
	delayLabels := make([]string, len(shown))
	for i, m := range shown {
		delayLabels[i] = gui.FormatMinutes(l, m)
	}
	sel := h.schedDelaySel
	if sel >= len(shown) {
		sel = -1 // picked from the full list, now folded away
	}
	moreTxt := u.t("home.more")
	if h.schedAll {
		moreTxt = u.t("home.less")
	}
	return card(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D {
				return cardTitle(gtx, u.th, u.t("tab.schedule"), label(u.th, textM, u.t("schedule.none"), colSub, font.Normal).Layout)
			}),
			vspace(gapM),
			fieldLabel(u.th, u.t("schedule.command")),
			layout.Rigid(func(gtx C) D { return h.schedCmds.Layout(gtx, u.th, cmdLabels, h.schedCmdSel) }),
			vspace(gapM),
			fieldLabel(u.th, u.t("schedule.delay")),
			layout.Rigid(func(gtx C) D { return h.schedDelays.Layout(gtx, u.th, delayLabels, sel) }),
			vspace(gapXS),
			layout.Rigid(smallButton(u.th, &h.schedMore, moreTxt, colAccent).Layout),
			vspace(gapM),
			layout.Rigid(fill(primaryButton(u.th, &h.schedStart, u.t("schedule.start")).Layout)),
		)
	})
}

func (u *UI) scheduleActive(gtx C, s snapshot) D {
	h := &u.home
	l := u.language()
	if h.schedCancel.Clicked(gtx) {
		u.act(func() error { return u.api.CancelSchedule("app") }, "", u.loadSchedule)
	}
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second)}) // the countdown ticks
	left := s.remaining(gtx.Now)
	origin, originCol := u.t("schedule.origin.ui"), colSub
	switch s.sched.Origin {
	case "remote":
		origin, originCol = u.t("schedule.origin.remote"), colWarn
	case "telegram":
		origin = u.t("schedule.origin.telegram")
	case "smartthings":
		origin = u.t("schedule.origin.smartthings")
	}
	return card(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("tab.schedule"), nil) }),
			vspace(gapM),
			layout.Rigid(label(u.th, 36, gui.FormatCountdown(l, left), colText, font.Bold).Layout),
			vspace(2),
			layout.Rigid(label(u.th, textL, fmt.Sprintf(u.t("home.schedule.until"), gui.CommandLabel(l, s.sched.Command)), colText, font.Medium).Layout),
			vspace(2),
			layout.Rigid(label(u.th, textS, origin, originCol, font.Normal).Layout),
			vspace(gapM),
			layout.Rigid(fill(greyButton(u.th, &h.schedCancel, u.t("schedule.cancel")).Layout)),
		)
	})
}

// --- 프리셋 ---

func (u *UI) presetsCard(gtx C, s snapshot) D {
	h := &u.home
	presets := s.cfg.Presets
	if len(h.presetRun) < len(presets) {
		h.presetRun = make([]widget.Clickable, len(presets))
	}
	for i, p := range presets {
		if h.presetRun[i].Clicked(gtx) {
			slot, name := p.Slot, p.Name
			u.act(func() error { return u.api.RunPreset(slot) }, fmt.Sprintf(u.t("cmd.presets.started"), name), nil)
		}
	}
	if h.presetAdd.Clicked(gtx) {
		u.open(pagePresets)
	}
	return card(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D {
				return cardTitle(gtx, u.th, u.t("tab.presets"), smallButton(u.th, &h.presetAdd, "+ "+u.t("home.add"), colAccent).Layout)
			}),
			vspace(gapS),
		}
		if len(presets) == 0 {
			children = append(children, layout.Rigid(label(u.th, textM, u.t("home.presets.empty"), colSub, font.Normal).Layout))
		}
		for i, p := range presets {
			if i > 0 {
				children = append(children, vspace(gapXS))
			}
			children = append(children, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D { return slotBadge(gtx, u.th, p.Slot) }),
						hspace(gapS),
						layout.Flexed(1, func(gtx C) D {
							return column(gtx,
								layout.Rigid(line(u.th, textL, p.Name, colText, font.Medium).Layout),
								vspace(2),
								layout.Rigid(line(u.th, textS, u.presetLine(p), colSub, font.Normal).Layout),
							)
						}),
						hspace(gapS),
						layout.Rigid(func(gtx C) D {
							b := smallButton(u.th, &h.presetRun[i], u.t("home.run"), colAccent)
							b.Background = colAccentDim
							return b.Layout(gtx)
						}),
					)
				})
			}))
		}
		return column(gtx, children...)
	})
}

// presetLine is a preset's grey line: its kind and target.
func (u *UI) presetLine(p gui.Preset) string {
	target := p.Path
	if p.Type != "url" {
		target = filepath.Base(p.Path)
	}
	return u.t("presets.type."+p.Type) + " · " + target
}

// slotBadge is the preset's number in a tinted rounded square.
func slotBadge(gtx C, th *material.Theme, slot int) D {
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx C) D { return iconBadgeBg(gtx, colAccentDim) }),
		layout.Stacked(func(gtx C) D {
			s := gtx.Dp(36)
			gtx.Constraints = layout.Exact(imagePt(s))
			return layout.Center.Layout(gtx, label(th, textL, fmt.Sprint(slot), colAccent, font.Bold).Layout)
		}),
	)
}

// --- 연결 ---

// connCard is one compact card that opens a sub-page.
func (u *UI) connCard(gtx C, clk *widget.Clickable, title, sub string, page pageID) D {
	if clk.Clicked(gtx) {
		u.open(page)
	}
	return surface(gtx, colCard, radiusL, func(gtx C) D {
		return layout.Inset{Top: gapM, Bottom: gapM, Left: gapL, Right: gapM}.Layout(gtx, func(gtx C) D {
			return listRow(gtx, u.th, clk, title, sub, "")
		})
	})
}

func (u *UI) stLine(s snapshot) string {
	switch {
	case !s.hubSet:
		return u.t("st.hub.loading")
	case s.hub.Connected:
		return fmt.Sprintf(u.t("home.st.connected"), s.hub.IP)
	}
	return u.t("home.st.none")
}

func (u *UI) tgLine(s snapshot) string {
	tg := s.cfg.Telegram
	switch {
	case !tg.Enabled && !tg.ControlEnabled:
		return u.t("home.off")
	case tg.Enabled && tg.ControlEnabled:
		return u.t("home.tg.both")
	case tg.ControlEnabled:
		return u.t("home.tg.control")
	}
	return u.t("home.tg.notify")
}

func (u *UI) shareLine(s snapshot) string {
	n := s.shareCount()
	if n == 0 {
		return u.t("home.share.none")
	}
	return fmt.Sprintf(u.t("home.share.some"), n)
}

// --- 설정 ---

// settingsCard is the 설정 card; withLogs adds the log row (the grid).
func (u *UI) settingsCard(gtx C, s snapshot, withLogs bool) D {
	h := &u.home
	if withLogs && h.logsRow.Clicked(gtx) {
		u.open(pageLogs)
	}
	if i := h.themeChips.Clicked(gtx, len(themeValues)); i >= 0 {
		u.prefs.Set(prefTheme, themeValues[i])
	}
	langs := gui.Languages()
	if i := h.langChips.Clicked(gtx, len(langs)); i >= 0 && langs[i] != u.language() {
		u.setLanguage(langs[i])
	}
	if h.autostart.Update(gtx) {
		on := h.autostart.Value
		u.prefs.Set(prefAutostart, on)
		u.act(func() error { return gui.SetAutostart(on) }, "", nil)
	}
	if h.serviceRow.Clicked(gtx) {
		u.open(pageService)
	}
	if h.serviceCtlRow.Clicked(gtx) {
		u.open(pageServiceCtl)
	}
	if h.updateRow.Clicked(gtx) {
		u.open(pageUpdate)
	}
	updateValue := u.version
	if s.updateTag != "" {
		updateValue = ""
	}
	themeLabels := []string{u.t("theme.system"), u.t("theme.light"), u.t("theme.dark")}
	themeSel := indexOfString(themeValues, u.prefs.String(prefTheme, ""))
	langLabels := make([]string, len(langs))
	for i, l := range langs {
		langLabels[i] = gui.T(l, "lang.name")
	}
	langSel := -1
	for i, l := range langs {
		if l == u.language() {
			langSel = i
		}
	}
	port := ""
	if s.cfgSet {
		port = fmt.Sprintf(u.t("home.port"), s.cfg.Port)
	}
	return card(gtx, func(gtx C) D {
		return column(gtx,
			layout.Rigid(func(gtx C) D { return cardTitle(gtx, u.th, u.t("tab.settings"), nil) }),
			vspace(gapM),
			layout.Rigid(func(gtx C) D {
				return settingRow(gtx, u.th, u.t("theme.title"), "", func(gtx C) D {
					return h.themeChips.Layout(gtx, u.th, themeLabels, themeSel)
				})
			}),
			vspace(gapS),
			layout.Rigid(func(gtx C) D {
				return settingRow(gtx, u.th, u.t("lang.title"), "", func(gtx C) D {
					return h.langChips.Layout(gtx, u.th, langLabels, langSel)
				})
			}),
			vspace(gapM),
			layout.Rigid(func(gtx C) D { return toggleRow(gtx, u.th, &h.autostart, u.t("autostart.check"), "") }),
			vspace(gapM),
			layout.Rigid(divider),
			vspace(gapS),
			layout.Rigid(func(gtx C) D {
				return listRow(gtx, u.th, &h.serviceRow, u.t("settings.service"), "", port)
			}),
			vspace(4),
			layout.Rigid(func(gtx C) D {
				return listRow(gtx, u.th, &h.serviceCtlRow, u.t("svc.section"), u.t("ui.home.servicectl.sub"), "")
			}),
			vspace(4),
			layout.Rigid(func(gtx C) D {
				if s.updateTag == "" {
					return listRow(gtx, u.th, &h.updateRow, u.t("update.manual"), "", updateValue)
				}
				return listRowBadge(gtx, u.th, &h.updateRow, u.t("update.manual"), u.version,
					statusPill(u.th, fmt.Sprintf(u.t("ui.home.update.new"), s.updateTag), toneAccent))
			}),
			layout.Rigid(func(gtx C) D {
				if !withLogs {
					return D{}
				}
				return layout.Inset{Top: 4}.Layout(gtx, func(gtx C) D {
					return listRow(gtx, u.th, &h.logsRow, u.t("tab.logs"), "", "")
				})
			}),
		)
	})
}

func indexOfString(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
