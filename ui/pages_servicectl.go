package ui

// 서비스 관리: the Windows service itself — install (after the install
// location check), start, restart, stop and uninstall — plus the WebUI,
// the crash folder and where the service runs from. What the Fyne
// settings tab's 서비스 관리 box, 도구 row and crash-folder button did.
//
// The service state comes from the SCM (cheap, no process): when the page
// opens, every few seconds while it is in front, when the connection
// changes and after each action. Install, start, stop and uninstall run
// elevated (UAC) and block their goroutine until the child exits; the page
// is busy meanwhile. Everything Windows-side goes through p.ops so the
// tests never elevate or touch the SCM.

import (
	"fmt"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// serviceOps is what the page asks of Windows; the tests replace it.
type serviceOps struct {
	state                           func() gui.ServiceState
	installRisk                     func() (dir string, risk gui.InstallRisk)
	install, start, stop, uninstall func() error
	crashDir                        func() (string, error)
	serviceCrashDir                 func() string
	openURL, openPath               func(string) error
	restart                         func(u *UI) // asks the service to restart itself
	reconnect                       func(u *UI) // after the service came up
	settle                          time.Duration
}

func realServiceOps() serviceOps {
	return serviceOps{
		state:           gui.QueryServiceState,
		installRisk:     gui.ExeInstallRisk,
		install:         gui.InstallService,
		start:           gui.StartService,
		stop:            gui.StopService,
		uninstall:       gui.UninstallService,
		crashDir:        gui.PrepareCrashDir,
		serviceCrashDir: gui.ServiceCrashDir,
		openURL:         openURL,
		openPath:        openPath,
		restart:         (*UI).restartService,
		reconnect:       func(u *UI) { go u.connect() },
		// a freshly (un)installed or started service settles before the
		// state is read again
		settle: 1500 * time.Millisecond,
	}
}

// svcRefreshEvery is how often the state is re-read while the page is in
// front.
const svcRefreshEvery = 5 * time.Second

type serviceCtlPage struct {
	ops serviceOps

	// Window goroutine only.
	state     gui.ServiceState
	known     bool // a state query has answered
	exeDir    string
	risk      gui.InstallRisk
	querying  bool
	busy      bool // an elevated action is running
	queriedAt time.Time
	lastConn  connState

	install, start, restart, stop, uninstall, webui, crash widget.Clickable
}

func newServiceCtlPage() screen { return &serviceCtlPage{ops: realServiceOps()} }

func (p *serviceCtlPage) title(u *UI) string { return u.t("svc.section") }

// shown reads the state and the install location afresh.
func (p *serviceCtlPage) shown(u *UI, s snapshot) {
	p.lastConn = s.conn
	p.refresh(u)
}

// refresh reads the service state and the install location off the window
// goroutine.
func (p *serviceCtlPage) refresh(u *UI) {
	if p.querying || p.busy {
		return
	}
	p.querying = true
	ops := p.ops
	go func() {
		st := ops.state()
		dir, risk := ops.installRisk()
		u.onWindow(func() {
			p.querying = false
			p.setState(st, dir, risk)
		})
	}()
}

func (p *serviceCtlPage) setState(st gui.ServiceState, dir string, risk gui.InstallRisk) {
	p.state, p.exeDir, p.risk, p.known = st, dir, risk, true
	p.queriedAt = time.Now()
}

// run starts an elevated action: done is the toast on success. The state
// is read again afterwards either way; a failure (a declined UAC prompt
// included) stays as the page's notice.
func (p *serviceCtlPage) run(u *UI, fn func() error, done string) {
	if p.busy {
		return
	}
	p.busy = true
	u.clearNotice(p)
	ops := p.ops
	go func() {
		err := fn()
		time.Sleep(ops.settle)
		st := ops.state()
		dir, risk := ops.installRisk()
		u.onWindow(func() {
			p.busy = false
			p.setState(st, dir, risk)
			if err != nil {
				u.setNotice(p, noticeError, err.Error())
				return
			}
			u.toast.show(done, false, time.Now())
			if st == gui.ServiceRunning {
				ops.reconnect(u)
			}
		})
	}()
}

// askInstall installs, asking first when the exe's folder is a poor place
// to run a SYSTEM service from (#126).
func (p *serviceCtlPage) askInstall(u *UI) {
	install := func() { p.run(u, p.ops.install, u.t("ui.servicectl.done.install")) }
	if p.risk == gui.InstallRiskNone {
		install()
		return
	}
	u.ask(askSpec{
		title:  u.t("svc.location.title"),
		body:   fmt.Sprintf(u.t(gui.InstallRiskBodyKey(p.risk)), p.exeDir, gui.RecommendedInstallDir),
		ok:     u.t("svc.location.anyway"),
		danger: p.risk == gui.InstallRiskUnsafe,
		onOK:   install,
	})
}

// clicks handles this frame's presses.
func (p *serviceCtlPage) clicks(gtx C, u *UI, s snapshot) {
	if p.install.Clicked(gtx) && !p.busy {
		p.askInstall(u)
	}
	if p.start.Clicked(gtx) && !p.busy {
		p.run(u, p.ops.start, u.t("ui.servicectl.done.start"))
	}
	if p.restart.Clicked(gtx) && !p.busy {
		u.ask(askSpec{title: u.t("cmd.confirm.title"), body: u.t("settings.restart.confirm"), ok: u.t("settings.restart"),
			onOK: func() { p.ops.restart(u) }})
	}
	if p.stop.Clicked(gtx) && !p.busy {
		u.ask(askSpec{title: u.t("cmd.confirm.title"), body: u.t("ui.servicectl.stop.confirm"), ok: u.t("ui.servicectl.stop"), danger: true,
			onOK: func() { p.run(u, p.ops.stop, u.t("ui.servicectl.done.stop")) }})
	}
	if p.uninstall.Clicked(gtx) && !p.busy {
		u.ask(askSpec{title: u.t("cmd.confirm.title"), body: u.t("svc.uninstall.confirm"), ok: u.t("ui.servicectl.uninstall"), danger: true,
			onOK: func() { p.run(u, p.ops.uninstall, u.t("ui.servicectl.done.uninstall")) }})
	}
	if p.webui.Clicked(gtx) {
		if err := p.ops.openURL(webUIURL(s.port)); err != nil {
			u.toast.show(err.Error(), true, gtx.Now)
		}
	}
	if p.crash.Clicked(gtx) {
		dir, err := p.ops.crashDir()
		if err == nil {
			err = p.ops.openPath(dir)
		}
		if err != nil {
			u.toast.show(err.Error(), true, gtx.Now)
		}
	}
}

func webUIURL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// hero is the service's state at a glance.
func (p *serviceCtlPage) hero(u *UI, s snapshot) hero {
	h := hero{icon: "service", tone: toneOff, headline: u.t("ui.servicectl.state.unknown")}
	if !p.known {
		return h
	}
	switch p.state {
	case gui.ServiceRunning:
		h.headline = u.t("svc.state.running")
		switch s.conn {
		case connOK:
			h.tone, h.sub = toneOK, fmt.Sprintf(u.t("ui.servicectl.sub.running"), s.port)
		case connLost:
			// running, yet the app cannot connect (#140): most likely
			// another program holds the WebUI port, which service.log names
			h.tone, h.sub = toneWarn, fmt.Sprintf(u.t("svc.state.running.unreachable"), s.port)
		default:
			h.tone, h.sub = toneOK, u.statusText(s)
		}
	case gui.ServiceStopped:
		h.tone, h.headline, h.sub = toneBad, u.t("svc.state.stopped"), u.t("ui.servicectl.sub.stopped")
	case gui.ServiceNotInstalled:
		h.headline, h.sub = u.t("svc.state.notinstalled"), u.t("ui.servicectl.sub.notinstalled")
		if p.risk != gui.InstallRiskNone {
			h.tone, h.sub = toneWarn, u.t("ui.servicectl.sub.risk")
		}
	default:
		h.sub = u.t("ui.servicectl.sub.unknown")
	}
	if p.busy {
		h.tone, h.sub = toneAccent, u.t("ui.servicectl.sub.busy")
	}
	return h
}

// riskText is the short line about the install location, "" when fine.
func (p *serviceCtlPage) riskText(u *UI) string {
	switch p.risk {
	case gui.InstallRiskVolatile:
		return u.t("ui.servicectl.risk.volatile")
	case gui.InstallRiskWritable:
		return u.t("ui.servicectl.risk.writable")
	case gui.InstallRiskUnsafe:
		return u.t("ui.servicectl.risk.unsafe")
	}
	return ""
}

func (p *serviceCtlPage) body(gtx C, u *UI, s snapshot) D {
	p.clicks(gtx, u, s)
	if s.conn != p.lastConn {
		p.lastConn = s.conn
		p.refresh(u)
	}
	if !p.queriedAt.IsZero() {
		next := p.queriedAt.Add(svcRefreshEvery)
		if !gtx.Now.Before(next) {
			p.refresh(u)
		} else {
			gtx.Execute(op.InvalidateCmd{At: next})
		}
	}

	th := u.th
	blocks := []layout.Widget{
		func(gtx C) D { return heroCard(gtx, th, p.hero(u, s), nil) },
	}
	if a := p.actions(u, s); a != nil {
		blocks = append(blocks, section(th, u.t("ui.servicectl.actions"), func(gtx C) D {
			if p.busy {
				gtx = gtx.Disabled()
			}
			return a(gtx)
		}))
	}
	blocks = append(blocks,
		section(th, u.t("settings.tools"), p.tools(u, s)),
		section(th, u.t("ui.servicectl.info"), p.info(u)),
	)
	return sections(gtx, blocks...)
}

// actions is the card of what can be done in the current state, nil while
// the state is unknown.
func (p *serviceCtlPage) actions(u *UI, s snapshot) layout.Widget {
	th := u.th
	if !p.known {
		return nil
	}
	stopRow := func(gtx C) D {
		return actionRow(gtx, th, &p.stop, "pause", toneOff, u.t("ui.servicectl.stop"), u.t("ui.servicectl.stop.hint"), false)
	}
	uninstallRow := func(gtx C) D {
		return actionRow(gtx, th, &p.uninstall, "delete", toneBad, u.t("ui.servicectl.uninstall"), u.t("ui.servicectl.uninstall.hint"), true)
	}
	switch p.state {
	case gui.ServiceNotInstalled:
		return func(gtx C) D {
			children := []layout.FlexChild{}
			if r := p.riskText(u); r != "" {
				children = append(children,
					layout.Rigid(func(gtx C) D { return banner(gtx, th, noticeWarn, r) }),
					vspace(gapM))
			}
			children = append(children,
				layout.Rigid(titled(th, u.t("ui.servicectl.install"), u.t("ui.servicectl.install.hint"))),
				vspace(gapM),
				layout.Rigid(fill(primaryButton(th, &p.install, u.t("svc.install")).Layout)),
			)
			return column(gtx, children...)
		}
	case gui.ServiceStopped:
		return func(gtx C) D {
			return rows(gtx,
				func(gtx C) D {
					return column(gtx,
						layout.Rigid(titled(th, u.t("ui.servicectl.start"), u.t("ui.servicectl.start.hint"))),
						vspace(gapM),
						layout.Rigid(fill(primaryButton(th, &p.start, u.t("svc.start")).Layout)),
					)
				},
				uninstallRow,
			)
		}
	case gui.ServiceRunning:
		ws := []layout.Widget{}
		if s.conn == connOK { // the restart is asked of the service itself
			ws = append(ws, func(gtx C) D {
				return actionRow(gtx, th, &p.restart, "restart", toneAccent, u.t("settings.restart"), u.t("ui.servicectl.restart.hint"), false)
			})
		}
		ws = append(ws, stopRow, uninstallRow)
		return func(gtx C) D { return rows(gtx, ws...) }
	}
	return nil
}

// tools is the WebUI (while the service runs) and the crash folder.
func (p *serviceCtlPage) tools(u *UI, s snapshot) layout.Widget {
	th := u.th
	return func(gtx C) D {
		ws := []layout.Widget{}
		if p.known && p.state == gui.ServiceRunning {
			ws = append(ws, func(gtx C) D {
				return actionRow(gtx, th, &p.webui, "open", toneAccent, u.t("settings.openwebui"), fmt.Sprintf(u.t("ui.servicectl.webui.hint"), s.port), false)
			})
		}
		ws = append(ws, func(gtx C) D {
			return column(gtx,
				layout.Rigid(func(gtx C) D {
					return actionRow(gtx, th, &p.crash, "folder", toneOff, u.t("settings.debug.open"), u.t("ui.servicectl.crash.hint"), false)
				}),
				layout.Rigid(func(gtx C) D {
					dir := p.ops.serviceCrashDir()
					if dir == "" {
						return D{}
					}
					return layout.Inset{Top: 6, Left: 36 + gapS}.Layout(gtx,
						label(th, textS, fmt.Sprintf(u.t("ui.servicectl.crash.service"), dir), colFaint, font.Normal).Layout)
				}),
			)
		})
		return rows(gtx, ws...)
	}
}

// info is the service's name and the folder it runs from, with the
// install location check.
func (p *serviceCtlPage) info(u *UI) layout.Widget {
	th := u.th
	return func(gtx C) D {
		ws := []layout.Widget{infoRow(th, u.t("ui.servicectl.name"), gui.ServiceName)}
		if p.known && p.exeDir != "" {
			ws = append(ws, func(gtx C) D {
				pillText, pillTone := u.t("ui.servicectl.risk.ok"), toneOK
				if p.risk != gui.InstallRiskNone {
					pillText, pillTone = u.t("ui.servicectl.risk.check"), toneWarn
				}
				return settingRow(gtx, th, u.t("ui.servicectl.location"), p.exeDir, statusPill(th, pillText, pillTone))
			})
		}
		return rows(gtx, ws...)
	}
}
