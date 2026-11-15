package gui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Text tests of the Fyne app, ported when its widgets went (#165): they
// called the window's (*langTexts) wrappers, now the package functions the Gio
// UI uses, through langTexts.

func TestAwakePresetLabels(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	want := []string{"30분", "1시간", "2시간", "4시간", "끌 때까지"}
	for i, m := range awakePresets {
		if got := ko.awakePresetLabel(m); got != want[i] {
			t.Errorf("label(%d) = %q, want %q", m, got, want[i])
		}
	}
	if got := (&langTexts{lang: LangEn}).awakePresetLabel(0); got != "Until turned off" {
		t.Errorf("en label(0) = %q", got)
	}
}

func TestAwakeStatusText(t *testing.T) {
	now := time.Date(2026, 9, 30, 13, 48, 0, 0, time.Local)
	ko := &langTexts{lang: LangKo}
	en := &langTexts{lang: LangEn}
	until := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	for _, tc := range []struct {
		u    *langTexts
		a    Awake
		want string
	}{
		{ko, Awake{}, "꺼짐 — 평소 절전 설정을 따라요"},
		{ko, Awake{On: true}, "켜짐 · 끌 때까지"},
		{ko, Awake{On: true, Until: until(42 * time.Minute), RemainingSeconds: 42 * 60}, "42분 남음 · 14:30까지"},
		// Seconds round up, never "0분".
		{ko, Awake{On: true, Until: until(20 * time.Second), RemainingSeconds: 20}, "1분 남음 · 13:48까지"},
		{ko, Awake{On: true, Until: until(90*time.Minute + 30*time.Second), RemainingSeconds: 90*60 + 30}, "1시간 31분 남음 · 15:18까지"},
		{ko, Awake{On: true, Until: until(12 * time.Hour), RemainingSeconds: 12 * 3600}, "12시간 남음 · 내일 01:48까지"},
		{en, Awake{On: true, Until: until(2 * time.Hour), RemainingSeconds: 7200}, "2 h left · until 15:48"},
		{en, Awake{On: true}, "On · until turned off"},
	} {
		if got := tc.u.awakeStatusText(tc.a, now); got != tc.want {
			t.Errorf("%s %+v = %q, want %q", tc.u.lang, tc.a, got, tc.want)
		}
	}
}

func TestBatteryText(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	en := &langTexts{lang: LangEn}
	for _, tc := range []struct {
		u    *langTexts
		b    Battery
		want string
	}{
		{ko, Battery{Present: false, Percent: -1, AC: true}, ""},
		{ko, Battery{}, ""},
		{ko, Battery{Present: true, Percent: 80, Charging: true, AC: true}, "배터리 80% · 충전 중"},
		{ko, Battery{Present: true, Percent: 100, AC: true}, "배터리 100% · 전원 연결됨"},
		{ko, Battery{Present: true, Percent: 15}, "배터리 15%"},
		{ko, Battery{Present: true, Percent: -1}, "배터리 잔량 알 수 없음"},
		{en, Battery{Present: true, Percent: 42, Charging: true, AC: true}, "Battery 42% · charging"},
		{en, Battery{Present: true, Percent: 99, AC: true}, "Battery 99% · plugged in"},
	} {
		if got := tc.u.batteryText(tc.b); got != tc.want {
			t.Errorf("%s %+v = %q, want %q", tc.u.lang, tc.b, got, tc.want)
		}
	}
}

func TestFormatMinutesClimbsTheUnits(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	en := &langTexts{lang: LangEn}
	cases := []struct {
		minutes      int
		wantKo, want string
	}{
		{5, "5분", "5 min"},
		{45, "45분", "45 min"},
		{60, "1시간", "1 h"},
		{90, "1시간 30분", "1 h 30 min"},
		{720, "12시간", "12 h"},
		{1440, "1일", "1 d"},
		{2880, "2일", "2 d"},
		{4320, "3일", "3 d"},
		{1620, "1일 3시간", "1 d 3 h"},
	}
	for _, tc := range cases {
		if got := ko.formatMinutes(tc.minutes); got != tc.wantKo {
			t.Errorf("ko formatMinutes(%d) = %q, want %q", tc.minutes, got, tc.wantKo)
		}
		if got := en.formatMinutes(tc.minutes); got != tc.want {
			t.Errorf("en formatMinutes(%d) = %q, want %q", tc.minutes, got, tc.want)
		}
	}
}

// The big countdown block: mm:ss under an hour, hh:mm:ss under a day and the
// days in front of it beyond that (#89) — "72:00:00" reads as a stopwatch.
func TestFormatCountdownCarriesTheDays(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{-time.Second, "00:00"},
		{90 * time.Second, "01:30"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour, "1:00:00"},
		{3*time.Hour + 15*time.Minute, "3:15:00"},
		{51*time.Hour + 15*time.Minute, "2일 3:15:00"},
		{72 * time.Hour, "3일 0:00:00"},
	}
	for _, tc := range cases {
		if got := ko.formatCountdown(tc.d); got != tc.want {
			t.Errorf("formatCountdown(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := (&langTexts{lang: LangEn}).formatCountdown(51*time.Hour + 15*time.Minute); got != "2d 3:15:00" {
		t.Errorf("en formatCountdown = %q, want %q", got, "2d 3:15:00")
	}
}

// The command port's firewall rule line (#139).
func TestSTCommandPortLine(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	if got := ko.stCommandPortLine(nil, 5001); got != "" {
		t.Errorf("a service without the field: %q, want the line hidden", got)
	}
	if got := ko.stCommandPortLine(&STCommandPort{}, 5001); got != T(LangKo, "st.cmdport.checking") {
		t.Errorf("before the check: %q", got)
	}
	cases := []struct {
		name  string
		state STCommandPort
		saved int
		ko    string
		en    string
	}{
		{"rule ok", STCommandPort{Port: 5001, FirewallRule: true, Checked: true}, 5001,
			"명령 포트 TCP 5001 · 방화벽 규칙 OK",
			"Command port TCP 5001 · firewall rule OK"},
		{"rule missing", STCommandPort{Port: 5001, Checked: true}, 0,
			"명령 포트 TCP 5001 · 방화벽 규칙 없음 (허브가 이 PC에 닿지 못할 수 있어요, 로그 확인)",
			"Command port TCP 5001 · no firewall rule (the hub may not reach this PC, check the log)"},
		{"saved port not applied yet", STCommandPort{Port: 5001, FirewallRule: true, Checked: true}, 5005,
			"명령 포트 TCP 5001 · 방화벽 규칙 OK · 저장한 포트 5005는 서비스를 다시 시작하면 적용돼요",
			"Command port TCP 5001 · firewall rule OK · the saved port 5005 applies after a service restart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ko.stCommandPortLine(&c.state, c.saved); got != c.ko {
				t.Errorf("ko = %q\nwant %q", got, c.ko)
			}
			if got := (&langTexts{lang: LangEn}).stCommandPortLine(&c.state, c.saved); got != c.en {
				t.Errorf("en = %q\nwant %q", got, c.en)
			}
		})
	}
}

func TestSTSearchLine(t *testing.T) {
	now := time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC)
	at := now.Add(-12 * time.Second).Format(time.RFC3339)
	search := &STLastSearch{IP: "192.168.1.105", At: at}

	cases := []struct {
		name   string
		state  STSSDPState
		ko, en string
	}{
		{
			name:  "everything in order",
			state: STSSDPState{Running: true, FirewallRule: true, LastSearch: search},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 OK · 마지막 검색 요청 192.168.1.105, 12초 전",
			en:    "Discovery responder on · Firewall rule OK · Last search from 192.168.1.105, 12s ago",
		},
		{
			name:  "no search has arrived yet",
			state: STSSDPState{Running: true, FirewallRule: true},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 OK · 검색 요청 없음",
			en:    "Discovery responder on · Firewall rule OK · No search yet",
		},
		{
			name:  "the firewall rule is missing",
			state: STSSDPState{Running: true},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 없음 (서비스를 다시 시작하면 만들어요) · 검색 요청 없음",
			en:    "Discovery responder on · No firewall rule (restart the service to create it) · No search yet",
		},
		{
			// A responder with no socket answers nothing, so the rest of
			// the line would only distract.
			name:  "the responder could not start",
			state: STSSDPState{Running: false, FirewallRule: true, LastSearch: search},
			ko:    "검색 응답기 꺼짐 (소켓 열기 실패, 로그 확인)",
			en:    "Discovery responder off (no socket could be opened, check the log)",
		},
		{
			// An older service that does not send last_search yet, and a
			// malformed one: neither may print a bare "%!s(MISSING)".
			name:  "a last_search with no address",
			state: STSSDPState{Running: true, FirewallRule: true, LastSearch: &STLastSearch{At: at}},
			ko:    "검색 응답기 켜짐 · 방화벽 규칙 OK · 검색 요청 없음",
			en:    "Discovery responder on · Firewall rule OK · No search yet",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (&langTexts{lang: LangKo}).stSearchLine(c.state, now); got != c.ko {
				t.Errorf("ko = %q\nwant %q", got, c.ko)
			}
			if got := (&langTexts{lang: LangEn}).stSearchLine(c.state, now); got != c.en {
				t.Errorf("en = %q\nwant %q", got, c.en)
			}
		})
	}

	// An unparseable timestamp falls back instead of printing a zero time.
	got := (&langTexts{lang: LangKo}).stSearchLine(STSSDPState{
		Running: true, FirewallRule: true, LastSearch: &STLastSearch{IP: "10.0.0.2"},
	}, now)
	if !strings.Contains(got, T(LangKo, "st.rel.unknown")) || strings.Contains(got, "%") {
		t.Errorf("a last_search with no time renders as %q", got)
	}
}

func TestMediaLines(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	cases := []struct {
		m         MediaInfo
		share     bool
		main, sub string
	}{
		{MediaInfo{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", App: "Spotify"}, true, "Hype Boy", "NewJeans · Spotify"},
		{MediaInfo{Status: "paused", Title: "Ditto"}, true, "Ditto", ""},
		{MediaInfo{Status: "playing", Title: "Lo-fi mix", App: "Chrome"}, true, "Lo-fi mix", "Chrome"},
		{MediaInfo{Status: "playing", Artist: "NewJeans", App: "Chrome"}, true, "NewJeans", "Chrome"},
		{MediaInfo{Status: "playing", App: "VLC"}, true, "재생 중", "VLC"},
		{MediaInfo{Status: "playing", Title: "stale", App: "Spotify"}, false, "재생 중", ""},
		{MediaInfo{Status: "none"}, true, "재생 중인 미디어 없음", ""},
	}
	for _, c := range cases {
		if m, s := ko.mediaLines(c.m, c.share); m != c.main || s != c.sub {
			t.Errorf("mediaLines(%+v, %v) = %q, %q; want %q, %q", c.m, c.share, m, s, c.main, c.sub)
		}
	}
}

func TestMediaToggleCommand(t *testing.T) {
	for status, want := range map[string]string{
		"playing": "pause", "paused": "play", "stopped": "play", "none": "playpause", "": "playpause",
	} {
		if got := MediaToggleCommand(status); got != want {
			t.Errorf("MediaToggleCommand(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestMediaLevelText(t *testing.T) {
	v := 42
	if got := MediaLevelText(MediaAudio{Available: true, Volume: &v}); got != "42%" {
		t.Errorf("level = %q", got)
	}
	if got := MediaLevelText(MediaAudio{}); got != "—" {
		t.Errorf("unknown level = %q", got)
	}
}

func TestMediaReason(t *testing.T) {
	ko := &langTexts{lang: LangKo}
	on := MediaState{Enabled: true, Session: true}
	cases := []struct {
		m    MediaState
		err  error
		want string
	}{
		{on, nil, ""},
		{MediaState{Session: true}, nil, "서비스 설정에서 '볼륨·미디어 제어'를 켜면 쓸 수 있어요"},
		{MediaState{Enabled: true}, nil, "로그인한 사용자가 없어 쓸 수 없어요"},
		{MediaState{}, errMediaUnsupported, "미디어 카드는 서비스 v1.2.0 이상이 필요해요"},
	}
	for _, c := range cases {
		if got := ko.mediaReason(c.m, c.err); got != c.want {
			t.Errorf("mediaReason(%+v, %v) = %q, want %q", c.m, c.err, got, c.want)
		}
	}
}

func TestReadServiceLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("a\r\nb\nc\r\n\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadServiceLogTail(path, 2); err != nil || !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("tail = %q, %v", got, err)
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadServiceLogTail(empty, 2); err != nil || len(got) != 0 {
		t.Errorf("empty file = %q, %v", got, err)
	}
}

// A log the user may not read is explained with its path.
func TestLogsFileNoteForPermission(t *testing.T) {
	u := &langTexts{lang: LangKo}
	const path = `C:\Program Files\SmartThings PC Control\service.log`
	err := &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	if got, want := u.logsFileNote(path, err), fmt.Sprintf(T(LangKo, "logs.file.denied"), path); got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

// logsShift finds how far the old lines moved up.
func TestLogsShift(t *testing.T) {
	l := logLinesN(6)
	cases := []struct {
		name       string
		old, lines []string
		want       int
	}{
		{"same", l[:4], l[:4], 0},
		{"appended", l[:4], l, 0},
		{"slid by one", l[:5], l[1:6], 1},
		{"slid by two", l[:4], l[2:6], 2},
		{"unrelated", l[:3], []string{"x", "y"}, 0},
		{"from nothing", nil, l, 0},
		{"to a subset", l, l[3:4], 0},
	}
	for _, c := range cases {
		if got := LogsShift(c.old, c.lines); got != c.want {
			t.Errorf("%s: logsShift = %d, want %d", c.name, got, c.want)
		}
	}
}

// langTexts stands in for the Fyne window's language-bound text helpers.
type langTexts struct{ lang Lang }

func (u *langTexts) awakePresetLabel(m int) string { return AwakePresetLabel(u.lang, m) }
func (u *langTexts) awakeStatusText(a Awake, now time.Time) string {
	return AwakeStatusText(u.lang, a, now)
}
func (u *langTexts) batteryText(b Battery) string           { return BatteryText(u.lang, b) }
func (u *langTexts) formatMinutes(m int) string             { return FormatMinutes(u.lang, m) }
func (u *langTexts) formatCountdown(d time.Duration) string { return FormatCountdown(u.lang, d) }
func (u *langTexts) stCommandPortLine(c *STCommandPort, saved int) string {
	return STCommandPortLine(u.lang, c, saved)
}
func (u *langTexts) stSearchLine(s STSSDPState, now time.Time) string {
	return STSearchLine(u.lang, s, now)
}
func (u *langTexts) mediaLines(m MediaInfo, share bool) (string, string) {
	return MediaLines(u.lang, m, share)
}
func (u *langTexts) mediaReason(m MediaState, err error) string { return MediaReason(u.lang, m, err) }
func (u *langTexts) logsFileNote(path string, err error) string {
	return LogsFileNote(u.lang, path, err)
}

// logLinesN is n service log lines, far more than the view's height.
func logLinesN(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026/10/04 13:%02d:%02d SSDP: answered 10.0.0.%d (ST upnp:rootdevice)", i/60, i%60, i)
	}
	return lines
}
