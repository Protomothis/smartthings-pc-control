package ui

import (
	"context"
	"errors"
	"image"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// fakeUpdater answers the update page without the network; nothing is
// ever downloaded or applied.
type fakeUpdater struct {
	mu       sync.Mutex
	chk      gui.UpdateCheck
	err      error
	m        *release.Manifest
	merr     error
	applyErr error
	block    bool // apply waits for its ctx to be cancelled
	checks   int
	prepares int
	applies  int
}

func (f *fakeUpdater) check() (gui.UpdateCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.chk, f.err
}

func (f *fakeUpdater) prepare(chk gui.UpdateCheck, version string) gui.UpdatePlan {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	if !gui.CanSelfUpdate(version) {
		return gui.PlanUpdate(chk, version, nil, nil)
	}
	return gui.PlanUpdate(chk, version, f.m, f.merr)
}

func (f *fakeUpdater) apply(ctx context.Context, _ gui.UpdatePlan, progress func(gui.UpdateProgress)) error {
	f.mu.Lock()
	f.applies++
	block, err := f.block, f.applyErr
	f.mu.Unlock()
	progress(gui.UpdateProgress{Stage: gui.StageDownloading, Done: 512 * 1024, Total: 2 * 1024 * 1024})
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	progress(gui.UpdateProgress{Stage: gui.StageApplying})
	return err
}

func (f *fakeUpdater) counts() (checks, prepares, applies int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.prepares, f.applies
}

// sampleReleases is a release list, newest first, the newest with an exe
// asset.
func sampleReleases(newest string) []release.Info {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.Local) }
	return []release.Info{
		{TagName: newest, HTMLURL: "https://example.invalid/" + newest, PublishedAt: day(14),
			Assets: []release.Asset{{Name: release.AssetName, DownloadURL: "https://example.invalid/a.exe"}},
			Body:   "## [" + newest + "] - 2026-11-14\r\n\r\n### 추가\r\n\r\n- **새 설정 앱** (Gio, #165)\r\n  두 줄째\r\n- 업데이트 화면에서 `릴리스 노트`를 읽기 쉽게\r\n\r\n### 수정\r\n\r\n1. [알림](https://x) 겹침 고침\r\n"},
		{TagName: "v1.2.3", PublishedAt: day(1), Body: "## [v1.2.3]\n\n- 이전 수정"},
		{TagName: "v1.2.2"},
	}
}

func sampleCheck(newest string) gui.UpdateCheck {
	list := sampleReleases(newest)
	return gui.NewUpdateCheck(&list[0], list)
}

func goodManifest(tag string) *release.Manifest {
	return &release.Manifest{Version: tag, Assets: []release.ManifestAsset{{Name: release.AssetName, Arch: runtime.GOARCH}}}
}

// updateUI is a UI on the update page with f as its updater.
func updateUI(t *testing.T, version string, f *fakeUpdater) (*UI, *updatePage, *atomic.Int32) {
	t.Helper()
	quits := new(atomic.Int32)
	u := New(fakeAPI{}, version, loadPrefs(""), func() { quits.Add(1) })
	u.th = newTheme()
	p := u.pages[pageUpdate].(*updatePage)
	p.upd = f
	return u, p, quits
}

// settle runs the window tasks until cond holds.
func settle(t *testing.T, u *UI, cond func() bool) {
	t.Helper()
	for range 200 {
		u.runTasks()
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// frame lays the page out once, without a GPU.
func frame(u *UI) {
	var ops op.Ops
	var router input.Router
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(480, 2000)),
		Now:         time.Now(),
		Source:      router.Source(),
	}
	u.layout(gtx)
}

func TestUpdatePageChecksAndOffersSelfUpdate(t *testing.T) {
	f := &fakeUpdater{chk: sampleCheck("v1.2.5"), m: goodManifest("v1.2.5")}
	u, p, _ := updateUI(t, "v1.2.4", f)
	u.open(pageUpdate)
	if !p.checking {
		t.Fatal("opening without a known release did not check")
	}
	settle(t, u, func() bool { return !p.checking })
	if s := u.current(); s.updateTag != "v1.2.5" || !s.updateCheck.Valid() {
		t.Errorf("home badge not updated: %q", s.updateTag)
	}
	if h := p.hero(u); h.tone != toneAccent || !strings.Contains(h.headline, "v1.2.5") {
		t.Errorf("hero = %+v", h)
	}
	frame(u) // starts the manifest read
	settle(t, u, func() bool { return p.planTag == "v1.2.5" })
	if !p.plan.CanApply() {
		t.Fatalf("plan = %+v", p.plan)
	}
	frame(u)
	if len(p.notes) != 1 || p.notes[0].heading != "v1.2.5 · 2026-11-14" {
		t.Fatalf("notes since v1.2.4: %+v", p.notes)
	}
	// opening again soon after does not check again
	u.nav.stack = nil
	u.open(pageUpdate)
	if c, pr, _ := f.counts(); c != 1 || pr != 1 || p.checking {
		t.Errorf("checks %d, prepares %d, checking %v", c, pr, p.checking)
	}
}

func TestUpdatePageUpToDateAndFailure(t *testing.T) {
	f := &fakeUpdater{chk: sampleCheck("v1.2.4")}
	u, p, _ := updateUI(t, "v1.2.4", f)
	u.update(func(s *snapshot) { s.updateTag = "stale" })
	u.open(pageUpdate)
	settle(t, u, func() bool { return !p.checking })
	if h := p.hero(u); h.tone != toneOK {
		t.Errorf("hero = %+v", h)
	}
	if u.current().updateTag != "" {
		t.Error("an up-to-date answer kept the badge")
	}
	frame(u)
	if p.offered(u) || p.planning {
		t.Error("an up-to-date release is offered")
	}
	if len(p.notes) != 1 {
		t.Errorf("the current release's notes: %+v", p.notes)
	}

	f.mu.Lock()
	f.err = errors.New("github down")
	f.mu.Unlock()
	p.startCheck(u)
	settle(t, u, func() bool { return !p.checking })
	if h := p.hero(u); h.tone != toneBad || p.checkErr == nil || p.checkErr.Error() != "github down" {
		t.Errorf("hero = %+v", h)
	}
}

func TestUpdatePageAdoptsTheShellsCheck(t *testing.T) {
	f := &fakeUpdater{chk: sampleCheck("v1.2.5"), merr: release.ErrNoManifest}
	u, p, _ := updateUI(t, "v1.2.4", f)
	u.update(func(s *snapshot) { s.updateTag, s.updateCheck = "v1.2.5", sampleCheck("v1.2.5") })
	u.open(pageUpdate)
	if p.checking {
		t.Error("checked again although the shell knew the answer")
	}
	frame(u)
	settle(t, u, func() bool { return p.planTag == "v1.2.5" })
	if p.plan.CanApply() || p.plan.Block != gui.UpdateUnsigned || p.plan.Note(gui.LangKo) == "" {
		t.Errorf("unsigned release: %+v", p.plan)
	}
	frame(u)
	// a newer find later replaces it
	u.update(func(s *snapshot) { s.updateCheck = sampleCheck("v1.3.0") })
	frame(u)
	if p.latest.Tag() != "v1.3.0" {
		t.Errorf("latest = %s", p.latest.Tag())
	}
}

func TestUpdatePageDevBuild(t *testing.T) {
	f := &fakeUpdater{chk: sampleCheck("v1.2.5")}
	u, p, _ := updateUI(t, "dev", f)
	u.open(pageUpdate)
	settle(t, u, func() bool { return !p.checking })
	frame(u)
	settle(t, u, func() bool { return p.planTag != "" })
	if p.plan.Block != gui.UpdateDevBuild || p.plan.CanApply() {
		t.Errorf("dev plan = %+v", p.plan)
	}
	if h := p.hero(u); h.headline != u.t("ui.update.hero.dev") {
		t.Errorf("hero = %+v", h)
	}
	if u.current().updateTag != "" {
		t.Error("a dev build got the badge")
	}
	frame(u)
}

func TestUpdatePageSkip(t *testing.T) {
	f := &fakeUpdater{chk: sampleCheck("v1.2.5"), m: goodManifest("v1.2.5")}
	u, p, _ := updateUI(t, "v1.2.4", f)
	u.open(pageUpdate)
	settle(t, u, func() bool { return !p.checking })
	u.prefs.Set(gui.PrefUpdateSkipped, "v1.2.5")
	if !p.skipped(u) || !strings.Contains(p.hero(u).sub, u.t("ui.update.skipped.short")) {
		t.Error("skipped version not shown")
	}
	u.prefs.Set(gui.PrefUpdateSkipped, "v1.2.0")
	if p.skipped(u) {
		t.Error("another version's skip applies")
	}
}

func TestUpdatePageApply(t *testing.T) {
	ready := func(t *testing.T, f *fakeUpdater) (*UI, *updatePage, *atomic.Int32) {
		u, p, quits := updateUI(t, "v1.2.4", f)
		u.open(pageUpdate)
		settle(t, u, func() bool { return !p.checking })
		frame(u)
		settle(t, u, func() bool { return p.planTag != "" })
		return u, p, quits
	}

	t.Run("success quits", func(t *testing.T) {
		f := &fakeUpdater{chk: sampleCheck("v1.2.5"), m: goodManifest("v1.2.5")}
		u, p, quits := ready(t, f)
		p.askUpdate(u)
		if u.sheetW.open == nil {
			t.Fatal("no confirmation")
		}
		u.sheetW.open.onOK()
		if !p.running {
			t.Fatal("not running")
		}
		frame(u)
		settle(t, u, func() bool { return !p.running })
		time.Sleep(20 * time.Millisecond) // quit runs on its own goroutine
		if quits.Load() != 1 {
			t.Errorf("quit %d times", quits.Load())
		}
		if _, ok := u.notices[p]; ok {
			t.Error("success left a notice")
		}
	})

	t.Run("failure shows why", func(t *testing.T) {
		f := &fakeUpdater{chk: sampleCheck("v1.2.5"), m: goodManifest("v1.2.5"), applyErr: errors.New("UAC declined")}
		u, p, quits := ready(t, f)
		p.startUpdate(u)
		settle(t, u, func() bool { return !p.running })
		n, ok := u.notices[p]
		if !ok || n.kind != noticeError || !strings.Contains(n.text, "UAC declined") {
			t.Errorf("notice = %+v", n)
		}
		if quits.Load() != 0 {
			t.Error("a failed update quit")
		}
	})

	t.Run("cancel is quiet", func(t *testing.T) {
		f := &fakeUpdater{chk: sampleCheck("v1.2.5"), m: goodManifest("v1.2.5"), block: true}
		u, p, quits := ready(t, f)
		p.startUpdate(u)
		settle(t, u, func() bool { return p.prog.Total > 0 })
		if fr := p.prog.Fraction(); fr != 0.25 {
			t.Errorf("fraction = %v", fr)
		}
		frame(u)
		p.stopping()
		settle(t, u, func() bool { return !p.running })
		if _, ok := u.notices[p]; ok || quits.Load() != 0 {
			t.Error("a cancel left a notice or quit")
		}
	})

	t.Run("blocked plan never applies", func(t *testing.T) {
		f := &fakeUpdater{chk: sampleCheck("v1.2.5"), merr: release.ErrBadSignature}
		u, p, _ := ready(t, f)
		p.startUpdate(u)
		if p.running {
			t.Error("a bad signature started an update")
		}
		if _, _, a := f.counts(); a != 0 {
			t.Error("apply called")
		}
	})
}

func TestNoteBlocks(t *testing.T) {
	md := "### 추가\r\n\r\n- **굵게** `코드`와 [링크](https://x)\r\n  이어지는 줄\r\n  - 안쪽\r\n\r\n1. 첫째\r\n2) 둘째\r\n\r\n---\r\n\r\n```\r\ncode line\r\n```\r\n\r\n문단 한 줄\r\n둘째 줄<br>\r\n\r\n\r\n"
	got := noteBlocks(md)
	want := []noteBlock{
		{kind: noteHeading, text: "추가"},
		{kind: noteGap},
		{kind: noteBullet, marker: "•", text: "굵게 코드와 링크 이어지는 줄"},
		{kind: noteBullet, marker: "•", text: "안쪽", level: 1},
		{kind: noteGap},
		{kind: noteBullet, marker: "1.", text: "첫째"},
		{kind: noteBullet, marker: "2.", text: "둘째"},
		{kind: noteGap},
		{kind: noteCode, text: "code line"},
		{kind: noteGap},
		{kind: noteText, text: "문단 한 줄 둘째 줄"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(noteBlocks("")) != 0 {
		t.Error("empty notes gave blocks")
	}
}

// A copy the setup program installed updates through the next setup
// (#166); any other copy swaps its exe as before.
func TestPickUpdater(t *testing.T) {
	if _, ok := pickUpdater(true).(installerUpdater); !ok {
		t.Errorf("installed by the setup: %T, want installerUpdater", pickUpdater(true))
	}
	if _, ok := pickUpdater(false).(selfUpdater); !ok {
		t.Errorf("installed with [설치]: %T, want selfUpdater", pickUpdater(false))
	}
}
