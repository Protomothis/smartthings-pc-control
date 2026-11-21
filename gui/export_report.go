package gui

// 문제 보내기 without a toolkit (#175): what a problem report says, the
// GitHub issue it prefills, and the zip of logs that goes with it. The
// report page (ui/pages_report.go) uses it.
//
// The issue page is opened with the title and body in the URL, so the body
// carries only what fits there: the description, the PC facts, the
// settings in on/off, and the latest warning and error lines. Everything
// else — the whole logs, diagnostic files — goes into a zip the user drags
// onto the issue. All of it passes through a Masker (report_mask.go)
// first: the issue is public.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const (
	// ReportIssueURL is the repository's new-issue page.
	ReportIssueURL = "https://github.com/Protomothis/smartthings-pc-control/issues/new"
	// reportURLMax is the longest prefilled issue URL we open. Browsers
	// take far more, but GitHub answers a long query with an error page;
	// about 8 KB is where that starts.
	reportURLMax = 7000
	// ReportWarnMax is how many warning and error lines the issue shows.
	ReportWarnMax = 30
	// reportGUIWarnMax is how many of those may come from gui.log: the
	// service log is the one that usually says what went wrong.
	reportGUIWarnMax = 10
	// reportTitleMax is how many characters of the description go into the
	// issue title.
	reportTitleMax = 60
	// reportTitlePrefix starts every report's title, so the reports are
	// easy to find among the issues, in either language.
	reportTitlePrefix = "[문제] "
)

// reportDiagMax caps the diagnostic files in the zip (bytes); a CPU
// profile is a few hundred KB, a goroutine dump less. A variable for the
// tests.
var reportDiagMax int64 = 20 << 20

// ReportService is the service connection a report states.
type ReportService int

const (
	ReportServicePending ReportService = iota // still connecting
	ReportServiceOK                           // connected
	ReportServiceLogin                        // wants the secret
	ReportServiceLost                         // not answering
)

// ReportState is what the app knows when the report is made.
type ReportState struct {
	Lang       Lang
	AppVersion string
	Service    ReportService
	Port       int // the WebUI port, while connected
	Config     Config
	ConfigSet  bool
	Hub        STHub
	HubSet     bool
	System     SystemInfo
	// Snapshot is the state at send time (TakeStateSnapshot), nil until
	// measured; Measuring says a measurement is under way (the preview).
	Snapshot  *StateSnapshot
	Measuring bool
}

// ReportInclude is what the user chose to send.
type ReportInclude struct {
	PCInfo     bool // the PC facts, the settings summary, CPU and our processes now
	Processes  bool // the programs using the most CPU now (names only)
	ServiceLog bool // service.log and its rotations
	GUILog     bool // gui.log
	Diag       bool // the diag folders' profiles and dumps
}

// ReportIncludeAll is every part switched on, the page's default.
var ReportIncludeAll = ReportInclude{PCInfo: true, Processes: true, ServiceLog: true, GUILog: true, Diag: true}

// Report is one problem report. Build it with NewReport, which masks the
// description.
type Report struct {
	State       ReportState
	Description string
	Include     ReportInclude
	Created     time.Time
}

// NewReport is a report of desc (masked by m: people paste log lines). The
// snapshot's process names are masked too.
func NewReport(st ReportState, desc string, inc ReportInclude, created time.Time, m *Masker) Report {
	if st.Snapshot != nil {
		s := st.Snapshot.Masked(m)
		st.Snapshot = &s
	}
	return Report{State: st, Description: strings.TrimSpace(m.Text(desc)), Include: inc, Created: created}
}

// ReportMaskInput is what a report hides by value: the secret, a bot token
// the config holds in plain text, the chat ids, and the names that point
// at the person (PC, Windows user, the PC name set for Telegram).
func ReportMaskInput(cfg Config, sys SystemInfo) MaskInput {
	in := MaskInput{
		Secrets: []string{cfg.Secret},
		ChatIDs: append([]string{cfg.Telegram.ChatID}, cfg.Telegram.AllowedChatIDs...),
		Names:   []string{sys.PCName, sys.UserName, cfg.Telegram.PCName},
	}
	// The service sends the token masked ("****6789"); only an unsaved
	// edit could hold the real one.
	if t := cfg.Telegram.BotToken; t != "" && !strings.HasPrefix(t, "****") {
		in.Secrets = append(in.Secrets, t)
	}
	return in
}

// ReportRow is one line of a facts table.
type ReportRow struct{ Name, Value string }

// PCRows is the PC facts, in the report's language.
func (r Report) PCRows() []ReportRow {
	l, st := r.State.Lang, r.State
	sys := st.System
	rows := []ReportRow{
		{T(l, "ui.report.row.app"), st.AppVersion},
		{T(l, "ui.report.row.service"), r.serviceText()},
	}
	if sys.Windows != "" {
		rows = append(rows, ReportRow{"Windows", sys.Windows})
	}
	if sys.CPU != "" || sys.Cores > 0 {
		v := sys.CPU
		if sys.Cores > 0 {
			cores := fmt.Sprintf(T(l, "ui.report.cores"), sys.Cores)
			v = strings.TrimPrefix(v+" · "+cores, " · ")
		}
		rows = append(rows, ReportRow{"CPU", v})
	}
	if len(sys.GPUs) > 0 {
		rows = append(rows, ReportRow{"GPU", strings.Join(sys.GPUs, " / ")})
	}
	if sys.MemTotal > 0 {
		rows = append(rows, ReportRow{T(l, "ui.report.row.memory"),
			fmt.Sprintf(T(l, "ui.report.memory"), gib(sys.MemTotal), gib(sys.MemFree))})
	}
	if sys.Uptime > 0 {
		rows = append(rows, ReportRow{T(l, "ui.report.row.uptime"), reportUptime(l, sys.Uptime)})
	}
	return append(rows, ReportRow{T(l, "ui.report.row.lang"), T(l, "lang.name")})
}

func (r Report) serviceText() string {
	l := r.State.Lang
	switch r.State.Service {
	case ReportServiceOK:
		return fmt.Sprintf(T(l, "ui.report.svc.ok"), r.State.Port)
	case ReportServiceLogin:
		return T(l, "ui.report.svc.login")
	case ReportServiceLost:
		return T(l, "ui.report.svc.lost")
	}
	return T(l, "ui.report.svc.pending")
}

// gib is bytes in GB with one decimal.
func gib(b uint64) string { return fmt.Sprintf("%.1f", float64(b)/(1<<30)) }

// reportUptime is "3일 4시간" / "3d 4h"; under a day "4시간 12분".
func reportUptime(l Lang, d time.Duration) string {
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	if days > 0 {
		return fmt.Sprintf(T(l, "ui.report.uptime.days"), days, hours)
	}
	return fmt.Sprintf(T(l, "ui.report.uptime.hours"), hours, mins)
}

// SettingRows is the settings summary: on or off, counts and versions,
// never a value someone could use (the secret, the token, chat ids, preset
// paths).
func (r Report) SettingRows() []ReportRow {
	l, st := r.State.Lang, r.State
	if !st.ConfigSet {
		return []ReportRow{{T(l, "ui.report.row.settings"), T(l, "ui.report.unknown")}}
	}
	c := st.Config
	onOff := func(on bool) string {
		if on {
			return T(l, "home.on")
		}
		return T(l, "home.off")
	}
	hub := T(l, "ui.report.unknown")
	if st.HubSet {
		hub = T(l, "ui.report.hub.off")
		if st.Hub.Connected {
			hub = T(l, "ui.report.hub.on")
			if st.Hub.DriverVersion != "" {
				hub += " · " + fmt.Sprintf(T(l, "ui.report.hub.driver"), st.Hub.DriverVersion)
			}
		}
	}
	return []ReportRow{
		{T(l, "ui.report.set.media"), onOff(c.Media.Enabled)},
		{T(l, "ui.report.set.nowplaying"), onOff(c.Media.NowPlaying)},
		{T(l, "ui.report.set.activity"), onOff(c.Activity.Enabled)},
		{T(l, "ui.report.set.telegram"), onOff(c.Telegram.Enabled)},
		{T(l, "ui.report.set.tgcontrol"), onOff(c.Telegram.ControlEnabled)},
		{T(l, "ui.report.set.notifypc"), onOff(c.NotifyPC.Enabled)},
		{T(l, "ui.report.set.session"), onOff(c.SmartThings.ExposeSession)},
		{T(l, "ui.report.set.hub"), hub},
		{T(l, "ui.report.set.remote"), onOff(c.WebUIRemote)},
		{T(l, "ui.report.set.secret"), onOff(c.Secret != "")},
		{T(l, "ui.report.set.grace"), onOff(c.ShutdownGrace)},
		{T(l, "ui.report.set.presets"), fmt.Sprint(len(c.Presets))},
		{T(l, "ui.report.set.debug"), onOff(c.Debug)},
	}
}

// table is rows as a Markdown table under a header row.
func table(l Lang, rows []ReportRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "| %s | %s |\n|---|---|\n", T(l, "ui.report.col.name"), T(l, "ui.report.col.value"))
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", cell(r.Name), cell(r.Value))
	}
	return b.String()
}

// cell keeps a value from breaking its table row.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}

// description is the user's text, or a line saying there is none.
func (r Report) description() string {
	if r.Description == "" {
		return "_" + T(r.State.Lang, "ui.report.nodesc") + "_"
	}
	return r.Description
}

// IssueTitle is "[문제] " and the description's first line, cut.
func (r Report) IssueTitle() string {
	first := ""
	for ln := range strings.SplitSeq(r.Description, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			first = ln
			break
		}
	}
	if first == "" {
		first = T(r.State.Lang, "ui.report.title.default")
	}
	return reportTitlePrefix + cutRunes(first, reportTitleMax)
}

// cutRunes is s cut to n characters, with "…" when it was longer.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// --- the state at send time ---

// StateSection is the "보내는 순간의 상태" part (without its heading): the
// whole-PC CPU and our two processes with the PC info, then up to top of
// the busiest programs with the process list. "" when neither is included
// or there is nothing to show yet.
func (r Report) StateSection(top int) string {
	l, inc, snap := r.State.Lang, r.Include, r.State.Snapshot
	if !inc.PCInfo && !inc.Processes {
		return ""
	}
	if snap == nil {
		if r.State.Measuring {
			return "_" + T(l, "ui.report.measuring") + "_\n"
		}
		return ""
	}
	var b strings.Builder
	if inc.PCInfo {
		cpu := T(l, "ui.report.unknown")
		if snap.CPUOK {
			cpu = percent(snap.CPU)
		}
		b.WriteString(table(l, []ReportRow{
			{T(l, "ui.report.state.cpu"), cpu},
			{T(l, "ui.report.state.app"), procText(l, snap.App)},
			{T(l, "ui.report.state.service"), procText(l, snap.Service)},
		}))
	}
	if inc.Processes && top > 0 && len(snap.Top) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n\n| %s | CPU | %s |\n|---|---|---|\n", T(l, "ui.report.state.top"),
			T(l, "ui.report.state.program"), T(l, "ui.report.row.memory"))
		for i, p := range snap.Top {
			if i == top {
				break
			}
			name := p.Name
			if p.Count > 1 {
				name = fmt.Sprintf("%s ×%d", name, p.Count)
			}
			cpu := "—"
			if p.CPUOK {
				cpu = percent(p.CPU)
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(name), cpu, megabytes(p.WorkingSet))
		}
		if snap.Skipped > 0 {
			fmt.Fprintf(&b, "\n_%s_\n", fmt.Sprintf(T(l, "ui.report.state.skipped"), snap.Skipped))
		}
	}
	return b.String()
}

// procText is one of our processes in a line: CPU, memory, threads,
// handles, or why it could not be read.
func procText(l Lang, p ProcStat) string {
	if p.Err != "" {
		s := fmt.Sprintf(T(l, "ui.report.state.noread"), p.Err)
		if p.Threads > 0 {
			s += " · " + fmt.Sprintf(T(l, "ui.report.state.threads"), p.Threads)
		}
		return s
	}
	cpu := "—"
	if p.CPUOK {
		cpu = percent(p.CPU)
	}
	return fmt.Sprintf(T(l, "ui.report.state.proc"), cpu, megabytes(p.WorkingSet), megabytes(p.Private), p.Threads, p.Handles)
}

// percent is a share in percent with one decimal.
func percent(v float64) string { return fmt.Sprintf("%.1f%%", v) }

// megabytes is bytes as "45.2 MB", or "1.3 GB" from a gigabyte up.
func megabytes(b uint64) string {
	if b >= 1<<30 {
		return gib(b) + " GB"
	}
	return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
}

// issueParts is what the issue body is made of; IssueURL drops parts of
// it until the URL fits.
type issueParts struct {
	desc     string
	settings bool
	warn     []string
	top      int // rows of the busiest programs
}

// IssueBody is the issue text: warn is the masked warning/error lines
// (newest last), zipName the zip to drag onto the issue.
func (r Report) IssueBody(warn []string, zipName string) string {
	return r.issueBody(issueParts{desc: r.description(), settings: true, warn: warn, top: snapshotTop}, zipName)
}

func (r Report) issueBody(p issueParts, zipName string) string {
	l := r.State.Lang
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n%s\n\n", T(l, "ui.report.h.desc"), p.desc)
	if r.Include.PCInfo {
		fmt.Fprintf(&b, "### %s\n\n%s\n", T(l, "ui.report.h.pc"), table(l, r.PCRows()))
		if p.settings {
			fmt.Fprintf(&b, "<details><summary>%s</summary>\n\n%s\n</details>\n\n", T(l, "ui.report.h.settings"), table(l, r.SettingRows()))
		}
	}
	if s := r.StateSection(p.top); s != "" {
		fmt.Fprintf(&b, "### %s\n\n%s\n", T(l, "ui.report.h.state"), s)
	}
	if len(p.warn) > 0 {
		fmt.Fprintf(&b, "### %s\n\n```text\n%s\n```\n\n", T(l, "ui.report.h.warn"), strings.Join(p.warn, "\n"))
	}
	fmt.Fprintf(&b, "### %s\n\n%s\n", T(l, "ui.report.h.attach"), fmt.Sprintf(T(l, "ui.report.attach"), zipName))
	return b.String()
}

// IssueURL is the new-issue page with the title and body filled in, kept
// under reportURLMax: the oldest warning lines go first, then the busiest
// programs from the bottom of the list, then the settings summary, then
// the description is cut.
func (r Report) IssueURL(warn []string, zipName string) string {
	title := r.IssueTitle()
	p := issueParts{desc: r.description(), settings: true, warn: warn, top: snapshotTop}
	build := func() string { return issueURL(title, r.issueBody(p, zipName)) }
	u := build()
	for len(u) > reportURLMax && len(p.warn) > 0 {
		p.warn = p.warn[1:]
		u = build()
	}
	for len(u) > reportURLMax && p.top > 0 {
		p.top--
		u = build()
	}
	if len(u) > reportURLMax {
		p.settings = false
		u = build()
	}
	// The description goes last: cut it a little at a time (an encoded
	// Hangul character is 9 bytes of URL).
	for len(u) > reportURLMax && utf8.RuneCountInString(p.desc) > 1 {
		over := (len(u) - reportURLMax) / 9
		n := max(utf8.RuneCountInString(p.desc)-max(over, 16), 1)
		p.desc = cutRunes(p.desc, n)
		u = build()
	}
	return u
}

// issueURL encodes title and body as the query. Spaces as %20: a '+'
// reads as a plus in some places a URL is pasted to.
func issueURL(title, body string) string {
	enc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	return ReportIssueURL + "?title=" + enc(title) + "&body=" + enc(body)
}

// Markdown is report.md: the description, the PC facts and settings (when
// included), and the files in the zip.
func (r Report) Markdown(files []string) string {
	l := r.State.Lang
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", r.IssueTitle())
	fmt.Fprintf(&b, "%s: %s\n\n", T(l, "ui.report.created"), r.Created.Format("2006-01-02 15:04 -0700"))
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", T(l, "ui.report.h.desc"), r.description())
	if r.Include.PCInfo {
		fmt.Fprintf(&b, "## %s\n\n%s\n", T(l, "ui.report.h.pc"), table(l, r.PCRows()))
		fmt.Fprintf(&b, "## %s\n\n%s\n", T(l, "ui.report.h.settings"), table(l, r.SettingRows()))
	}
	if s := r.StateSection(snapshotTop); s != "" {
		fmt.Fprintf(&b, "## %s\n\n%s\n", T(l, "ui.report.h.state"), s)
	}
	fmt.Fprintf(&b, "## %s\n\n", T(l, "ui.report.h.files"))
	for _, f := range files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	return b.String()
}

// ReportZipName is the zip's file name for t: stpc-report-20261121-1530.zip.
func ReportZipName(t time.Time) string {
	return "stpc-report-" + t.Format("20060102-1504") + ".zip"
}

// --- collecting ---

// ReportDiagDir is a folder of diagnostic files and the folder they go to
// in the zip.
type ReportDiagDir struct{ Dir, Zip string }

// ReportSources is where a report's files come from. ReadFile and ReadDir
// are os.ReadFile and os.ReadDir but in the tests.
type ReportSources struct {
	// ServiceLogs is service.log and its rotations (.1 … .3), current
	// first.
	ServiceLogs []string
	GUILog      string
	DiagDirs    []ReportDiagDir
	ReadFile    func(string) ([]byte, error)
	ReadDir     func(string) ([]fs.DirEntry, error)
}

// DefaultReportSources is this install's logs and the two diag folders:
// %LOCALAPPDATA%\SmartThings PC Control\diag (the tray's) and diag next
// to the exe (the service's).
func DefaultReportSources() ReportSources {
	src := ReportSources{GUILog: guiLogPath(), ReadFile: os.ReadFile, ReadDir: os.ReadDir}
	if p, err := ServiceLogPath(); err == nil {
		src.ServiceLogs = serviceLogSet(p)
		src.DiagDirs = append(src.DiagDirs, ReportDiagDir{filepath.Join(filepath.Dir(p), "diag"), "diag/service"})
	}
	if d := userDataDir(); d != "" {
		src.DiagDirs = append([]ReportDiagDir{{filepath.Join(d, "diag"), "diag/app"}}, src.DiagDirs...)
	}
	return src
}

// serviceLogSet is service.log and the names logx rotates it to.
func serviceLogSet(path string) []string {
	return []string{path, path + ".1", path + ".2", path + ".3"}
}

// ReportFile is one file of the zip.
type ReportFile struct {
	Name     string
	Data     []byte
	Modified time.Time
}

// ReportWarnings is the latest warning and error lines of the two logs,
// masked, oldest first.
type ReportWarnings struct {
	Service, GUI []string
}

// ReadReportWarnings reads the warning and error lines of the current
// service.log and gui.log (the preview shows them before anything is
// saved).
func ReadReportWarnings(src ReportSources, m *Masker) ReportWarnings {
	var svc, gl []byte
	if len(src.ServiceLogs) > 0 {
		svc, _ = src.ReadFile(src.ServiceLogs[0])
	}
	if src.GUILog != "" {
		gl, _ = src.ReadFile(src.GUILog)
	}
	return warnings(string(svc), string(gl), m)
}

// warnings picks the warning and error lines of the two logs.
func warnings(svc, gl string, m *Masker) ReportWarnings {
	return ReportWarnings{
		Service: m.Lines(lastWarn(svc, ReportWarnMax)),
		GUI:     m.Lines(lastWarn(gl, reportGUIWarnMax)),
	}
}

// Lines is the issue's warning lines for what inc includes: the last of
// the service's, then the app's (marked "gui:"), ReportWarnMax at most.
func (w ReportWarnings) Lines(inc ReportInclude) []string {
	var g []string
	if inc.GUILog {
		g = w.GUI
	}
	var out []string
	if inc.ServiceLog {
		s := w.Service
		if n := ReportWarnMax - len(g); len(s) > n {
			s = s[len(s)-n:]
		}
		out = append(out, s...)
	}
	for _, ln := range g {
		out = append(out, "gui: "+ln)
	}
	return out
}

// lastWarn is the last n lines of text that are warnings or errors, as the
// logs page tells them.
func lastWarn(text string, n int) []string {
	var out []string
	for _, ln := range splitLines(text) {
		_, msg := LogCells(ln)
		if LogLineLevel(msg)&(LogLevelError|LogLevelWarn) != 0 || strings.Contains(ln, "WARNING") {
			out = append(out, strings.TrimSpace(ln))
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func splitLines(text string) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// CollectReport reads what r includes into the zip's files, masked, with
// report.md first, and the warning lines for the issue. A file that is
// missing or cannot be read is left out (report.md lists what is in).
func CollectReport(r Report, src ReportSources, m *Masker) (files []ReportFile, warn []string) {
	var svcText, guiText string
	if r.Include.ServiceLog {
		for i, p := range src.ServiceLogs {
			data, err := src.ReadFile(p)
			if err != nil {
				continue
			}
			if i == 0 {
				svcText = string(data)
			}
			files = append(files, ReportFile{Name: filepath.Base(p), Data: []byte(m.Text(string(data)))})
		}
	}
	if r.Include.GUILog && src.GUILog != "" {
		if data, err := src.ReadFile(src.GUILog); err == nil {
			guiText = string(data)
			files = append(files, ReportFile{Name: "gui.log", Data: []byte(m.Text(guiText))})
		}
	}
	if r.Include.Diag {
		files = append(files, diagFiles(src)...)
	}
	names := make([]string, 0, len(files)+1)
	names = append(names, "report.md")
	for _, f := range files {
		names = append(names, f.Name)
	}
	md := ReportFile{Name: "report.md", Data: []byte(r.Markdown(names)), Modified: r.Created}
	return append([]ReportFile{md}, files...), warnings(svcText, guiText, m).Lines(r.Include)
}

// diagFiles is the diag folders' files, newest first, as long as they fit
// in reportDiagMax together. They are included as they are: a profile is
// binary, and a goroutine dump holds code locations, not user data.
func diagFiles(src ReportSources) []ReportFile {
	type cand struct {
		path, name string
		size       int64
		mod        time.Time
	}
	var cands []cand
	for _, d := range src.DiagDirs {
		entries, err := src.ReadDir(d.Dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			cands = append(cands, cand{filepath.Join(d.Dir, e.Name()), d.Zip + "/" + e.Name(), info.Size(), info.ModTime()})
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return b.mod.Compare(a.mod) })
	var out []ReportFile
	var total int64
	for _, c := range cands {
		if total+c.size > reportDiagMax {
			continue // an older, smaller one may still fit
		}
		data, err := src.ReadFile(c.path)
		if err != nil || total+int64(len(data)) > reportDiagMax {
			continue
		}
		total += int64(len(data))
		out = append(out, ReportFile{Name: c.name, Data: data, Modified: c.mod})
	}
	return out
}

// WriteReportZip writes files as a zip to w.
func WriteReportZip(w io.Writer, files []ReportFile) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified}
		if h.Modified.IsZero() {
			h.Modified = time.Now()
		}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// SaveReportZip writes files as name into the first of dirs that takes
// it, and returns the path. A name already taken there gets "-2", "-3" ….
func SaveReportZip(dirs []string, name string, files []ReportFile) (string, error) {
	var errs []error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		path, err := saveZipIn(dir, name, files)
		if err == nil {
			return path, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return "", errors.New("no folder to save the report in")
	}
	return "", errors.Join(errs...)
}

func saveZipIn(dir, name string, files []ReportFile) (string, error) {
	base := strings.TrimSuffix(name, ".zip")
	for i := 1; i < 100; i++ {
		path := filepath.Join(dir, name)
		if i > 1 {
			path = filepath.Join(dir, fmt.Sprintf("%s-%d.zip", base, i))
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		werr := WriteReportZip(f, files)
		cerr := f.Close()
		if err := errors.Join(werr, cerr); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("%s: too many reports with this name", dir)
}

// ReportSaveDirs is where the zip goes, in order of preference: the
// Desktop (easy to find and drag from), Downloads, then temp.
func ReportSaveDirs() []string {
	var dirs []string
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Desktop, windows.FOLDERID_Downloads} {
		if p, err := windows.KnownFolderPath(id, 0); err == nil && p != "" {
			dirs = append(dirs, p)
		}
	}
	return append(dirs, os.TempDir())
}
