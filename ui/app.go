// Package ui is the desktop app drawn with Gio (#165): a tray icon and one
// window with a home screen of cards and a few sub-pages. It replaces the
// Fyne window in package gui, whose toolkit-free parts (the API client,
// texts, formatting) it uses.
//
// Threading follows proto-easy-mount: the tray runs on the main thread
// (systray locks it), the window runs its own event loop on a goroutine and
// is created anew each time it is shown, and the poller writes a snapshot
// under u.mu that each frame copies.
package ui

import (
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

const windowTitle = "SmartThings PC Control"

// Window size in dp: a phone-like column, never so narrow that two chips
// and a title no longer share a row.
const (
	winW, winH       = 480, 720
	winMinW, winMinH = 380, 520
)

// Background is work that lives as long as the tray, not the window: the
// user-session job runner of #164 plugs in here. Start runs once the tray is
// up, Stop when the app quits.
type Background interface {
	Start()
	Stop()
}

// UI owns the tray and at most one window at a time.
type UI struct {
	api     API
	version string
	prefs   *prefs
	quit    func() // ends the whole app, not just the window

	mu   sync.Mutex
	snap snapshot
	lang gui.Lang
	win  *app.Window

	toast    toaster
	wantLogs atomic.Bool // the logs page is open: poll the log
	sysDark  atomic.Bool // Windows' own setting, followed while the window is open

	// Window goroutine only.
	th       *material.Theme
	dark     bool // palette in use
	nav      nav
	homeList widget.List
	pageList widget.List
	back     widget.Clickable
	saveBar  saveBarState
	home     homeState
	pages    pages

	tray tray
}

// New prepares the UI; nothing is shown until Show.
func New(api API, version string, p *prefs, quit func()) *UI {
	u := &UI{api: api, version: version, prefs: p, quit: quit}
	u.lang = gui.Lang(p.String(prefLang, ""))
	if u.lang != gui.LangKo && u.lang != gui.LangEn {
		u.lang = systemLang()
	}
	u.homeList.Axis = layout.Vertical
	u.pageList.Axis = layout.Vertical
	u.pages.init()
	u.home.init()
	return u
}

func (u *UI) language() gui.Lang {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lang
}

func (u *UI) setLanguage(l gui.Lang) {
	u.mu.Lock()
	u.lang = l
	u.mu.Unlock()
	u.prefs.Set(prefLang, string(l))
	u.refreshTray()
}

// t is the text for key in the current language.
func (u *UI) t(key string) string { return gui.T(u.language(), key) }

// Show opens the window, or brings it to the front. Safe from any goroutine.
func (u *UI) Show() {
	u.mu.Lock()
	if w := u.win; w != nil {
		u.mu.Unlock()
		w.Perform(system.ActionRaise)
		return
	}
	w := new(app.Window)
	u.win = w
	u.mu.Unlock()

	w.Option(
		app.Title(windowTitle),
		app.Size(unit.Dp(winW), unit.Dp(winH)),
		app.MinSize(unit.Dp(winMinW), unit.Dp(winMinH)),
	)
	go u.loop(w)
	go u.connect() // fresh data for what is about to show
}

// onScreen reports whether the window is open.
func (u *UI) onScreen() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.win != nil
}

func (u *UI) invalidate() {
	u.mu.Lock()
	w := u.win
	u.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

func (u *UI) loop(w *app.Window) {
	if u.th == nil {
		u.th = newTheme()
	}
	w.Perform(system.ActionCenter)
	stop := make(chan struct{})
	defer close(stop)
	u.sysDark.Store(systemDark())
	go u.watchTheme(w, stop)

	var ops op.Ops
	var hwnd uintptr
	for {
		e := w.Event()
		if h, ok := windowHandle(e); ok {
			hwnd = h
			darkTitleBar(hwnd, u.dark)
		}
		switch e := e.(type) {
		case app.DestroyEvent:
			// Closing the window hides the app to the tray.
			u.mu.Lock()
			u.win = nil
			u.mu.Unlock()
			u.wantLogs.Store(false)
			return
		case app.FrameEvent:
			if d := u.wantDark(u.sysDark.Load()); d != u.dark {
				u.setDark(d)
				darkTitleBar(hwnd, d)
			}
			gtx := app.NewContext(&ops, e)
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// Theme choices: the stored value; the label key is "theme."+value or
// theme.system.
var themeValues = []string{"", "light", "dark"}

// wantDark is whether the window should be dark: the theme chosen by hand,
// or the system setting when none is.
func (u *UI) wantDark(systemDark bool) bool {
	switch u.prefs.String(prefTheme, "") {
	case "light":
		return false
	case "dark":
		return true
	}
	return systemDark
}

// setDark switches the palette. Call on the window goroutine.
func (u *UI) setDark(dark bool) {
	u.dark = dark
	usePalette(dark)
	if u.th != nil {
		setThemeColors(u.th)
	}
}

// watchTheme follows the system light/dark setting while the window is
// open, redrawing when it changes.
func (u *UI) watchTheme(w *app.Window, stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if d := systemDark(); d != u.sysDark.Load() {
				u.sysDark.Store(d)
				w.Invalidate()
			}
		}
	}
}

// layout draws one frame: the home screen or the top sub-page, and the
// toast over either.
func (u *UI) layout(gtx C) D {
	fillBg(gtx)
	s := u.current()
	if u.back.Clicked(gtx) {
		u.nav.back(u, gtx.Now)
	}
	top := u.nav.top()
	_, logsOpen := top.(*logsPage)
	u.wantLogs.Store(logsOpen)

	bottom := 0
	if top == nil {
		u.layoutHome(gtx, s)
	} else {
		bottom = u.layoutPage(gtx, top, s)
	}
	u.toast.layout(gtx, u.th, bottom+gtx.Dp(gapL))
	return D{Size: gtx.Constraints.Max}
}

// layoutPage draws a sub-page and returns the height of its save bar (0
// when there is none).
func (u *UI) layoutPage(gtx C, p screen, s snapshot) int {
	ed, isEditor := p.(editor)
	dirty := isEditor && ed.dirty(s)
	if dirty {
		if u.saveBar.save.Clicked(gtx) {
			ed.save(u, s)
		}
		if u.saveBar.discard.Clicked(gtx) {
			ed.revert(s)
		}
	}
	var barH int
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return centered(gtx, pageColumnW, func(gtx C) D { return topBar(gtx, u.th, &u.back, p.title(u)) })
		}),
		layout.Flexed(1, func(gtx C) D {
			return material.List(u.th, &u.pageList).Layout(gtx, 1, func(gtx C, _ int) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return centered(gtx, pageColumnW, func(gtx C) D {
					return layout.Inset{Left: gapM, Right: gapM, Top: 4, Bottom: gapL}.Layout(gtx, func(gtx C) D {
						return p.body(gtx, u, s)
					})
				})
			})
		}),
		layout.Rigid(func(gtx C) D {
			if !dirty {
				return D{}
			}
			d := saveBar(gtx, u.th, &u.saveBar, u.t("unsaved.indicator"), u.t("savebar.revert"), u.t("unsaved.save"))
			barH = d.Size.Y
			return d
		}),
	)
	return barH
}
