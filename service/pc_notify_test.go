package service

// Tests for PC notification (#106): text rules, the rate limit, the
// /st/v1/notify and /api/notify/test endpoints over an injected runner.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func TestCleanNotifyText(t *testing.T) {
	for in, want := range map[string]string{
		"빨래 끝":                       "빨래 끝",
		"  a  b  ":                   "a b",
		"line1\nline2\r\nline3\tx":   "line1 line2 line3 x",
		"bell\a esc\x1b[2J null\x00": "bell esc[2J null",
		"del\x7f c1\u0085":           "del c1", // U+0085 is a space to Go, and a control
		"rtl ‮evil‬ mark‏":           "rtl evil mark",
		"\xff\xfeok":                 "ok",
		"\n\t ":                      "",
		"$(calc) & <b>":              "$(calc) & <b>", // not ours to escape: the toast XML does
	} {
		if got := cleanNotifyText(in); got != want {
			t.Errorf("cleanNotifyText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrepareNotify(t *testing.T) {
	title, text, err := prepareNotify("", " 빨래 끝\n")
	if err != nil || title != pcNotifyDefaultTitle || text != "빨래 끝" {
		t.Errorf("default title: %q %q %v", title, text, err)
	}
	if _, text, err := prepareNotify("t", strings.Repeat("가", 200)); err != nil || len([]rune(text)) != 200 {
		t.Errorf("200 characters: %v", err)
	}
	// Control characters do not count: 200 visible characters plus noise.
	if _, _, err := prepareNotify("t", strings.Repeat("가", 200)+"\x00\x01"); err != nil {
		t.Errorf("200 + controls: %v", err)
	}
	for name, c := range map[string][2]string{
		"empty":      {"t", ""},
		"blank":      {"t", " \n\t "},
		"only ctrl":  {"t", "\x00\x1b"},
		"201":        {"t", strings.Repeat("a", 201)},
		"long title": {strings.Repeat("a", 101), "x"},
	} {
		_, _, err := prepareNotify(c[0], c[1])
		var ne *pcNotifyError
		if !errors.As(err, &ne) || ne.Code != "bad_text" {
			t.Errorf("%s: err = %v, want bad_text", name, err)
		}
	}
}

func TestNotifyLimiter(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	l := newNotifyLimiter(func() time.Time { return now })
	for i := 0; i < pcNotifyPerMinute; i++ {
		if ok, _ := l.allow("ip 1"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
		now = now.Add(time.Second)
	}
	ok, wait := l.allow("ip 1")
	if ok {
		t.Fatal("11th request in a minute allowed")
	}
	// The first hit was at 12:00:00; the window frees at 12:01:00, and it
	// is 12:00:10 now.
	if wait != 50*time.Second {
		t.Errorf("retry after %v, want 50s", wait)
	}
	if ok, _ := l.allow("ip 2"); !ok {
		t.Error("another source shares the limit")
	}
	now = now.Add(50 * time.Second)
	if ok, _ := l.allow("ip 1"); !ok {
		t.Error("still refused once the oldest hit left the window")
	}
	if ok, _ := l.allow("ip 1"); ok {
		t.Error("the window should be full again")
	}
}

// fakeNotifyRun replaces the user-action runner and the rate limiter.
func fakeNotifyRun(t *testing.T, reply string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	saved, savedLimits := pcNotifyRun, pcNotifyLimits
	pcNotifyRun = func(ctx context.Context, args ...string) (UserActionResult, error) {
		calls = append(calls, args)
		if err != nil {
			return UserActionResult{}, err
		}
		return parseUserActionOutput([]byte(reply))
	}
	pcNotifyLimits = newNotifyLimiter(time.Now)
	t.Cleanup(func() { pcNotifyRun, pcNotifyLimits = saved, savedLimits })
	return &calls
}

func notifyCfg(enabled, speak bool, voice string) Config {
	return Config{Port: 5001, NotifyPC: NotifyPCConfig{Enabled: enabled, Speak: speak, Voice: voice}}
}

func TestSTNotifyOK(t *testing.T) {
	stSetup(t, notifyCfg(true, false, "Heami"))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)

	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"빨래가 끝났습니다\n","speak":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := stJSON(t, w)
	if body["ok"] != true || body["toast"] != "shown" || body["spoken"] != false {
		t.Errorf("body = %v", body)
	}
	// speak was asked for, but notify_pc.speak is off: no --speak.
	want := []string{"notify", "--title", "SmartThings", "--text", "빨래가 끝났습니다"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("args = %q, want %q", *calls, want)
	}
}

func TestSTNotifySpeakFollowsConfig(t *testing.T) {
	stSetup(t, notifyCfg(true, true, "Heami"))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown","spoken":true,"voice_used":"Microsoft Heami Desktop - Korean","voice_found":true}`, nil)

	body := stJSON(t, stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"title":"세탁기","text":"끝","speak":true}`))
	if body["spoken"] != true || body["voice_used"] != "Microsoft Heami Desktop - Korean" || body["voice_found"] != true {
		t.Errorf("body = %v", body)
	}
	want := []string{"notify", "--title", "세탁기", "--text", "끝", "--speak", "--voice", "Heami"}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("args = %q, want %q", (*calls)[0], want)
	}
	// Not asked for: not spoken even though speech is on.
	stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"조용히"}`)
	if got := (*calls)[1]; len(got) != 5 {
		t.Errorf("args without speak = %q", got)
	}
}

func TestSTNotifyRefusals(t *testing.T) {
	stSetup(t, notifyCfg(false, false, ""))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)

	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`)
	if w.Code != http.StatusForbidden || stJSON(t, w)["error"] != "notify_disabled" {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}

	setConfig(notifyCfg(true, false, ""))
	for name, body := range map[string]string{
		"no text":    `{}`,
		"blank":      `{"text":" \n "}`,
		"too long":   fmt.Sprintf(`{"text":%q}`, strings.Repeat("a", 201)),
		"bad json":   `{"text":`,
		"text array": `{"text":["a"]}`,
	} {
		if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, w.Code)
		}
	}
	if w := stDo(t, "GET", "/st/v1/notify", "192.168.1.20", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d", w.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("refused requests reached the user session: %q", *calls)
	}
}

func TestSTNotifyRateLimit(t *testing.T) {
	stSetup(t, notifyCfg(true, false, ""))
	fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)
	for i := 0; i < pcNotifyPerMinute; i++ {
		resetSTRateLimit() // the per-second /st/v1 bucket is not what is tested
		if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`); w.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, w.Code)
		}
	}
	resetSTRateLimit()
	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`)
	if w.Code != http.StatusTooManyRequests || stJSON(t, w)["error"] != "rate_limited" {
		t.Fatalf("11th: %d %s", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q", ra)
	}
	resetSTRateLimit()
	if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.21", "", `{"text":"hi"}`); w.Code != http.StatusOK {
		t.Errorf("another hub: status %d", w.Code)
	}
}

func TestSTNotifyUserSessionErrors(t *testing.T) {
	stSetup(t, notifyCfg(true, false, ""))
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("get session: %w", errNoUserSession), http.StatusConflict, "no_user_session"},
		{fmt.Errorf("%w after 3s", errUserActionTimeout), http.StatusGatewayTimeout, "timeout"},
		{&userActionError{Code: "unsupported", Message: "notify is not available"}, http.StatusNotImplemented, "unsupported"},
		{&userActionError{Code: "failed", Message: "toast: exit status 1"}, http.StatusBadGateway, "failed"},
		{errUserActionOutput, http.StatusBadGateway, "failed"},
	} {
		fakeNotifyRun(t, "", c.err)
		resetSTRateLimit()
		w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`)
		if w.Code != c.status || stJSON(t, w)["error"] != c.code {
			t.Errorf("%v: %d %s, want %d %s", c.err, w.Code, w.Body.String(), c.status, c.code)
		}
	}
}

func TestSTNotifyNeedsTheSecret(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cret", NotifyPC: NotifyPCConfig{Enabled: true}})
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)
	if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("no secret: %d", w.Code)
	}
	if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "s3cret", `{"text":"hi"}`); w.Code != http.StatusOK {
		t.Errorf("with secret: %d", w.Code)
	}
	if len(*calls) != 1 {
		t.Errorf("calls = %q", *calls)
	}
}

func TestSTStatusNotifyFeature(t *testing.T) {
	stSetup(t, notifyCfg(true, false, ""))
	stubAwake(t)
	if f := fmt.Sprint(stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))["features"]); !strings.Contains(f, "notify") {
		t.Errorf("enabled: features = %s, want notify", f)
	}
	// Still listed while off: the driver sends, gets 403 notify_disabled and
	// says "PC 알림 꺼짐" rather than "not supported".
	setConfig(notifyCfg(false, false, ""))
	if f := fmt.Sprint(stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))["features"]); !strings.Contains(f, "notify") {
		t.Errorf("disabled: features = %s, want notify", f)
	}
}

func TestNotifyTestAPI(t *testing.T) {
	withLiveConfig(t, notifyCfg(false, false, "")) // off: the test button still works
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown","spoken":true,"voice_used":"Zira"}`, nil)

	w := httptest.NewRecorder()
	handleNotifyTestAPI(w, postJSON("/api/notify/test", `{"speak":true,"voice":"Zira"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	if body["status"] != "ok" || body["spoken"] != true || body["voice_used"] != "Zira" {
		t.Errorf("body = %v", body)
	}
	args := (*calls)[0]
	if args[len(args)-3] != "--speak" || args[len(args)-1] != "Zira" {
		t.Errorf("args = %q: the form's speech settings were not used", args)
	}

	// No CSRF header: refused before anything runs.
	w = httptest.NewRecorder()
	handleNotifyTestAPI(w, httptest.NewRequest("POST", "/api/notify/test", strings.NewReader(`{}`)))
	if w.Code != http.StatusForbidden || len(*calls) != 1 {
		t.Errorf("without CSRF header: %d, calls %d", w.Code, len(*calls))
	}

	fakeNotifyRun(t, "", errNoUserSession)
	w = httptest.NewRecorder()
	handleNotifyTestAPI(w, postJSON("/api/notify/test", `{}`))
	if w.Code != http.StatusConflict || decodeBody(t, w)["error"] != "no_user_session" {
		t.Errorf("no session: %d %s", w.Code, w.Body.String())
	}
}

func TestNotifyArgsParse(t *testing.T) {
	for _, c := range []struct {
		speak bool
		voice string
	}{{false, ""}, {true, ""}, {true, "Microsoft Heami Desktop - Korean"}, {false, "ignored"}} {
		args := notifyArgs("SmartThings", "--text is text", c.speak, c.voice)
		req, err := useraction.Parse(args)
		if err != nil {
			t.Errorf("%q: %v", args, err)
			continue
		}
		wantVoice := ""
		if c.speak {
			wantVoice = c.voice
		}
		if req.Text != "--text is text" || req.Speak != c.speak || req.Voice != wantVoice {
			t.Errorf("%q parsed as %+v", args, req)
		}
	}
}

func TestValidateNotifyPC(t *testing.T) {
	if msg := validateNotifyPC(NotifyPCConfig{Voice: "Microsoft Heami Desktop - Korean"}); msg != "" {
		t.Error(msg)
	}
	for _, v := range []string{strings.Repeat("a", useraction.MaxVoiceRunes+1), "a\nb"} {
		if validateNotifyPC(NotifyPCConfig{Voice: v}) == "" {
			t.Errorf("voice %q accepted", v)
		}
	}
}

func TestNotifyPCDefaults(t *testing.T) {
	cfg := defaultConfig.withDefaults()
	if !cfg.NotifyPC.Enabled || cfg.NotifyPC.Speak || cfg.NotifyPC.Voice != "" {
		t.Errorf("defaults = %+v, want enabled, no speech, default voice", cfg.NotifyPC)
	}
}
