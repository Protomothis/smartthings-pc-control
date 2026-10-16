package gui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// The "not connected" line names the service's state (#140).
func TestUnreachableTextByServiceState(t *testing.T) {
	u := &ui{lang: LangKo}
	port := currentWebUIPort()
	for state, want := range map[svcState]string{
		svcNotInstalled: "서비스가 설치되지 않음 — 설정 탭에서 설치하세요",
		svcStopped:      "서비스가 중지됨 — 설정 탭에서 시작하세요",
		svcRunning:      fmt.Sprintf("서비스 실행 중 · 연결 안 됨 (포트 %d) — 로그 확인", port),
		svcUnknown:      fmt.Sprintf("서비스 연결 안 됨 (포트 %d)", port),
	} {
		if got := u.unreachableText(state); got != want {
			t.Errorf("state %d: %q, want %q", state, got, want)
		}
	}
}

// svcBoxText is every label in the settings tab's service box.
func svcBoxText(u *ui) string {
	var out []string
	for _, o := range u.svcBox.Objects {
		if l, ok := o.(*widget.Label); ok {
			out = append(out, l.Text)
		}
	}
	return strings.Join(out, "\n")
}

// A service that runs but does not answer is told apart from a stopped
// one, in the status bar and in the service box, which points at the log;
// the logs tab stays usable without the service.
func TestDisconnectedShowsServiceState(t *testing.T) {
	u := newTestUI(t, LangKo, NewClient(1)) // nothing listens on port 1
	state := svcRunning
	unreachableSvcState = func() svcState { return state }

	u.initialLoad()
	if u.connected.Load() {
		t.Fatal("connected to port 1")
	}
	if want := u.unreachableText(svcRunning); u.statusText != want {
		t.Errorf("status = %q, want %q", u.statusText, want)
	}
	hintText := fmt.Sprintf(T(LangKo, "svc.state.running.unreachable"), currentWebUIPort())
	if !strings.Contains(svcBoxText(u), hintText) {
		t.Errorf("service box = %q, want the port-conflict hint", svcBoxText(u))
	}
	if u.tabs.Items[tabLogs].Disabled() {
		t.Error("the logs tab is disabled without the service")
	}
	if !u.tabs.Items[tabCommands].Disabled() {
		t.Error("the commands tab is usable without the service")
	}

	// Stopped meanwhile: the line and the box follow, the hint goes.
	state = svcStopped
	u.initialLoad()
	if want := T(LangKo, "status.svc.stopped"); u.statusText != want {
		t.Errorf("status = %q, want %q", u.statusText, want)
	}
	if box := svcBoxText(u); strings.Contains(box, hintText) || !strings.Contains(box, T(LangKo, "svc.state.stopped")) {
		t.Errorf("service box = %q, want stopped without the hint", box)
	}
}

// Without the service the logs tab reads the tail of service.log itself
// and says so; on the logs tab the disconnect does not move the window to
// the settings tab.
func TestLogsTabReadsTheFileWhenDisconnected(t *testing.T) {
	u := newTestUI(t, LangEn, NewClient(1))
	u.win.Resize(fyne.NewSize(640, 400))
	lines := logLinesN(150)
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceLogFile = func() (string, error) { return path, nil }

	u.initialLoad()
	if u.curTab != tabSettings {
		t.Fatalf("on tab %d after the failed connect, want settings", u.curTab)
	}
	u.tabs.SelectIndex(tabLogs)
	if u.curTab != tabLogs {
		t.Fatal("the logs tab did not come to the front")
	}
	if !slices.Equal(u.logsShown, lines[50:]) {
		t.Fatalf("view shows %d lines (message %q), want the last 100 of the file", len(u.logsShown), u.logsShownMsg)
	}
	if !u.logsSource.Visible() || u.logsSource.Text != T(LangEn, "logs.file.shown") {
		t.Errorf("source note = %q (shown %v)", u.logsSource.Text, u.logsSource.Visible())
	}

	// Another failed connect leaves the user on the logs tab.
	u.initialLoad()
	if u.curTab != tabLogs {
		t.Errorf("moved to tab %d while reading the log", u.curTab)
	}

	// The file going away is said, not shown as an empty log.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	u.loadLogs()
	if want := fmt.Sprintf(T(LangEn, "logs.file.missing"), path); u.logsSource.Text != want {
		t.Errorf("source note = %q, want %q", u.logsSource.Text, want)
	}

	// Connected again: the service's log, no note.
	u.connected.Store(true)
	u.client = &logsAPI{serviceAPI: NewClient(1), lines: []string{"from the service"}}
	u.loadLogs()
	if u.logsSource.Visible() || !slices.Equal(u.logsShown, []string{"from the service"}) {
		t.Errorf("connected: note shown %v, view %q", u.logsSource.Visible(), u.logsShown)
	}
}

// A log the user may not read is explained with its path.
func TestLogsFileNoteForPermission(t *testing.T) {
	u := &ui{lang: LangKo}
	const path = `C:\Program Files\SmartThings PC Control\service.log`
	err := &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	if got, want := u.logsFileNote(path, err), fmt.Sprintf(T(LangKo, "logs.file.denied"), path); got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

func TestReadServiceLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("a\r\nb\nc\r\n\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readServiceLogTail(path, 2); err != nil || !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("tail = %q, %v", got, err)
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readServiceLogTail(empty, 2); err != nil || len(got) != 0 {
		t.Errorf("empty file = %q, %v", got, err)
	}
}
