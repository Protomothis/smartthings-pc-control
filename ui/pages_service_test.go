package ui

import (
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// notifyAPI answers /api/notify/test.
type notifyAPI struct {
	*configAPI
	calls int
	err   error
}

func (a *notifyAPI) TestNotify() (gui.NotifyResult, error) {
	a.calls++
	return gui.NotifyResult{}, a.err
}

// [테스트 알림] asks the service for a notification and says how it went;
// opening the page again clears the line (a fresh config does not).
func TestServiceNotifyTest(t *testing.T) {
	cfg := &configAPI{cfg: baseConfig()}
	u := formUI(t, cfg)
	api := &notifyAPI{configAPI: cfg}
	u.api = api
	u.open(pageService)
	p := u.pages[pageService].(*servicePage)

	p.sendNotifyTest(u)
	if !p.notifyTesting || p.notifyResult != u.t("notifypc.test.sending") {
		t.Fatalf("not sending: %q", p.notifyResult)
	}
	waitTasks(t, u, func() bool { return !p.notifyTesting })
	if api.calls != 1 || p.notifyResultTn != toneOK || p.notifyResult != u.t("notifypc.test.shown") {
		t.Fatalf("calls %d, result %q (%v)", api.calls, p.notifyResult, p.notifyResultTn)
	}

	api.err = errFake
	p.sendNotifyTest(u)
	waitTasks(t, u, func() bool { return !p.notifyTesting })
	if p.notifyResultTn != toneBad || p.notifyResult == "" {
		t.Fatalf("failure shown as %q (%v)", p.notifyResult, p.notifyResultTn)
	}

	p.fill(u, baseConfig())
	if p.notifyResult == "" {
		t.Fatal("a refill cleared the result")
	}
	u.nav.back(u, time.Now())
	u.open(pageService)
	if p.notifyResult != "" {
		t.Fatal("reopening kept the result")
	}
}
