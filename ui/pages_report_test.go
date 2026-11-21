package ui

import (
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// fakeReport stands in for Windows: logs in a map, a folder to save in,
// and the URLs and paths the page asked to open. Nothing is launched.
type fakeReport struct {
	mu       sync.Mutex
	dir      string // where the zip goes
	urls     []string
	revealed []string
	urlErr   error
	snaps    int           // measurements taken
	snapWait chan struct{} // when set, a measurement waits for it
	svcAsked []bool        // per measurement: was the service asked
}

// diagAPI answers DiagProcess with the service's own figures.
type diagAPI struct{ fakeAPI }

func (diagAPI) DiagProcess() (gui.ServiceProcess, error) {
	return gui.ServiceProcess{PID: 4321, Threads: 21, Goroutines: 57, WorkingSet: 30 << 20}, nil
}

// The snapshot asks the service through the API while connected, and
// not at all otherwise; a failing call leaves the process-list row.
func TestReportSnapshotAsksService(t *testing.T) {
	f := &fakeReport{dir: t.TempDir()}
	u, p := newReportTest(t, f)
	u.api = diagAPI{}
	u.open(pageReport)
	waitFor(t, u, func() bool { return p.snap != nil })
	if p.snap.Service.Goroutines != 57 || p.snap.Service.Err != "" {
		t.Errorf("service row %+v", p.snap.Service)
	}

	u.api = fakeAPI{} // DiagProcess fails (an older service answers 404)
	p.snap = nil
	p.measure(u, u.current())
	waitFor(t, u, func() bool { return p.snap != nil })
	if p.snap.Service.Err != "Access is denied." {
		t.Errorf("fallback row %+v", p.snap.Service)
	}

	u.update(func(s *snapshot) { s.conn = connLost })
	p.snap = nil
	p.measure(u, u.current())
	waitFor(t, u, func() bool { return p.snap != nil })
	f.mu.Lock()
	asked := append([]bool(nil), f.svcAsked...)
	f.mu.Unlock()
	if len(asked) != 3 || !asked[0] || !asked[1] || asked[2] {
		t.Errorf("asked %v, want the service asked only while connected", asked)
	}
}

// fakeStateSnapshot is a measurement with a program named after the user.
func fakeStateSnapshot() gui.StateSnapshot {
	return gui.StateSnapshot{
		CPU: 37.5, CPUOK: true,
		App:     gui.ProcStat{Name: "smartthings-pc-control.exe", CPU: 0.3, CPUOK: true, WorkingSet: 45 << 20, Private: 30 << 20, Threads: 18, Handles: 412},
		Service: gui.ProcStat{Name: "smartthings-pc-control.exe", Threads: 23, Err: "Access is denied."},
		Top: []gui.ProcStat{
			{Name: "game.exe", Count: 1, CPU: 25, CPUOK: true, WorkingSet: 3 << 30},
			{Name: "chrome.exe", Count: 12, CPU: 8.4, CPUOK: true, WorkingSet: 1300 << 20},
			{Name: "jdoe-sync.exe", Count: 1, CPU: 1.2, CPUOK: true, WorkingSet: 80 << 20},
		},
		Skipped: 4,
	}
}

func (f *fakeReport) snapCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snaps
}

var fakeReportLogs = map[string]string{
	`S:\service.log`: "2026/11/21 10:00:00 started\n2026/11/21 10:00:01 WARNING: hub 8.8.8.8 failed for chat 123456789\n",
	`S:\gui.log`:     "2026-11-21 10:00:00 [update] ok\n",
}

func (f *fakeReport) ops() reportOps {
	return reportOps{
		system: func() gui.SystemInfo {
			return gui.SystemInfo{Windows: "Windows 11 Pro 23H2 (build 22631.4460)", CPU: "AMD Ryzen 7 5800X", Cores: 16,
				MemTotal: 32 << 30, MemFree: 20 << 30, Uptime: 50 * time.Hour, PCName: "DESK-1", UserName: "jdoe",
				GPUs: []string{"NVIDIA GeForce RTX 3070"}}
		},
		sources: func() gui.ReportSources {
			return gui.ReportSources{
				ServiceLogs: []string{`S:\service.log`},
				GUILog:      `S:\gui.log`,
				ReadFile: func(p string) ([]byte, error) {
					if s, ok := fakeReportLogs[p]; ok {
						return []byte(s), nil
					}
					return nil, fs.ErrNotExist
				},
				ReadDir: func(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist },
			}
		},
		saveDirs: func() []string { return []string{f.dir} },
		openURL: func(s string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.urls = append(f.urls, s)
			return f.urlErr
		},
		reveal: func(s string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.revealed = append(f.revealed, s)
			return nil
		},
		now: func() time.Time { return time.Date(2026, 11, 21, 15, 30, 0, 0, time.Local) },
		snapshot: func(svc func() (gui.ServiceProcess, error)) gui.StateSnapshot {
			if f.snapWait != nil {
				<-f.snapWait
			}
			snap := fakeStateSnapshot()
			if svc != nil {
				if sp, err := svc(); err == nil {
					snap.Service = gui.ProcStat{Threads: uint32(sp.Threads), Goroutines: sp.Goroutines, WorkingSet: sp.WorkingSet}
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.snaps++
			f.svcAsked = append(f.svcAsked, svc != nil)
			return snap
		},
	}
}

func (f *fakeReport) opened() (urls, revealed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.urls...), append([]string(nil), f.revealed...)
}

func newReportTest(t *testing.T, f *fakeReport) (*UI, *reportPage) {
	t.Helper()
	u := sampleUI(gui.LangKo, false)
	p := u.pages[pageReport].(*reportPage)
	p.ops = f.ops()
	return u, p
}

func TestReportRealOpsWired(t *testing.T) {
	o := realReportOps()
	if o.system == nil || o.sources == nil || o.saveDirs == nil || o.openURL == nil || o.reveal == nil || o.now == nil || o.snapshot == nil {
		t.Fatal("an op is missing")
	}
}

func TestReportShownLoadsPreview(t *testing.T) {
	f := &fakeReport{dir: t.TempDir()}
	u, p := newReportTest(t, f)
	u.open(pageReport)
	for i := range p.inc {
		if !p.inc[i].Value {
			t.Fatalf("switch %d starts off", i)
		}
	}
	waitFor(t, u, func() bool { return p.loaded })
	if len(p.warn.Service) != 1 || !strings.Contains(p.warn.Service[0], "<public-ip>") || !strings.Contains(p.warn.Service[0], "<chat-id>") {
		t.Fatalf("warnings %+v", p.warn)
	}
	// A switch the user turned off stays off when the page opens again.
	p.inc[incDiag].Value = false
	u.nav.back(u, time.Now())
	u.open(pageReport)
	if p.inc[incDiag].Value {
		t.Fatal("the switches were reset")
	}
	p.preview.open = true
	frame(u) // the preview lays out without a GPU
}

// The preview says 측정 중… until the measurement is in, then shows it,
// the program names masked.
func TestReportPreviewMeasures(t *testing.T) {
	f := &fakeReport{dir: t.TempDir(), snapWait: make(chan struct{})}
	u, p := newReportTest(t, f)
	u.open(pageReport)
	waitFor(t, u, func() bool { return p.loaded })
	preview := func() string {
		st := u.reportState(u.current(), p.sys)
		st.Snapshot, st.Measuring = p.snap, p.measuring
		r := gui.NewReport(st, "", p.include(), time.Now(), p.masker)
		return r.IssueBody(nil, "z.zip")
	}
	if !p.measuring || !strings.Contains(preview(), "측정 중…") {
		t.Fatalf("while measuring:\n%s", preview())
	}
	close(f.snapWait)
	waitFor(t, u, func() bool { return p.snap != nil })
	body := preview()
	for _, want := range []string{"보내는 순간의 상태", "| PC 전체 CPU | 37.5% |", "| chrome.exe ×12 | 8.4% | 1.3 GB |", "<name>-sync.exe", "읽을 수 없음 (Access is denied.)"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "jdoe") {
		t.Errorf("a program name kept the user name:\n%s", body)
	}
	// Without the process switch, the list goes; the CPU rows stay with
	// PC 정보.
	p.inc[incProcs].Value = false
	if body := preview(); strings.Contains(body, "game.exe") || !strings.Contains(body, "PC 전체 CPU") {
		t.Errorf("processes off:\n%s", body)
	}
	p.preview.open = true
	frame(u)
}

// Sending measures again; with both PC 정보 and the processes off it does
// not measure at all.
func TestReportSendMeasures(t *testing.T) {
	f := &fakeReport{dir: t.TempDir()}
	u, p := newReportTest(t, f)
	u.open(pageReport)
	waitFor(t, u, func() bool { return p.loaded && p.snap != nil })
	p.save.Click()
	frame(u)
	waitFor(t, u, func() bool { return !p.busy })
	if n := f.snapCount(); n != 2 {
		t.Fatalf("%d measurements, want the page's and the send's", n)
	}
	p.inc[incPC].Value, p.inc[incProcs].Value = false, false
	p.save.Click()
	frame(u)
	waitFor(t, u, func() bool { return !p.busy })
	if n := f.snapCount(); n != 2 {
		t.Fatalf("measured with both switches off (%d)", n)
	}
}

func TestReportSendOpensIssueAndExplorer(t *testing.T) {
	f := &fakeReport{dir: t.TempDir()}
	u, p := newReportTest(t, f)
	u.open(pageReport)
	waitFor(t, u, func() bool { return p.loaded })
	p.desc.SetText("허브가 PC를 못 찾아요\nDESK-1 에서 생겨요")
	p.send.Click()
	frame(u)
	if !p.busy || !p.busySend {
		t.Fatal("not busy while collecting")
	}
	waitFor(t, u, func() bool { return !p.busy })
	zip := filepath.Join(f.dir, "stpc-report-20261121-1530.zip")
	if _, err := os.Stat(zip); err != nil {
		t.Fatalf("zip: %v", err)
	}
	urls, revealed := f.opened()
	if len(revealed) != 1 || revealed[0] != zip {
		t.Fatalf("revealed %q", revealed)
	}
	if len(urls) != 1 || !strings.HasPrefix(urls[0], gui.ReportIssueURL+"?") {
		t.Fatalf("urls %q", urls)
	}
	q, _ := url.Parse(urls[0])
	if q.Query().Get("title") != "[문제] 허브가 PC를 못 찾아요" {
		t.Errorf("title %q", q.Query().Get("title"))
	}
	body := q.Query().Get("body")
	for _, want := range []string{"<name> 에서 생겨요", "WARNING: hub <public-ip> failed", "stpc-report-20261121-1530.zip", "v1.2.5", "| game.exe | 25.0% | 3.0 GB |"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "DESK-1") || strings.Contains(body, "123456789") {
		t.Errorf("body not masked:\n%s", body)
	}
	if p.result.kind != noticeOK || !strings.Contains(p.result.text, zip) || !strings.Contains(p.result.text, "끌어다") {
		t.Errorf("result %+v", p.result)
	}
	frame(u) // the banner draws
}

func TestReportSaveOnlyWritesZip(t *testing.T) {
	f := &fakeReport{dir: t.TempDir()}
	u, p := newReportTest(t, f)
	u.open(pageReport)
	p.save.Click()
	frame(u)
	if !p.busy || p.busySend {
		t.Fatal("the save button is not the busy one")
	}
	waitFor(t, u, func() bool { return !p.busy })
	urls, revealed := f.opened()
	if len(urls) != 0 || len(revealed) != 1 {
		t.Fatalf("urls %q revealed %q", urls, revealed)
	}
	if p.result.kind != noticeOK || !strings.Contains(p.result.text, "갤러리") {
		t.Errorf("result %+v", p.result)
	}
}

func TestReportFailures(t *testing.T) {
	f := &fakeReport{dir: filepath.Join(t.TempDir(), "missing")}
	u, p := newReportTest(t, f)
	u.open(pageReport)
	p.send.Click()
	frame(u)
	waitFor(t, u, func() bool { return !p.busy })
	if urls, _ := f.opened(); len(urls) != 0 {
		t.Fatal("opened the issue without a zip")
	}
	if p.result.kind != noticeError || !strings.HasPrefix(p.result.text, "보고서를 저장하지 못했어요") {
		t.Errorf("result %+v", p.result)
	}

	f.dir, f.urlErr = t.TempDir(), errors.New("no browser")
	p.send.Click()
	frame(u)
	waitFor(t, u, func() bool { return !p.busy })
	if p.result.kind != noticeWarn || !strings.Contains(p.result.text, "no browser") {
		t.Errorf("result %+v", p.result)
	}
}

func TestReportReachedFromHomeAndLogs(t *testing.T) {
	u := sampleUI(gui.LangKo, false)
	u.pages[pageReport].(*reportPage).ops = (&fakeReport{dir: t.TempDir()}).ops()
	u.home.reportRow.Click()
	frame(u)
	if u.nav.top() != u.pages[pageReport] {
		t.Fatal("the home row does not open the page")
	}
	u.nav.stack = nil
	u.open(pageLogs)
	u.pages[pageLogs].(*logsPage).report.Click()
	frame(u)
	if u.nav.top() != u.pages[pageReport] {
		t.Fatal("the logs page does not open it")
	}
}
