package gui

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// sampleReportState is a connected PC with most things switched on.
func sampleReportState(l Lang) ReportState {
	return ReportState{
		Lang:       l,
		AppVersion: "v1.2.6",
		Service:    ReportServiceOK,
		Port:       5002,
		Config: Config{
			Secret:      "hunter2pass",
			WebUIRemote: true,
			Telegram:    TelegramConfig{Enabled: true, ControlEnabled: true, BotToken: "****6789", ChatID: "123456789", PCName: "거실PC"},
			SmartThings: SmartThingsConfig{ExposeSession: true, WoLMAC: "AA:BB:CC:DD:EE:FF"},
			Media:       MediaConfig{Enabled: true, NowPlaying: true},
			Presets:     []Preset{{Slot: 1, Name: "게임", Type: "script", Path: `C:\Users\jdoe\game.ps1`}},
			Debug:       true,
		},
		ConfigSet: true,
		Hub:       STHub{Connected: true, IP: "192.168.1.20", DriverVersion: "1.4.2"},
		HubSet:    true,
		System: SystemInfo{
			Windows: "Windows 11 Pro 23H2 (build 22631.4460)", CPU: "AMD Ryzen 7 5800X 8-Core Processor", Cores: 16,
			MemTotal: 32 << 30, MemFree: 20 << 30, Uptime: 50 * time.Hour, PCName: "DESKTOP-K1M", UserName: "jdoe",
		},
	}
}

var reportTime = time.Date(2026, 11, 21, 15, 30, 0, 0, time.FixedZone("KST", 9*3600))

func sampleReport(t *testing.T, desc string) (Report, *Masker) {
	t.Helper()
	st := sampleReportState(LangKo)
	m := NewMasker(ReportMaskInput(st.Config, st.System))
	return NewReport(st, desc, ReportIncludeAll, reportTime, m), m
}

func TestReportPCRows(t *testing.T) {
	r, _ := sampleReport(t, "")
	got := map[string]string{}
	for _, row := range r.PCRows() {
		got[row.Name] = row.Value
	}
	want := map[string]string{
		"앱 버전":    "v1.2.6",
		"서비스":     "연결됨 · 포트 5002",
		"Windows": "Windows 11 Pro 23H2 (build 22631.4460)",
		"CPU":     "AMD Ryzen 7 5800X 8-Core Processor · 논리 코어 16개",
		"메모리":     "32.0 GB 중 20.0 GB 남음",
		"켜진 시간":   "2일 2시간",
		"앱 언어":    "한국어",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	r.State.Service = ReportServiceLost
	r.State.System = SystemInfo{}
	if rows := r.PCRows(); len(rows) != 3 || rows[1].Value != "연결 안 됨" {
		t.Errorf("unknown facts are left out: %+v", rows)
	}
	if s := reportUptime(LangEn, 4*time.Hour+12*time.Minute); s != "4h 12m" {
		t.Errorf("uptime %q", s)
	}
}

// The settings summary is on/off, counts and versions: none of the values
// someone could use.
func TestReportSettingRowsHideValues(t *testing.T) {
	r, _ := sampleReport(t, "")
	var all strings.Builder
	for _, row := range r.SettingRows() {
		all.WriteString(row.Name + "=" + row.Value + "\n")
	}
	s := all.String()
	for _, secret := range []string{"hunter2pass", "6789", "123456789", "거실PC", "AA:BB", "game.ps1", "192.168"} {
		if strings.Contains(s, secret) {
			t.Errorf("summary shows %q:\n%s", secret, s)
		}
	}
	for _, want := range []string{"미디어 제어=켜짐", "SmartThings 허브=연결됨 · 드라이버 1.4.2", "프리셋 수=1", "디버그 모드=켜짐", "시크릿 설정=켜짐", "PC 알림=꺼짐"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	r.State.ConfigSet = false
	if rows := r.SettingRows(); len(rows) != 1 || rows[0].Value != "알 수 없음" {
		t.Errorf("unknown config: %+v", rows)
	}
}

func TestReportIssueTitle(t *testing.T) {
	r, _ := sampleReport(t, "\n  허브가 PC를 못 찾아요  \n자세히는…")
	if got := r.IssueTitle(); got != "[문제] 허브가 PC를 못 찾아요" {
		t.Errorf("title %q", got)
	}
	r, _ = sampleReport(t, "")
	if got := r.IssueTitle(); got != "[문제] 앱에서 보낸 문제" {
		t.Errorf("empty title %q", got)
	}
	r, _ = sampleReport(t, strings.Repeat("가", 100))
	if got := r.IssueTitle(); got != "[문제] "+strings.Repeat("가", 59)+"…" {
		t.Errorf("long title %q", got)
	}
}

func TestReportIssueBody(t *testing.T) {
	r, _ := sampleReport(t, "C:\\Users\\jdoe 에서 kim@example.com 으로")
	body := r.IssueBody([]string{"2026/11/21 15:00:00 WARNING: x"}, "stpc-report-20261121-1530.zip")
	for _, want := range []string{
		"### 어떤 문제가 있었나요?", `C:\Users\<name> 에서 <email> 으로`,
		"### PC 정보", "| 앱 버전 | v1.2.6 |", "<details><summary>설정 요약</summary>",
		"```text\n2026/11/21 15:00:00 WARNING: x\n```", "`stpc-report-20261121-1530.zip`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "jdoe") {
		t.Error("the description is masked")
	}
	r.Include.PCInfo = false
	body = r.IssueBody(nil, "x.zip")
	if strings.Contains(body, "PC 정보") || strings.Contains(body, "```") {
		t.Errorf("PC info off, no warnings:\n%s", body)
	}
	if !strings.Contains(body, "<email> 으로") {
		t.Errorf("description kept:\n%s", body)
	}
}

func TestReportIssueURLFits(t *testing.T) {
	r, _ := sampleReport(t, "짧은 설명")
	warn := []string{"line one", "line two"}
	u := r.IssueURL(warn, "z.zip")
	if !strings.HasPrefix(u, ReportIssueURL+"?title=") || strings.Contains(u, "+") {
		t.Fatalf("url %q", u)
	}
	q, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if q.Query().Get("title") != "[문제] 짧은 설명" || !strings.Contains(q.Query().Get("body"), "line two") {
		t.Errorf("query %v", q.Query())
	}

	// Many long warning lines: the oldest go first, the newest stays.
	var many []string
	for i := range 200 {
		many = append(many, strings.Repeat("경고 ", 20)+string(rune('A'+i%26)))
	}
	many = append(many, "THE NEWEST LINE")
	u = r.IssueURL(many, "z.zip")
	if len(u) > reportURLMax {
		t.Fatalf("url is %d long", len(u))
	}
	q, _ = url.Parse(u)
	body := q.Query().Get("body")
	if !strings.Contains(body, "THE NEWEST LINE") || !strings.Contains(body, "설정 요약") {
		t.Errorf("trimmed the wrong part:\n%s", body)
	}

	// A huge description is cut after everything else went.
	r, _ = sampleReport(t, strings.Repeat("설명이 아주 깁니다. ", 1000))
	u = r.IssueURL(many, "z.zip")
	if len(u) > reportURLMax {
		t.Fatalf("url is %d long", len(u))
	}
	q, _ = url.Parse(u)
	body = q.Query().Get("body")
	if strings.Contains(body, "THE NEWEST LINE") || strings.Contains(body, "설정 요약") || !strings.Contains(body, "…") || !strings.Contains(body, "z.zip") {
		t.Errorf("cut:\n%s", body)
	}
}

func TestReportWarningsLines(t *testing.T) {
	m := NewMasker(MaskInput{})
	svc := "2026/11/21 10:00:00 started\n2026/11/21 10:00:01 WARNING: no secret\n2026/11/21 10:00:02 hub 8.8.8.8 failed\n"
	gl := "2026-11-21 10:00:03 [update] WARNING: slow\r\n2026-11-21 10:00:04 [heartbeat] ok\r\n"
	w := warnings(svc, gl, m)
	got := w.Lines(ReportIncludeAll)
	want := []string{"2026/11/21 10:00:01 WARNING: no secret", "2026/11/21 10:00:02 hub <public-ip> failed", "gui: 2026-11-21 10:00:03 [update] WARNING: slow"}
	if !slices.Equal(got, want) {
		t.Errorf("lines\n got %q\nwant %q", got, want)
	}
	if got := w.Lines(ReportInclude{GUILog: true}); len(got) != 1 {
		t.Errorf("service log off: %q", got)
	}
	if got := w.Lines(ReportInclude{}); len(got) != 0 {
		t.Errorf("both off: %q", got)
	}

	// The cap: the app's ten, the rest from the service, newest kept.
	var b strings.Builder
	for i := range 50 {
		b.WriteString("2026/11/21 10:00:00 WARNING: s" + string(rune('a'+i%26)) + "\n")
	}
	b.WriteString("2026/11/21 10:00:00 WARNING: last\n")
	var g strings.Builder
	for range 15 {
		g.WriteString("x [t] error y\n")
	}
	got = warnings(b.String(), g.String(), m).Lines(ReportIncludeAll)
	if len(got) != ReportWarnMax || got[ReportWarnMax-reportGUIWarnMax-1] != "2026/11/21 10:00:00 WARNING: last" {
		t.Errorf("cap: %d lines, %q", len(got), got)
	}
}

// writeFile writes data to dir/name with modification time mod.
func writeFile(t *testing.T, dir, name string, data []byte, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	var order []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
		order = append(order, f.Name)
	}
	if len(order) == 0 || order[0] != "report.md" {
		t.Errorf("report.md is not first: %q", order)
	}
	return out
}

func TestCollectReportZip(t *testing.T) {
	dir := t.TempDir()
	old := reportTime.Add(-time.Hour)
	svc := writeFile(t, dir, "svc/service.log", []byte("2026/11/21 10:00:00 Hub 192.168.1.20 connected\r\n2026/11/21 10:00:01 WARNING: chat 123456789 from 8.8.4.4 failed\r\n"), old)
	writeFile(t, dir, "svc/service.log.1", []byte("older DESKTOP-K1M line\n"), old)
	gl := writeFile(t, dir, "app/gui.log", []byte("2026-11-21 10:00:00 [debug] C:\\Users\\jdoe\\AppData now playing Ditto\r\n"), old)
	appDiag, svcDiag := filepath.Join(dir, "app", "diag"), filepath.Join(dir, "svc", "diag")
	writeFile(t, appDiag, "cpu-1.pprof", bytes.Repeat([]byte{1}, 600), reportTime.Add(-3*time.Minute))
	writeFile(t, appDiag, "goroutines-1.txt", []byte("goroutine 1 [running]:"), reportTime.Add(-1*time.Minute))
	writeFile(t, svcDiag, "cpu-2.pprof", bytes.Repeat([]byte{2}, 600), reportTime.Add(-2*time.Minute))
	writeFile(t, svcDiag, "cpu-0.pprof", bytes.Repeat([]byte{0}, 100), reportTime.Add(-10*time.Minute))
	if err := os.MkdirAll(filepath.Join(svcDiag, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := ReportSources{
		ServiceLogs: serviceLogSet(svc),
		GUILog:      gl,
		DiagDirs:    []ReportDiagDir{{appDiag, "diag/app"}, {svcDiag, "diag/service"}, {filepath.Join(dir, "nope"), "diag/x"}},
		ReadFile:    os.ReadFile,
		ReadDir:     os.ReadDir,
	}
	saved := reportDiagMax
	reportDiagMax = 1000 // the newest two take 622; 600 more do not fit, 100 do
	defer func() { reportDiagMax = saved }()

	r, m := sampleReport(t, "느려요")
	files, warn := CollectReport(r, src, m)
	var buf bytes.Buffer
	if err := WriteReportZip(&buf, files); err != nil {
		t.Fatal(err)
	}
	got := unzip(t, buf.Bytes())
	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	slices.Sort(names)
	want := []string{"diag/app/goroutines-1.txt", "diag/service/cpu-0.pprof", "diag/service/cpu-2.pprof", "gui.log", "report.md", "service.log", "service.log.1"}
	if !slices.Equal(names, want) {
		t.Fatalf("zip holds %q, want %q", names, want)
	}
	if s := got["service.log"]; !strings.Contains(s, "Hub 192.168.1.x connected") || !strings.Contains(s, "chat <chat-id> from <public-ip>") {
		t.Errorf("service.log not masked:\n%s", s)
	}
	if got["service.log.1"] != "older <name> line\n" {
		t.Errorf("service.log.1: %q", got["service.log.1"])
	}
	if s := got["gui.log"]; strings.Contains(s, "jdoe") || strings.Contains(s, "Ditto") {
		t.Errorf("gui.log not masked: %q", s)
	}
	if got["diag/service/cpu-2.pprof"] != string(bytes.Repeat([]byte{2}, 600)) {
		t.Error("a profile is included as it is")
	}
	md := got["report.md"]
	for _, w := range []string{"# [문제] 느려요", "만든 시각: 2026-11-21 15:30 +0900", "| 앱 버전 | v1.2.6 |", "## 설정 요약", "- service.log.1", "- diag/service/cpu-0.pprof"} {
		if !strings.Contains(md, w) {
			t.Errorf("report.md lacks %q:\n%s", w, md)
		}
	}
	for _, secret := range []string{"hunter2pass", "123456789", "jdoe", "DESKTOP-K1M"} {
		for n, s := range got {
			if strings.Contains(s, secret) {
				t.Errorf("%s shows %q", n, secret)
			}
		}
	}
	if len(warn) != 1 || warn[0] != "2026/11/21 10:00:01 WARNING: chat <chat-id> from <public-ip> failed" {
		t.Errorf("warn %q", warn)
	}

	// Everything off but the PC info: report.md alone, no warnings.
	r.Include = ReportInclude{PCInfo: true}
	files, warn = CollectReport(r, src, m)
	if len(files) != 1 || files[0].Name != "report.md" || len(warn) != 0 {
		t.Errorf("only report.md: %d files, warn %q", len(files), warn)
	}
}

// A reader that fails everywhere leaves report.md alone.
func TestCollectReportUnreadable(t *testing.T) {
	src := ReportSources{
		ServiceLogs: []string{`X:\service.log`},
		GUILog:      `X:\gui.log`,
		DiagDirs:    []ReportDiagDir{{`X:\diag`, "diag/app"}},
		ReadFile:    func(string) ([]byte, error) { return nil, fs.ErrPermission },
		ReadDir:     func(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist },
	}
	r, m := sampleReport(t, "x")
	files, warn := CollectReport(r, src, m)
	if len(files) != 1 || len(warn) != 0 {
		t.Fatalf("%d files, warn %q", len(files), warn)
	}
	if w := ReadReportWarnings(src, m); len(w.Service)+len(w.GUI) != 0 {
		t.Errorf("warnings %+v", w)
	}
}

func TestReadReportWarnings(t *testing.T) {
	logs := map[string]string{`S:\service.log`: "2026/11/21 10:00:00 panic: boom at C:\\Users\\jdoe\\x", `S:\gui.log`: "a\nb error c"}
	src := ReportSources{
		ServiceLogs: []string{`S:\service.log`},
		GUILog:      `S:\gui.log`,
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := logs[p]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("no")
		},
	}
	w := ReadReportWarnings(src, NewMasker(MaskInput{}))
	if !slices.Equal(w.Service, []string{`2026/11/21 10:00:00 panic: boom at C:\Users\<user>\x`}) || !slices.Equal(w.GUI, []string{"b error c"}) {
		t.Errorf("%+v", w)
	}
}

func TestSaveReportZip(t *testing.T) {
	dir := t.TempDir()
	files := []ReportFile{{Name: "report.md", Data: []byte("hi")}}
	name := ReportZipName(reportTime)
	if name != "stpc-report-20261121-1530.zip" {
		t.Fatalf("name %q", name)
	}
	missing := filepath.Join(dir, "no", "such", "dir")
	p1, err := SaveReportZip([]string{"", missing, dir}, name, files)
	if err != nil || p1 != filepath.Join(dir, name) {
		t.Fatalf("first: %q %v", p1, err)
	}
	p2, err := SaveReportZip([]string{dir}, name, files)
	if err != nil || p2 != filepath.Join(dir, "stpc-report-20261121-1530-2.zip") {
		t.Fatalf("second: %q %v", p2, err)
	}
	data, _ := os.ReadFile(p2)
	if got := unzip(t, data); got["report.md"] != "hi" {
		t.Errorf("zip %v", got)
	}
	if _, err := SaveReportZip([]string{missing}, name, files); err == nil {
		t.Error("no folder that takes it: an error")
	}
	if _, err := SaveReportZip(nil, name, files); err == nil {
		t.Error("no folders: an error")
	}
}

func TestDefaultReportSources(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\LAD`)
	src := DefaultReportSources()
	if len(src.ServiceLogs) != 4 || !strings.HasSuffix(src.ServiceLogs[0], "service.log") || !strings.HasSuffix(src.ServiceLogs[3], "service.log.3") {
		t.Errorf("service logs %q", src.ServiceLogs)
	}
	if src.GUILog != `C:\LAD\SmartThings PC Control\gui.log` {
		t.Errorf("gui log %q", src.GUILog)
	}
	if len(src.DiagDirs) != 2 || src.DiagDirs[0].Dir != `C:\LAD\SmartThings PC Control\diag` || src.DiagDirs[0].Zip != "diag/app" ||
		filepath.Base(src.DiagDirs[1].Dir) != "diag" || src.DiagDirs[1].Zip != "diag/service" {
		t.Errorf("diag dirs %+v", src.DiagDirs)
	}
	if src.ReadFile == nil || src.ReadDir == nil {
		t.Error("readers missing")
	}
	if dirs := ReportSaveDirs(); len(dirs) == 0 || dirs[len(dirs)-1] != os.TempDir() {
		t.Errorf("save dirs %q", dirs)
	}
}
