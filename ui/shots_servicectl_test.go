package ui

import (
	"fmt"
	"os"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsServiceCtl draws 서비스 관리 in its states: running and
// connected, running but unreachable, stopped, not installed (safe and
// risky folder), unknown, busy, a failed action and the uninstall
// question — light and dark, at 480 and 1200dp (English at 480).
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsServiceCtl ./ui/
func TestShotsServiceCtl(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
				name := func(what string) string { return fmt.Sprintf("servicectl-%s-%d-%s", what, w, sfx) }
				winHt := winH
				if w > 480 {
					winHt = 800
				}
				for _, c := range []struct {
					what  string
					state gui.ServiceState
					known bool
					dir   string
					risk  gui.InstallRisk
					conn  connState
					busy  bool
					err   string
					ask   bool
				}{
					{what: "running", state: gui.ServiceRunning, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connOK},
					{what: "unreachable", state: gui.ServiceRunning, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connLost},
					{what: "stopped", state: gui.ServiceStopped, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connLost},
					{what: "notinstalled", state: gui.ServiceNotInstalled, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connLost},
					{what: "risk", state: gui.ServiceNotInstalled, known: true, dir: `C:\Users\me\Downloads\smartthings-pc-control`, risk: gui.InstallRiskVolatile, conn: connLost},
					{what: "unknown", conn: connLost},
					{what: "busy", state: gui.ServiceStopped, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connLost, busy: true},
					{what: "error", state: gui.ServiceStopped, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connLost, err: "The operation was canceled by the user."},
					{what: "ask", state: gui.ServiceRunning, known: true, dir: `C:\Program Files\SmartThings PC Control`, conn: connOK, ask: true},
					{what: "riskask", state: gui.ServiceNotInstalled, known: true, dir: `C:\Users\me\Downloads\smartthings-pc-control`, risk: gui.InstallRiskVolatile, conn: connLost, ask: true},
				} {
					if w != 480 && (c.what == "unknown" || c.what == "busy" || c.what == "ask" || c.what == "riskask") {
						continue
					}
					u := sampleUI(lang, dark)
					u.update(func(s *snapshot) { s.conn = c.conn })
					f := &fakeSvc{state: c.state, dir: c.dir, risk: c.risk}
					p := u.pages[pageServiceCtl].(*serviceCtlPage)
					p.ops = f.ops()
					u.nav.stack = append(u.nav.stack, p) // without shown: no query
					p.lastConn = c.conn
					if c.known {
						p.setState(c.state, c.dir, c.risk)
					}
					p.busy = c.busy
					if c.err != "" {
						u.setNotice(p, noticeError, c.err)
					}
					if c.ask {
						if c.risk != gui.InstallRiskNone {
							p.askInstall(u)
						} else {
							u.ask(askSpec{title: u.t("cmd.confirm.title"), body: u.t("svc.uninstall.confirm"), ok: u.t("ui.servicectl.uninstall"), danger: true})
						}
					}
					shotW(t, dir, name(c.what), w, winHt, u.layout)
				}
			}
		}
	}
	usePalette(false)
}
