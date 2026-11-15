package ui

import (
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// logsAPI answers Logs with fixed lines, so the read the page starts when
// it opens keeps the sample.
type logsAPI struct {
	fakeAPI
	lines []string
}

func (a logsAPI) Logs() ([]string, error) { return a.lines, nil }

// sampleLogLines is a believable service log: plain lines, debug,
// warnings, errors, a panic with its stack and a long line that wraps.
func sampleLogLines(n int) []string {
	base := []string{
		"2026/11/15 09:12:%02d SmartThings PC Control service v1.2.4 starting",
		"2026/11/15 09:12:%02d Listening on port 5001 (WebUI 5002)",
		"2026/11/15 09:12:%02d [debug] SSDP: answered M-SEARCH from 192.168.1.20",
		"2026/11/15 09:12:%02d WARNING: No secret configured. Anyone on your network can control this PC.",
		"2026/11/15 09:12:%02d Hub 192.168.1.20 connected, driver v1.4.2",
		"2026/11/15 09:12:%02d Telegram: 명령 수신 /status (chat 123456789)",
		"2026/11/15 09:12:%02d SSDP: reply to 10.0.0.2 failed: i/o timeout",
		"2026/11/15 09:12:%02d Media: now playing NewJeans - Hype Boy (Spotify), volume 42%%, device 스피커 (Realtek(R) Audio), session active",
		"2026/11/15 09:12:%02d [debug] WoL: adapter 이더넷 AA:BB:CC:DD:EE:FF selected",
	}
	var out []string
	for i := 0; len(out) < n; i++ {
		out = append(out, fmt.Sprintf(base[i%len(base)], i%60))
		if i == 20 {
			out = append(out,
				"2026/11/15 09:13:00 http: panic serving 127.0.0.1:5555: runtime error: index out of range [3] with length 3",
				"goroutine 41 [running]:",
				"\tC:/dev/pc/service/webui/routes.go:123 +0x45",
			)
		}
	}
	return out[:n]
}

// TestShotsLogs draws the 로그 page: live, filtered, scrolled up with the
// pill, paused, read from disk (and failing to), empty and loading —
// light and dark, ko at 480 and 1200dp, en at 480dp.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsLogs ./ui/
func TestShotsLogs(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := sampleLogLines(300)
	diskLines := sampleLogLines(40)
	saved := logsFromDisk
	defer func() { logsFromDisk = saved }()
	for _, lang := range []gui.Lang{gui.LangKo, gui.LangEn} {
		for _, dark := range []bool{false, true} {
			sfx := string(lang)
			if dark {
				sfx += "-dark"
			}
			for _, w := range []int{480, 1200} {
				if lang == gui.LangEn && w == 1200 {
					continue
				}
				name := func(what string) string { return fmt.Sprintf("logs-%s-%d-%s", what, w, sfx) }
				winHt := winH
				if w > 480 {
					winHt = 800
				}
				u := sampleUI(lang, dark)
				u.api = logsAPI{lines: lines}
				u.update(func(s *snapshot) { s.logs, s.logsSet = lines, true })
				p := u.pages[pageLogs].(*logsPage)

				u.open(pageLogs)
				shotW(t, dir, name("live"), w, winHt, u.layout)

				// a filter and the 오류 chip
				p.filter.SetText("ssdp")
				p.level = levelError
				shotW(t, dir, name("filtered"), w, winHt, u.layout)
				p.filter.SetText("")
				p.level = levelAll
				frames(u, w, winHt, 2)
				p.filter.SetText("zzz-nothing")
				shotW(t, dir, name("nomatch"), w, winHt, u.layout)
				p.filter.SetText("")

				// scrolled up: the pill back to the newest line; then paused
				frames(u, w, winHt, 2)
				p.list.Position = layout.Position{First: 40, BeforeEnd: true}
				shotW(t, dir, name("scrolled"), w, winHt, u.layout)
				p.paused = true
				p.jump = true
				shotW(t, dir, name("paused"), w, winHt, u.layout)
				p.paused = false
				u.nav.stack = nil

				// the service is gone: service.log from disk, then unreadable
				u.update(func(s *snapshot) { s.conn, s.logs, s.logsSet = connLost, nil, false })
				note := gui.LogsFileNote(lang, `C:\Program Files\SmartThings PC Control\service.log`, nil)
				logsFromDisk = func(gui.Lang) ([]string, string, bool) { return diskLines, note, true }
				u.open(pageLogs)
				p.file, p.fileNote, p.fileOK, p.fileSet, p.fileAt = diskLines, note, true, true, time.Now()
				shotW(t, dir, name("disk"), w, winHt, u.layout)
				u.nav.stack = nil
				if w != 480 {
					continue
				}
				denied := gui.LogsFileNote(lang, `C:\Program Files\SmartThings PC Control\service.log`, os.ErrPermission)
				logsFromDisk = func(gui.Lang) ([]string, string, bool) { return nil, denied, false }
				u.open(pageLogs)
				p.file, p.fileNote, p.fileOK, p.fileSet, p.fileAt = nil, denied, false, true, time.Now()
				shotW(t, dir, name("disk-denied"), w, winHt, u.layout)
				u.nav.stack = nil

				// connected, before the first read
				u.update(func(s *snapshot) { s.conn, s.logs, s.logsSet = connOK, nil, false })
				u.api = slowLogsAPI{}
				u.open(pageLogs)
				shotW(t, dir, name("loading"), w, winHt, u.layout)
				u.nav.stack = nil
			}
		}
	}
	usePalette(false)
}

// frames lays the window out n times without drawing it, as the frames
// between two shots would.
func frames(u *UI, widthDp, heightDp, n int) {
	const scale = 2.0
	var ops op.Ops
	var router input.Router
	for range n {
		ops.Reset()
		sz := image.Pt(int(float64(widthDp)*scale), int(float64(heightDp)*scale))
		u.layout(layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(sz),
			Now:         time.Now(),
			Source:      router.Source(),
		})
	}
}

// slowLogsAPI never answers Logs while a shot is taken.
type slowLogsAPI struct{ fakeAPI }

func (slowLogsAPI) Logs() ([]string, error) { select {} }
