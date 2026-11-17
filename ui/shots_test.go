package ui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShots draws the window with sample data to PNGs, light and dark, for
// a look without starting the app. It needs a GPU (Direct3D 11 offscreen),
// so it only runs when asked:
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShots ./ui/
func TestShots(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []gui.Lang{gui.LangKo, gui.LangEn} {
		for _, dark := range []bool{false, true} {
			name := string(lang)
			if dark {
				name += "-dark"
			}
			u := sampleUI(lang, dark)
			// the whole home screen, as tall as its content
			shot(t, u, dir, "home-"+name, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
			// what the window shows at its default size
			shot(t, u, dir, "window-"+name, winH, u.layout)

			// the schedule card picking a clock time tomorrow, wide
			u.home.schedPick = gui.SchedulePick{At: true, Day: 1, Hour: 7, Minute: 30}
			shotW(t, dir, "home-at-1200-"+name, 1200, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
			u.home.schedPick = gui.SchedulePick{Hours: 1, Mins: 30}
			shotW(t, dir, "home-after-1200-"+name, 1200, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
			u.home.schedPick = gui.SchedulePick{At: true, Hour: 0}
			shotW(t, dir, "home-past-1200-"+name, 1200, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
			u.home.schedPick = gui.DefaultSchedulePick(time.Now())

			// a running schedule and an armed destructive command
			u.update(func(s *snapshot) {
				s.sched = gui.Schedule{Active: true, Command: "shutdown", RemainingSec: 1530, Origin: "remote"}
				s.schedAt = time.Now()
			})
			u.home.armed = &quickTiles[3]
			u.home.more.open = true
			shot(t, u, dir, "home-active-"+name, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })

			// the service settings page with an unsaved change: the save bar
			u.home.armed = nil
			u.nav.push(u, u.pages[pageService].(*servicePage))
			u.pages[pageService].(*servicePage).secret.ed.SetText("hunter22")
			u.pages[pageService].(*servicePage).port.SetText("5011")
			shot(t, u, dir, "service-"+name, winH, u.layout)
			u.nav.stack = nil

			u.nav.push(u, u.pages[pageSmartThings])
			shot(t, u, dir, "smartthings-"+name, winH, u.layout)
			u.nav.stack = nil
		}
	}
	usePalette(false)
}

// TestShotsResponsive draws the home screen narrow, medium and wide (one,
// two and three columns) and a sub-page wide, light and dark.
func TestShotsResponsive(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		suffix := ""
		if dark {
			suffix = "-dark"
		}
		u := sampleUI(gui.LangKo, dark)
		for _, w := range []int{420, 800, 1200} {
			shotW(t, dir, fmt.Sprintf("home-%d%s", w, suffix), w, 0, func(gtx C) D { return u.homeColumn(gtx, u.current()) })
		}
		shotW(t, dir, "window-1200"+suffix, 1200, 800, u.layout)
		u.nav.push(u, u.pages[pageService].(*servicePage))
		u.pages[pageService].(*servicePage).port.SetText("5011")
		shotW(t, dir, "service-1200"+suffix, 1200, 800, u.layout)
		// the tray asked to quit, over a page with an unsaved change
		u.quitAsk.Store(true)
		shotW(t, dir, "quit"+suffix, winW, winH, u.layout)
		u.quitAsk.Store(false)
		u.nav.stack = nil

		// the English home screen wide, for README.en.md
		en := sampleUI(gui.LangEn, dark)
		shotW(t, dir, "home-1200-en"+suffix, 1200, 0, func(gtx C) D { return en.homeColumn(gtx, en.current()) })
	}
	usePalette(false)
}

// sampleUI is a connected UI with believable data and no service behind it.
func sampleUI(lang gui.Lang, dark bool) *UI {
	p := loadPrefs("")
	p.Set(prefLang, string(lang))
	if dark {
		p.Set(prefTheme, "dark")
	}
	u := New(fakeAPI{}, "v1.2.4", p, func() {})
	u.th = newTheme()
	u.setDark(dark)
	vol, muted, dev := 42, false, "스피커 (Realtek(R) Audio)"
	u.snap = snapshot{
		conn: connOK, port: 5002,
		cfg: gui.Config{
			Port: 5001,
			Presets: []gui.Preset{
				{Slot: 1, Name: "게임 모드", Type: "script", Path: `C:\Users\me\Documents\game-mode.ps1`},
				{Slot: 2, Name: "유튜브 뮤직", Type: "url", Path: "https://music.youtube.com"},
				{Slot: 3, Name: "메모장", Type: "program", Path: `C:\Windows\notepad.exe`},
			},
			Telegram:    gui.TelegramConfig{Enabled: true, ControlEnabled: true, BotTokenSet: true, ChatID: "123456789"},
			SmartThings: gui.SmartThingsConfig{ExposeSession: true, AllowedHubs: []string{"192.168.1.20"}},
			Media:       gui.MediaConfig{Enabled: true, NowPlaying: true},
		},
		cfgSet:   true,
		battery:  gui.Battery{Present: true, Percent: 80, Charging: true},
		awake:    gui.Awake{On: true, Until: time.Now().Add(42 * time.Minute).Format(time.RFC3339), RemainingSeconds: 42 * 60, DefaultMinutes: 60},
		awakeSet: true,
		media: gui.MediaState{Enabled: true, NowPlaying: true, Session: true,
			Audio: gui.MediaAudio{Available: true, Volume: &vol, Muted: &muted, Device: &dev},
			Media: gui.MediaInfo{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", App: "Spotify"}},
		mediaSet: true,
		hub: gui.STHub{Connected: true, IP: "192.168.1.20", DriverVersion: "1.4.2", LastSeen: time.Now().Add(-8 * time.Second).Format(time.RFC3339),
			SSDP: gui.STSSDPState{Running: true, FirewallRule: true},
			WoL:  gui.STWoLInfo{Selected: &gui.STWoLSelected{Name: "이더넷", MAC: "AA:BB:CC:DD:EE:FF"}}},
		hubSet: true,
		tg:     gui.TelegramState{Polling: true},
	}
	u.snap.cfgGen = 1
	u.syncForms(u.current())
	u.home.autostart.Value = true
	return u
}

// shot lays w out at the window's width and saves it as a PNG. height 0
// means as tall as w's content.
func shot(t *testing.T, u *UI, dir, name string, heightDp int, w layout.Widget) {
	t.Helper()
	shotW(t, dir, name, winW, heightDp, w)
}

// shotW is shot at another window width.
func shotW(t *testing.T, dir, name string, widthDp, heightDp int, w layout.Widget) {
	t.Helper()
	const scale = 2.0
	width := int(float64(widthDp) * scale)
	var ops op.Ops
	var router input.Router // an enabled source, so widgets draw in their active colours
	frame := func(height int) D {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Constraints{Min: image.Pt(width, height), Max: image.Pt(width, height)},
			Now:         time.Now(),
			Source:      router.Source(),
		}
		gtx.Constraints.Min.Y = 0
		fillBg(gtx)
		return w(gtx)
	}
	h := int(float64(heightDp) * scale)
	if h == 0 {
		frame(20000) // creates widget state on first layout
		h = frame(20000).Size.Y
	}
	frame(h)
	frame(h)

	win, err := headless.NewWindow(width, h)
	if err != nil {
		t.Skipf("no offscreen GPU: %v", err)
	}
	defer win.Release()
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
