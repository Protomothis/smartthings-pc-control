package gui

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func sampleSnapshot() *StateSnapshot {
	return &StateSnapshot{
		CPU: 37.5, CPUOK: true,
		App:     ProcStat{Name: "smartthings-pc-control.exe", CPU: 0.3, CPUOK: true, WorkingSet: 45 << 20, Private: 30 << 20, Threads: 18, Handles: 412},
		Service: ProcStat{Name: "smartthings-pc-control.exe", Threads: 23, Err: "Access is denied."},
		Top: []ProcStat{
			{Name: "game.exe", Count: 1, CPU: 25, CPUOK: true, WorkingSet: 3 << 30},
			{Name: "chrome.exe", Count: 12, CPU: 8.4, CPUOK: true, WorkingSet: 1300 << 20},
			{Name: "jdoe-bot.exe", Count: 1, CPU: 0, CPUOK: false, WorkingSet: 5 << 20},
		},
		Skipped: 4,
	}
}

func TestReportStateSection(t *testing.T) {
	st := sampleReportState(LangKo)
	st.Snapshot = sampleSnapshot()
	st.System.GPUs = []string{"NVIDIA GeForce RTX 3070", "Intel(R) UHD Graphics 630"}
	m := NewMasker(ReportMaskInput(st.Config, st.System))
	r := NewReport(st, "느려요", ReportIncludeAll, reportTime, m)
	md := r.Markdown([]string{"report.md"})
	t.Log("\n" + md)
	for _, want := range []string{
		"| GPU | NVIDIA GeForce RTX 3070 / Intel(R) UHD Graphics 630 |",
		"## 보내는 순간의 상태",
		"| PC 전체 CPU | 37.5% |",
		"| 트레이 앱 | CPU 0.3% · 메모리 45.0 MB (전용 30.0 MB) · 스레드 18 · 핸들 412 |",
		"| 서비스 | 읽을 수 없음 (Access is denied.) · 스레드 23 |",
		"| game.exe | 25.0% | 3.0 GB |",
		"| chrome.exe ×12 | 8.4% | 1.3 GB |",
		"| <name>-bot.exe | — | 5.0 MB |",
		"권한이 없어 열지 못한 프로세스 4개는 빼고 셌어요.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report.md lacks %q", want)
		}
	}
	if st.Snapshot.Top[2].Name != "jdoe-bot.exe" {
		t.Error("NewReport changed the caller's snapshot")
	}

	// Each switch brings its own half.
	r.Include = ReportInclude{Processes: true}
	if s := r.StateSection(10); strings.Contains(s, "PC 전체 CPU") || !strings.Contains(s, "game.exe") {
		t.Errorf("processes only:\n%s", s)
	}
	r.Include = ReportInclude{PCInfo: true}
	if s := r.StateSection(10); !strings.Contains(s, "PC 전체 CPU") || strings.Contains(s, "game.exe") {
		t.Errorf("PC info only:\n%s", s)
	}
	if s := (Report{Include: ReportInclude{PCInfo: true}}).StateSection(10); s != "" {
		t.Errorf("before a measurement: %q", s)
	}
	r.State.Snapshot, r.State.Measuring = nil, true
	if s := r.StateSection(10); s != "_측정 중…_\n" {
		t.Errorf("measuring: %q", s)
	}
	r.Include = ReportInclude{ServiceLog: true}
	if s := r.StateSection(10); s != "" {
		t.Errorf("both off: %q", s)
	}
}

// Over the budget, the program list goes after the log lines and before
// the settings summary.
func TestReportIssueURLTrimsProgramsAfterLogs(t *testing.T) {
	st := sampleReportState(LangKo)
	st.Snapshot = sampleSnapshot()
	for i := range 7 {
		st.Snapshot.Top = append(st.Snapshot.Top, ProcStat{Name: fmt.Sprintf("program-with-a-long-name-%d.exe", i), Count: 1, CPUOK: true})
	}
	r := NewReport(st, strings.Repeat("설명이 깁니다. ", 150), ReportIncludeAll, reportTime, NewMasker(MaskInput{}))
	var warn []string
	for range 30 {
		warn = append(warn, strings.Repeat("경고 ", 15))
	}
	u := r.IssueURL(warn, "z.zip")
	if len(u) > reportURLMax {
		t.Fatalf("url is %d long", len(u))
	}
	q, _ := url.Parse(u)
	body := q.Query().Get("body")
	if strings.Contains(body, "경고 경고") {
		t.Error("log lines kept while the budget was short")
	}
	if !strings.Contains(body, "보내는 순간의 상태") || !strings.Contains(body, "PC 전체 CPU") {
		t.Errorf("the state rows stay:\n%s", body)
	}
	if !strings.Contains(body, "설정 요약") && strings.Contains(body, "program-with-a-long-name-0.exe") {
		t.Error("settings dropped before the programs")
	}
	if strings.Contains(body, "program-with-a-long-name-6.exe") {
		t.Errorf("the last programs should have gone:\n%s", body)
	}
}
