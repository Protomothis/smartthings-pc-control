package ui

// 서비스 관리: install, start, stop and uninstall the Windows service, its
// install location check, restart, the WebUI and the crash folder. Phase
// 2a shows the connection only; the page itself is #165 phase 2b and
// replaces this file.

import "fmt"

type serviceCtlPage struct{}

func newServiceCtlPage() screen { return &serviceCtlPage{} }

func (p *serviceCtlPage) title(u *UI) string { return u.t("svc.section") }

func (p *serviceCtlPage) body(gtx C, u *UI, s snapshot) D {
	h := hero{icon: "service", tone: toneOff, headline: u.statusText(s)}
	switch s.conn {
	case connOK:
		h.tone, h.sub = toneOK, fmt.Sprintf(u.t("home.port"), s.port)
	case connLost:
		h.tone = toneBad
	case connLogin:
		h.tone = toneWarn
	}
	return sections(gtx,
		func(gtx C) D { return heroCard(gtx, u.th, h, nil) },
		pendingNote(u),
	)
}
