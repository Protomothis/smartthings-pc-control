package ui

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// fakeSvc stands in for Windows: a state, an install location and the
// actions it was asked for. Nothing is elevated, no SCM is asked.
type fakeSvc struct {
	mu       sync.Mutex
	state    gui.ServiceState
	dir      string
	risk     gui.InstallRisk
	err      error // returned by the elevated actions
	calls    []string
	opened   []string
	restarts int
}

func (f *fakeSvc) record(name string, next gui.ServiceState) func() error {
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, name)
		if f.err != nil {
			return f.err
		}
		f.state = next
		return nil
	}
}

func (f *fakeSvc) ops() serviceOps {
	return serviceOps{
		state: func() gui.ServiceState {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.state
		},
		installRisk: func() (string, gui.InstallRisk) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.dir, f.risk
		},
		install:         f.record("install", gui.ServiceRunning),
		start:           f.record("start", gui.ServiceRunning),
		stop:            f.record("stop", gui.ServiceStopped),
		uninstall:       f.record("uninstall", gui.ServiceNotInstalled),
		crashDir:        func() (string, error) { return `C:\crash`, nil },
		serviceCrashDir: func() string { return `C:\svc\crash` },
		openURL:         func(s string) error { f.open(s); return nil },
		openPath:        func(s string) error { f.open(s); return nil },
		restart:         func(*UI) { f.mu.Lock(); f.restarts++; f.mu.Unlock() },
		reconnect:       func(*UI) {},
	}
}

func (f *fakeSvc) open(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, s)
}

func (f *fakeSvc) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func newCtlTest(t *testing.T, f *fakeSvc) (*UI, *serviceCtlPage) {
	t.Helper()
	u := New(fakeAPI{}, "dev", loadPrefs(""), func() {})
	u.lang = gui.LangKo
	p := u.pages[pageServiceCtl].(*serviceCtlPage)
	p.ops = f.ops()
	return u, p
}

// waitFor runs the window tasks until ok holds.
func waitFor(t *testing.T, u *UI, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
		u.runTasks()
	}
}

func ctlGtx() C { return layout.Context{Ops: new(op.Ops), Now: time.Now()} }

func TestServiceCtlRealOpsWired(t *testing.T) {
	o := realServiceOps()
	if o.state == nil || o.installRisk == nil || o.install == nil || o.start == nil || o.stop == nil ||
		o.uninstall == nil || o.crashDir == nil || o.serviceCrashDir == nil || o.openURL == nil ||
		o.openPath == nil || o.restart == nil || o.reconnect == nil {
		t.Fatal("an op is missing")
	}
}

func TestServiceCtlShownReadsState(t *testing.T) {
	f := &fakeSvc{state: gui.ServiceStopped, dir: `C:\Users\me\Downloads`, risk: gui.InstallRiskVolatile}
	u, p := newCtlTest(t, f)
	if h := p.hero(u, snapshot{}); h.headline != u.t("ui.servicectl.state.unknown") {
		t.Fatalf("before the answer: %q", h.headline)
	}
	u.open(pageServiceCtl)
	waitFor(t, u, func() bool { return p.known })
	if p.state != gui.ServiceStopped || p.exeDir != f.dir || p.risk != gui.InstallRiskVolatile {
		t.Fatalf("state %v dir %q risk %v", p.state, p.exeDir, p.risk)
	}
}

func TestServiceCtlHero(t *testing.T) {
	f := &fakeSvc{}
	u, p := newCtlTest(t, f)
	for _, c := range []struct {
		state gui.ServiceState
		risk  gui.InstallRisk
		conn  connState
		tone  tone
		head  string
		sub   string // a part of it
	}{
		{gui.ServiceRunning, 0, connOK, toneOK, "svc.state.running", "5002"},
		{gui.ServiceRunning, 0, connLost, toneWarn, "svc.state.running", "5002"},
		{gui.ServiceStopped, 0, connLost, toneBad, "svc.state.stopped", ""},
		{gui.ServiceNotInstalled, gui.InstallRiskNone, connLost, toneOff, "svc.state.notinstalled", ""},
		{gui.ServiceNotInstalled, gui.InstallRiskWritable, connLost, toneWarn, "svc.state.notinstalled", u.t("ui.servicectl.sub.risk")},
		{gui.ServiceUnknown, 0, connLost, toneOff, "ui.servicectl.state.unknown", ""},
	} {
		p.setState(c.state, `C:\x`, c.risk)
		h := p.hero(u, snapshot{conn: c.conn, port: 5002})
		if h.tone != c.tone || h.headline != u.t(c.head) || !strings.Contains(h.sub, c.sub) {
			t.Errorf("%v/%v/%v: %+v", c.state, c.risk, c.conn, h)
		}
	}
	p.busy = true
	if h := p.hero(u, snapshot{}); h.sub != u.t("ui.servicectl.sub.busy") {
		t.Errorf("busy: %q", h.sub)
	}
}

func TestServiceCtlActionsByState(t *testing.T) {
	f := &fakeSvc{}
	u, p := newCtlTest(t, f)
	if p.actions(u, snapshot{}) != nil {
		t.Fatal("actions before the state is known")
	}
	for _, st := range []gui.ServiceState{gui.ServiceNotInstalled, gui.ServiceStopped, gui.ServiceRunning} {
		p.setState(st, "", 0)
		if p.actions(u, snapshot{conn: connOK}) == nil {
			t.Errorf("%v: no actions", st)
		}
	}
	p.setState(gui.ServiceUnknown, "", 0)
	if p.actions(u, snapshot{}) != nil {
		t.Error("actions while the state is unknown")
	}
}

func TestServiceCtlInstallSafeRunsAtOnce(t *testing.T) {
	f := &fakeSvc{state: gui.ServiceNotInstalled, dir: `C:\Program Files\X`}
	u, p := newCtlTest(t, f)
	p.setState(gui.ServiceNotInstalled, f.dir, gui.InstallRiskNone)
	p.install.Click()
	p.clicks(ctlGtx(), u, snapshot{})
	if u.sheetW.open != nil {
		t.Fatal("asked about a safe folder")
	}
	if !p.busy {
		t.Fatal("not busy while installing")
	}
	waitFor(t, u, func() bool { return !p.busy })
	if got := f.callList(); !slices.Equal(got, []string{"install"}) {
		t.Fatalf("calls %v", got)
	}
	if p.state != gui.ServiceRunning {
		t.Fatalf("state after install: %v", p.state)
	}
	if tn, ok := u.toast.current(time.Now()); !ok || tn.text != u.t("ui.servicectl.done.install") {
		t.Fatalf("toast %+v", tn)
	}
}

func TestServiceCtlInstallRiskAsksFirst(t *testing.T) {
	f := &fakeSvc{state: gui.ServiceNotInstalled}
	u, p := newCtlTest(t, f)
	p.setState(gui.ServiceNotInstalled, `D:\`, gui.InstallRiskUnsafe)
	p.install.Click()
	p.clicks(ctlGtx(), u, snapshot{})
	a := u.sheetW.open
	if a == nil || !a.danger || !strings.Contains(a.body, `D:\`) || !strings.Contains(a.body, gui.RecommendedInstallDir) {
		t.Fatalf("question %+v", a)
	}
	if len(f.callList()) != 0 {
		t.Fatal("installed before the answer")
	}
	a.onOK()
	waitFor(t, u, func() bool { return !p.busy })
	if got := f.callList(); !slices.Equal(got, []string{"install"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestServiceCtlDangerousActionsConfirm(t *testing.T) {
	for _, c := range []struct {
		name  string
		click func(p *serviceCtlPage)
		want  gui.ServiceState
	}{
		{"stop", func(p *serviceCtlPage) { p.stop.Click() }, gui.ServiceStopped},
		{"uninstall", func(p *serviceCtlPage) { p.uninstall.Click() }, gui.ServiceNotInstalled},
	} {
		f := &fakeSvc{state: gui.ServiceRunning}
		u, p := newCtlTest(t, f)
		p.setState(gui.ServiceRunning, "", 0)
		c.click(p)
		p.clicks(ctlGtx(), u, snapshot{conn: connOK})
		a := u.sheetW.open
		if a == nil || !a.danger {
			t.Fatalf("%s: no danger question: %+v", c.name, a)
		}
		if len(f.callList()) != 0 {
			t.Fatalf("%s ran before the answer", c.name)
		}
		a.onOK()
		waitFor(t, u, func() bool { return !p.busy })
		if got := f.callList(); !slices.Equal(got, []string{c.name}) || p.state != c.want {
			t.Fatalf("%s: calls %v state %v", c.name, got, p.state)
		}
	}
}

func TestServiceCtlStartAndFailure(t *testing.T) {
	f := &fakeSvc{state: gui.ServiceStopped, err: errors.New("The operation was canceled by the user.")}
	u, p := newCtlTest(t, f)
	p.setState(gui.ServiceStopped, "", 0)
	p.start.Click()
	p.clicks(ctlGtx(), u, snapshot{})
	waitFor(t, u, func() bool { return !p.busy })
	n, ok := u.notices[p]
	if !ok || n.kind != noticeError || !strings.Contains(n.text, "canceled") {
		t.Fatalf("notice %+v", n)
	}
	if p.state != gui.ServiceStopped {
		t.Fatalf("state %v", p.state)
	}
	// a second try that works clears the notice
	f.err = nil
	p.start.Click()
	p.clicks(ctlGtx(), u, snapshot{})
	waitFor(t, u, func() bool { return !p.busy })
	if _, ok := u.notices[p]; ok || p.state != gui.ServiceRunning {
		t.Fatalf("notice kept or state %v", p.state)
	}
}

func TestServiceCtlRestartWebUICrash(t *testing.T) {
	f := &fakeSvc{state: gui.ServiceRunning}
	u, p := newCtlTest(t, f)
	p.setState(gui.ServiceRunning, "", 0)

	p.restart.Click()
	p.clicks(ctlGtx(), u, snapshot{conn: connOK})
	a := u.sheetW.open
	if a == nil || a.danger {
		t.Fatalf("restart question %+v", a)
	}
	a.onOK()
	if f.restarts != 1 {
		t.Fatalf("restarts %d", f.restarts)
	}

	p.webui.Click()
	p.crash.Click()
	p.clicks(ctlGtx(), u, snapshot{conn: connOK, port: 5002})
	if !slices.Equal(f.opened, []string{"http://127.0.0.1:5002", `C:\crash`}) {
		t.Fatalf("opened %v", f.opened)
	}
}

func TestServiceCtlBusyIgnoresPresses(t *testing.T) {
	f := &fakeSvc{state: gui.ServiceStopped}
	u, p := newCtlTest(t, f)
	p.setState(gui.ServiceStopped, "", 0)
	p.busy = true
	p.start.Click()
	p.uninstall.Click()
	p.clicks(ctlGtx(), u, snapshot{})
	if u.sheetW.open != nil || len(f.callList()) != 0 {
		t.Fatal("a press went through while busy")
	}
}
