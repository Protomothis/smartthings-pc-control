package service

// Tests for PC notification (#106): text rules, the rate limit, the
// /st/v1/notify and /api/notify/test endpoints over an injected runner.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/session"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func TestNotifyLimiter(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	l := ratelimit.New(pcNotifyPerMinute, pcNotifyWindow, func() time.Time { return now })
	for i := 0; i < pcNotifyPerMinute; i++ {
		if ok, _ := l.Allow("ip 1"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
		now = now.Add(time.Second)
	}
	ok, wait := l.Allow("ip 1")
	if ok {
		t.Fatal("11th request in a minute allowed")
	}
	// The first hit was at 12:00:00; the window frees at 12:01:00, and it
	// is 12:00:10 now.
	if wait != 50*time.Second {
		t.Errorf("retry after %v, want 50s", wait)
	}
	if ok, _ := l.Allow("ip 2"); !ok {
		t.Error("another source shares the limit")
	}
	now = now.Add(50 * time.Second)
	if ok, _ := l.Allow("ip 1"); !ok {
		t.Error("still refused once the oldest hit left the window")
	}
	if ok, _ := l.Allow("ip 1"); ok {
		t.Error("the window should be full again")
	}
}

// fakeNotifyRun replaces the user-action runner and the rate limiter.
func fakeNotifyRun(t *testing.T, reply string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	saved, savedLimits := userRun.notify, pcNotifyLimits
	userRun.notify = func(ctx context.Context, args ...string) (UserActionResult, error) {
		calls = append(calls, args)
		if err != nil {
			return UserActionResult{}, err
		}
		return session.ParseOutput([]byte(reply))
	}
	pcNotifyLimits = ratelimit.New(pcNotifyPerMinute, pcNotifyWindow, time.Now)
	t.Cleanup(func() { userRun.notify, pcNotifyLimits = saved, savedLimits })
	return &calls
}

func notifyCfg(enabled bool) Config {
	return Config{Port: 5001, NotifyPC: NotifyPCConfig{Enabled: enabled}}
}

func TestSTNotifyOK(t *testing.T) {
	stSetup(t, notifyCfg(true))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)

	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"빨래가 끝났습니다\n"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := stJSON(t, w)
	if !reflect.DeepEqual(body, map[string]any{"ok": true, "toast": "shown"}) {
		t.Errorf("body = %v", body)
	}
	want := []string{"notify", "--title", "SmartThings", "--text", "빨래가 끝났습니다"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("args = %q, want %q", *calls, want)
	}
}

// An older driver still sends "speak" (read-aloud was dropped on
// 2026-10-01): the field is ignored and the toast is shown as usual.
func TestSTNotifyIgnoresSpeak(t *testing.T) {
	stSetup(t, notifyCfg(true))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)

	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"title":"세탁기","text":"끝","speak":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if body := stJSON(t, w); !reflect.DeepEqual(body, map[string]any{"ok": true, "toast": "shown"}) {
		t.Errorf("body = %v", body)
	}
	want := []string{"notify", "--title", "세탁기", "--text", "끝"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("args = %q, want %q", *calls, want)
	}
}

func TestSTNotifyRefusals(t *testing.T) {
	stSetup(t, notifyCfg(false))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)

	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`)
	if w.Code != http.StatusForbidden || stJSON(t, w)["error"] != "notify_disabled" {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}

	setConfig(notifyCfg(true))
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
	stSetup(t, notifyCfg(true))
	fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)
	for i := 0; i < pcNotifyPerMinute; i++ {
		stSrv.ResetRateLimit() // the per-second /st/v1 bucket is not what is tested
		if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`); w.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, w.Code)
		}
	}
	stSrv.ResetRateLimit()
	w := stDo(t, "POST", "/st/v1/notify", "192.168.1.20", "", `{"text":"hi"}`)
	if w.Code != http.StatusTooManyRequests || stJSON(t, w)["error"] != "rate_limited" {
		t.Fatalf("11th: %d %s", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q", ra)
	}
	stSrv.ResetRateLimit()
	if w := stDo(t, "POST", "/st/v1/notify", "192.168.1.21", "", `{"text":"hi"}`); w.Code != http.StatusOK {
		t.Errorf("another hub: status %d", w.Code)
	}
}

func TestSTNotifyUserSessionErrors(t *testing.T) {
	stSetup(t, notifyCfg(true))
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
		stSrv.ResetRateLimit()
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

func TestNotifyTestAPI(t *testing.T) {
	withLiveConfig(t, notifyCfg(false)) // off: the test button still works
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)

	w := httptest.NewRecorder()
	// An older app still sends speak/voice: ignored.
	webAPI(w, postJSON("/api/notify/test", `{"speak":true,"voice":"Zira"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	if body["status"] != "ok" || body["toast"] != "shown" {
		t.Errorf("body = %v", body)
	}
	if args := (*calls)[0]; len(args) != 5 || args[0] != "notify" {
		t.Errorf("args = %q", args)
	}

	// No CSRF header: refused before anything runs.
	w = httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("POST", "/api/notify/test", strings.NewReader(`{}`)))
	if w.Code != http.StatusForbidden || len(*calls) != 1 {
		t.Errorf("without CSRF header: %d, calls %d", w.Code, len(*calls))
	}

	fakeNotifyRun(t, "", errNoUserSession)
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/notify/test", `{}`))
	if w.Code != http.StatusConflict || decodeBody(t, w)["error"] != "no_user_session" {
		t.Errorf("no session: %d %s", w.Code, w.Body.String())
	}
}

func TestNotifyArgsParse(t *testing.T) {
	args := notifyArgs("SmartThings", "--text is text")
	req, err := useraction.Parse(args)
	if err != nil {
		t.Fatalf("%q: %v", args, err)
	}
	if req.Title != "SmartThings" || req.Text != "--text is text" {
		t.Errorf("%q parsed as %+v", args, req)
	}
}

// TestNotifyPCLegacySpeechKeysDropped: a config.json written while
// read-aloud existed (notify_pc.speak/voice, dropped 2026-10-01) loads
// without an error, keeps enabled, and the next save writes notify_pc
// without the old keys.
func TestNotifyPCLegacySpeechKeysDropped(t *testing.T) {
	configPath := withConfigFile(t, `{"port": 5001, "notify_pc": {"enabled": false, "speak": true, "voice": "Heami"}}`)

	cfg := loadConfig()
	if cfg.NotifyPC.Enabled {
		t.Error("notify_pc.enabled was lost alongside the retired keys")
	}

	prev := getConfig()
	t.Cleanup(func() { setConfig(prev) })
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		NotifyPC map[string]any `json:"notify_pc"`
	}
	if err := json.Unmarshal(saved, &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.NotifyPC, map[string]any{"enabled": false}) {
		t.Errorf("saved notify_pc = %v, want only enabled", doc.NotifyPC)
	}
}
