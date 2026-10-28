package gui

// Why the app cannot reach the service (#140). "서비스 연결 안 됨" alone
// said the same thing whether the service was missing, stopped or running
// behind a port another program holds; the Windows service state tells
// those apart, and the logs tab reads service.log itself meanwhile
// (logs_tab.go).

import "fmt"

// unreachableSvcState is how the "not connected" paths ask the SCM; the
// tests replace it.
var unreachableSvcState = scmServiceState

// offlineTab reports whether tab i stays usable without the service: the
// settings tab (install, start) and the logs tab, which then shows
// service.log read from disk.
func offlineTab(i int) bool { return i == tabSettings || i == tabLogs }

// unreachableText is the status line while the service does not answer,
// by the service's state. A running service the app cannot reach is the
// case worth a hint: the WebUI port may be taken (service.log says so).
func (u *ui) unreachableText(state svcState) string {
	port := currentWebUIPort()
	switch state {
	case svcNotInstalled:
		return u.t("status.svc.notinstalled")
	case svcStopped:
		return u.t("status.svc.stopped")
	case svcRunning:
		return fmt.Sprintf(u.t("status.svc.running"), port)
	}
	return fmt.Sprintf(u.t("status.unreachable"), port)
}

// showUnreachable puts the line for state in the status bar and, when the
// state changed, redraws the settings tab's service box with it, so a
// service stopped or removed meanwhile offers [시작] or [설치] at once.
// UI thread only.
func (u *ui) showUnreachable(state svcState) {
	u.setStatus(u.unreachableText(state))
	if state != u.svcDiag {
		u.svcDiag = state
		if state != svcUnknown { // the box would read that as "not installed"
			u.fillSvcBox(state)
		}
	}
}

// svcDiagReset forgets the state seen while unreachable once the service
// answers again, re-querying the service box if it showed one. UI thread
// only.
func (u *ui) svcDiagReset() {
	if u.svcDiag == svcUnknown {
		return
	}
	u.svcDiag = svcUnknown
	u.refreshSvcBox()
}
