package ui

// 문제 보내기 (#175): the user writes what went wrong, picks what goes
// along (PC info, the two logs, diagnostic files) and either opens a
// prefilled GitHub issue or only saves the zip (for someone without a
// GitHub account, to attach to a forum post).
//
// Both buttons write the whole report as a zip (gui.CollectReport, masked)
// to the Desktop and show it selected in Explorer: a browser cannot attach
// a file to the issue by URL, so the page then says to drag it in. The
// issue itself carries the description, the PC facts and the latest
// warning and error lines in its URL (gui.Report.IssueURL).
//
// Reading the logs and zipping run off the window goroutine, the buttons
// busy meanwhile. Everything that reaches outside — the registry, files,
// the browser, Explorer — goes through p.ops, so the tests launch nothing.

import (
	"fmt"
	"path/filepath"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// reportOps is what the page asks of Windows; the tests replace it.
type reportOps struct {
	system   func() gui.SystemInfo
	sources  func() gui.ReportSources
	saveDirs func() []string
	openURL  func(string) error
	reveal   func(string) error
	now      func() time.Time
}

func realReportOps() reportOps {
	return reportOps{
		system:   gui.ReadSystemInfo,
		sources:  gui.DefaultReportSources,
		saveDirs: gui.ReportSaveDirs,
		openURL:  openURL,
		reveal:   revealPath,
		now:      time.Now,
	}
}

// What can be included, in the order of the switches.
const (
	incPC = iota
	incService
	incGUI
	incDiag
	incCount
)

// reportDescLines is the description box's least height in lines.
const reportDescLines = 5

type reportPage struct {
	ops reportOps

	desc       widget.Editor
	inc        [incCount]widget.Bool
	preview    expander
	send, save widget.Clickable

	// Window goroutine only.
	started  bool // the switches have their defaults
	sys      gui.SystemInfo
	masker   *gui.Masker
	warn     gui.ReportWarnings
	loaded   bool // sys, masker and warn are read
	loading  bool
	busy     bool // a zip is being written
	busySend bool // … for the GitHub button (its label says so)
	result   notice
}

func newReportPage() screen { return &reportPage{ops: realReportOps()} }

func (p *reportPage) title(u *UI) string { return u.t("ui.report.title") }

// shown switches everything on the first time and reads the PC facts and
// the latest warnings for the preview.
func (p *reportPage) shown(u *UI, s snapshot) {
	if !p.started {
		p.started = true
		for i := range p.inc {
			p.inc[i].Value = true
		}
	}
	p.result = notice{}
	p.load(u, s)
}

// load reads the PC facts and the logs' warning lines off the window
// goroutine.
func (p *reportPage) load(u *UI, s snapshot) {
	if p.loading {
		return
	}
	p.loading = true
	ops, cfg := p.ops, s.cfg
	go func() {
		sys := ops.system()
		m := gui.NewMasker(gui.ReportMaskInput(cfg, sys))
		warn := gui.ReadReportWarnings(ops.sources(), m)
		u.onWindow(func() {
			p.sys, p.masker, p.warn, p.loaded, p.loading = sys, m, warn, true, false
		})
	}()
}

// include is what the switches say.
func (p *reportPage) include() gui.ReportInclude {
	return gui.ReportInclude{
		PCInfo:     p.inc[incPC].Value,
		ServiceLog: p.inc[incService].Value,
		GUILog:     p.inc[incGUI].Value,
		Diag:       p.inc[incDiag].Value,
	}
}

// reportState is what the app knows of the service and the PC now.
func (u *UI) reportState(s snapshot, sys gui.SystemInfo) gui.ReportState {
	st := gui.ReportState{
		Lang:       u.language(),
		AppVersion: u.version,
		Config:     s.cfg,
		ConfigSet:  s.cfgSet,
		Hub:        s.hub,
		HubSet:     s.hubSet,
		System:     sys,
	}
	switch s.conn {
	case connOK:
		st.Service, st.Port = gui.ReportServiceOK, s.port
	case connLogin:
		st.Service = gui.ReportServiceLogin
	case connLost:
		st.Service = gui.ReportServiceLost
	}
	return st
}

// run writes the zip, shows it in Explorer and, for toGitHub, opens the
// prefilled issue; the outcome is the banner over the buttons.
func (p *reportPage) run(u *UI, s snapshot, toGitHub bool) {
	if p.busy {
		return
	}
	p.busy, p.busySend, p.result = true, toGitHub, notice{}
	ops, desc, inc := p.ops, p.desc.Text(), p.include()
	sys, haveSys := p.sys, p.loaded
	st := u.reportState(s, sys)
	l := u.language()
	go func() {
		if !haveSys {
			st.System = ops.system()
		}
		m := gui.NewMasker(gui.ReportMaskInput(st.Config, st.System))
		now := ops.now()
		r := gui.NewReport(st, desc, inc, now, m)
		files, warn := gui.CollectReport(r, ops.sources(), m)
		path, err := gui.SaveReportZip(ops.saveDirs(), gui.ReportZipName(now), files)
		var res notice
		switch {
		case err != nil:
			res = notice{noticeError, fmt.Sprintf(gui.T(l, "ui.report.fail"), err)}
		default:
			// Explorer first: the browser then opens over it, and the
			// file is right there to drag once the issue page is up.
			_ = ops.reveal(path)
			res = notice{noticeOK, fmt.Sprintf(gui.T(l, "ui.report.done.save"), path)}
			if toGitHub {
				res = notice{noticeOK, fmt.Sprintf(gui.T(l, "ui.report.done.send"), path)}
				if err := ops.openURL(r.IssueURL(warn, filepath.Base(path))); err != nil {
					res = notice{noticeWarn, fmt.Sprintf(gui.T(l, "ui.report.browser.fail"), err)}
				}
			}
		}
		u.onWindow(func() { p.busy, p.result = false, res })
	}()
}

func (p *reportPage) body(gtx C, u *UI, s snapshot) D {
	if p.send.Clicked(gtx) {
		p.run(u, s, true)
	}
	if p.save.Clicked(gtx) {
		p.run(u, s, false)
	}
	th := u.th
	return sections(gtx,
		section(th, u.t("ui.report.desc.title"), textArea(th, &p.desc, u.t("ui.report.desc.hint"), reportDescLines)),
		section(th, u.t("ui.report.include"), p.includeCard(u)),
		func(gtx C) D { return card(gtx, p.previewCard(u, s)) },
		// In a card: the grey button is the page background's colour.
		func(gtx C) D { return card(gtx, p.actions(u)) },
	)
}

// includeCard is the four switches and the line on what is masked.
func (p *reportPage) includeCard(u *UI) layout.Widget {
	th := u.th
	items := [incCount][2]string{
		{u.t("ui.report.inc.pc"), u.t("ui.report.inc.pc.sub")},
		{u.t("ui.report.inc.svc"), u.t("ui.report.inc.svc.sub")},
		{u.t("ui.report.inc.gui"), u.t("ui.report.inc.gui.sub")},
		{u.t("ui.report.inc.diag"), u.t("ui.report.inc.diag.sub")},
	}
	return func(gtx C) D {
		ws := make([]layout.Widget, 0, incCount+1)
		for i := range items {
			ws = append(ws, func(gtx C) D { return toggleRow(gtx, th, &p.inc[i], items[i][0], items[i][1]) })
		}
		ws = append(ws, func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "lock", 16, colSub) }),
				hspace(8),
				layout.Flexed(1, label(th, textS, u.t("ui.report.privacy"), colSub, font.Normal).Layout),
			)
		})
		return rows(gtx, ws...)
	}
}

// previewCard opens to the issue text as it will be sent (the zip name
// as of now).
func (p *reportPage) previewCard(u *UI, s snapshot) layout.Widget {
	th := u.th
	return func(gtx C) D {
		return p.preview.Layout(gtx, th, u.t("ui.report.preview"), func(gtx C) D {
			txt := u.t("ui.report.preview.loading")
			if p.loaded {
				r := gui.NewReport(u.reportState(s, p.sys), p.desc.Text(), p.include(), p.ops.now(), p.masker)
				txt = r.IssueTitle() + "\n\n" + r.IssueBody(p.warn.Lines(r.Include), gui.ReportZipName(r.Created))
			}
			return surface(gtx, colBg, radiusM, func(gtx C) D {
				return layout.UniformInset(gapS).Layout(gtx, func(gtx C) D {
					l := label(th, textS, txt, colSub, font.Normal)
					l.Font.Typeface = logsFace
					return l.Layout(gtx)
				})
			})
		})
	}
}

// actions is the outcome banner, the two buttons and the hint for those
// without a GitHub account.
func (p *reportPage) actions(u *UI) layout.Widget {
	th := u.th
	return func(gtx C) D {
		sendTxt, saveTxt := u.t("ui.report.send"), u.t("ui.report.save")
		if p.busy {
			if p.busySend {
				sendTxt = u.t("ui.report.busy")
			} else {
				saveTxt = u.t("ui.report.busy")
			}
		}
		var children []layout.FlexChild
		if p.result.text != "" {
			children = append(children,
				layout.Rigid(func(gtx C) D { return banner(gtx, th, p.result.kind, p.result.text) }),
				vspace(gapM))
		}
		children = append(children,
			layout.Rigid(func(gtx C) D {
				if p.busy {
					gtx = gtx.Disabled()
				}
				return column(gtx,
					layout.Rigid(fill(primaryButton(th, &p.send, sendTxt).Layout)),
					vspace(gapS),
					layout.Rigid(fill(greyButton(th, &p.save, saveTxt).Layout)),
				)
			}),
			vspace(gapS),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: 4, Right: 4}.Layout(gtx, label(th, textS, u.t("ui.report.buttons.hint"), colSub, font.Normal).Layout)
			}),
		)
		return column(gtx, children...)
	}
}
