package service

// Tests for the volume/mute/media commands (#104, #105): argument building,
// the /st/v1 command paths and status block, the heartbeat's sampled_at
// and the audio.changed push.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func intp(n int) *int { return &n }

func TestMediaCommandArgs(t *testing.T) {
	ok := []struct {
		name  string
		value *int
		want  []string
	}{
		{"volume", intp(0), []string{"audio", "set", "0"}},
		{"volume", intp(30), []string{"audio", "set", "30"}},
		{"volume", intp(100), []string{"audio", "set", "100"}},
		{"volumeup", nil, []string{"audio", "step", "+5"}},
		{"volumeup", intp(1), []string{"audio", "step", "+1"}},
		{"volumedown", nil, []string{"audio", "step", "-5"}},
		{"volumedown", intp(100), []string{"audio", "step", "-100"}},
		{"mute", nil, []string{"audio", "mute", "on"}},
		// mute and unmute take no value; one sent anyway is ignored.
		{"unmute", intp(7), []string{"audio", "mute", "off"}},
		{"playpause", nil, []string{"media", "playpause"}},
		{"play", nil, []string{"media", "play"}},
		{"pause", nil, []string{"media", "pause"}},
		{"stop", nil, []string{"media", "stop"}},
		{"next", nil, []string{"media", "next"}},
		{"prev", nil, []string{"media", "prev"}},
	}
	for _, c := range ok {
		got, err := mediaCommandArgs(c.name, c.value)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v: %q, %v; want %q", c.name, c.value, got, err, c.want)
			continue
		}
		// Whatever is built must pass the subcommand's own parser, or
		// runUserAction would refuse it.
		if _, err := useraction.Parse(got); err != nil {
			t.Errorf("%s: %q does not parse: %v", c.name, got, err)
		}
	}
	bad := []struct {
		name  string
		value *int
	}{
		{"volume", nil}, {"volume", intp(-1)}, {"volume", intp(101)},
		{"volumeup", intp(0)}, {"volumeup", intp(101)}, {"volumedown", intp(-5)},
	}
	for _, c := range bad {
		var ve *errMediaValue
		if _, err := mediaCommandArgs(c.name, c.value); !errors.As(err, &ve) {
			t.Errorf("%s %v: err = %v, want a value error", c.name, *valueOr(c.value), err)
		}
	}
	// Every name the handler dispatches has arguments.
	for name := range mediaCommandKinds {
		if _, err := mediaCommandArgs(name, intp(5)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func valueOr(p *int) *int {
	if p == nil {
		return intp(-999)
	}
	return p
}

// fakeMediaRun replaces runUserActionFn and records the argument vectors.
type fakeMediaRun struct {
	mu    sync.Mutex
	calls [][]string
	res   UserActionResult
	err   error
}

func stubMediaRun(t *testing.T, res UserActionResult, err error) *fakeMediaRun {
	t.Helper()
	f := &fakeMediaRun{res: res, err: err}
	saved := runUserActionFn
	runUserActionFn = func(_ context.Context, args ...string) (UserActionResult, error) {
		f.mu.Lock()
		f.calls = append(f.calls, args)
		f.mu.Unlock()
		return f.res, f.err
	}
	t.Cleanup(func() { runUserActionFn = saved })
	return f
}

func (f *fakeMediaRun) Calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// mediaSetup is stSetup with media on (unless cfg says otherwise), an
// empty audio store and someone logged in.
func mediaSetup(t *testing.T, cfg Config) {
	t.Helper()
	stSetup(t, cfg)
	resetAudioSample()
	savedPresent := audioSessionPresent
	audioSessionPresent = func() bool { return true }
	t.Cleanup(func() {
		resetAudioSample()
		audioNow = time.Now
		audioSessionPresent = savedPresent
	})
}

func mediaOn() Config { return Config{Port: 5001, Media: MediaConfig{Enabled: true}} }

func TestSTVolumeCommand(t *testing.T) {
	mediaSetup(t, mediaOn())
	events := captureNotifications(t)
	run := stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 30, Device: "스피커"}}, nil)
	before := getLastRemote()

	w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"volume","value":30}`)
	if w.Code != http.StatusOK {
		t.Fatalf("volume: %d (%s)", w.Code, w.Body.String())
	}
	got := stJSON(t, w)
	if got["accepted"] != true || got["executed"] != true {
		t.Errorf("reply = %v", got)
	}
	a, _ := got["audio"].(map[string]any)
	if a["available"] != true || a["volume"] != float64(30) || a["muted"] != false || a["device"] != "스피커" || a["updated_at"] == "" {
		t.Errorf("audio = %v", got["audio"])
	}
	if calls := run.Calls(); len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"audio", "set", "30"}) {
		t.Errorf("user-action calls = %q", calls)
	}
	// Immediate and quiet: no schedule, no notification, no last_command.
	if sched := got["schedule"].(map[string]any); sched["active"] != false {
		t.Errorf("schedule = %v", sched)
	}
	expectNoNotification(t, events)
	if lr := getLastRemote(); lr != before {
		t.Errorf("last remote changed to %+v", lr)
	}

	// Up/down with and without a value; mute; media keys.
	for _, tc := range []struct {
		body string
		want []string
	}{
		{`{"command":"volumeup"}`, []string{"audio", "step", "+5"}},
		{`{"command":"volumedown","value":10}`, []string{"audio", "step", "-10"}},
		{`{"command":"MUTE"}`, []string{"audio", "mute", "on"}},
		{`{"command":"unmute","mode":"grace"}`, []string{"audio", "mute", "off"}},
		{`{"command":"next"}`, []string{"media", "next"}},
		{`{"command":"play"}`, []string{"media", "play"}},
	} {
		n := len(run.Calls())
		if w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", tc.body); w.Code != http.StatusOK {
			t.Errorf("%s: %d (%s)", tc.body, w.Code, w.Body.String())
			continue
		}
		if calls := run.Calls(); len(calls) != n+1 || !reflect.DeepEqual(calls[n], tc.want) {
			t.Errorf("%s: calls = %q, want %q last", tc.body, calls, tc.want)
		}
	}
	// A media key's reply has no audio block.
	run.res = UserActionResult{OK: true}
	if got := stJSON(t, stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", `{"command":"stop"}`)); got["audio"] != nil {
		t.Errorf("stop reply carries audio: %v", got)
	}
}

func TestSTMediaCommandErrors(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		runErr error
		body   string
		status int
		code   string
		ran    bool
	}{
		{"disabled", Config{Port: 5001}, nil, `{"command":"volume","value":30}`, http.StatusForbidden, "media_disabled", false},
		{"disabled media key", Config{Port: 5001}, nil, `{"command":"playpause"}`, http.StatusForbidden, "media_disabled", false},
		{"no user", mediaOn(), fmt.Errorf("get session: %w", errNoUserSession), `{"command":"mute"}`, http.StatusConflict, "no_user_session", true},
		{"no user media", mediaOn(), errNoUserSession, `{"command":"next"}`, http.StatusConflict, "no_user_session", true},
		{"volume without value", mediaOn(), nil, `{"command":"volume"}`, http.StatusBadRequest, "", false},
		{"volume too high", mediaOn(), nil, `{"command":"volume","value":101}`, http.StatusBadRequest, "", false},
		{"volume negative", mediaOn(), nil, `{"command":"volume","value":-1}`, http.StatusBadRequest, "", false},
		{"step zero", mediaOn(), nil, `{"command":"volumeup","value":0}`, http.StatusBadRequest, "", false},
		{"step too big", mediaOn(), nil, `{"command":"volumedown","value":101}`, http.StatusBadRequest, "", false},
		{"scheduled", mediaOn(), nil, `{"command":"mute","minutes":5}`, http.StatusBadRequest, "", false},
		{"no device", mediaOn(), &userActionError{Code: useraction.CodeUnsupported, Message: "no default playback device"},
			`{"command":"volume","value":1}`, http.StatusNotImplemented, "unsupported", true},
		{"failed", mediaOn(), &userActionError{Code: useraction.CodeFailed, Message: "SendInput inserted 0 of 2 events"},
			`{"command":"next"}`, http.StatusBadGateway, "failed", true},
		{"timeout", mediaOn(), fmt.Errorf("%w after 3s", errUserActionTimeout), `{"command":"mute"}`, http.StatusGatewayTimeout, "timeout", true},
		{"start error", mediaOn(), errors.New(`exec: C:\PC Control\x.exe: access denied`), `{"command":"mute"}`, http.StatusBadGateway, "failed", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mediaSetup(t, c.cfg)
			run := stubMediaRun(t, UserActionResult{}, c.runErr)
			w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "", c.body)
			if w.Code != c.status {
				t.Fatalf("status %d, want %d (%s)", w.Code, c.status, w.Body.String())
			}
			got := stJSON(t, w)
			if c.code != "" && got["error"] != c.code {
				t.Errorf("error = %v, want %s", got["error"], c.code)
			}
			if ran := len(run.Calls()) > 0; ran != c.ran {
				t.Errorf("user-action ran = %v, want %v", ran, c.ran)
			}
			// Paths and child output stay in the log.
			if strings.Contains(w.Body.String(), `C:\`) {
				t.Errorf("reply leaks a path: %s", w.Body.String())
			}
		})
	}
}

func TestSTStatusAudioBlock(t *testing.T) {
	mediaSetup(t, mediaOn())
	status := func() (map[string]any, []any) {
		t.Helper()
		got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
		a, ok := got["audio"].(map[string]any)
		if !ok {
			t.Fatalf("audio = %v, want an object", got["audio"])
		}
		f, _ := got["features"].([]any)
		return a, f
	}

	// No sample yet: available false, nothing else.
	a, features := status()
	if a["available"] != false || len(a) != 1 {
		t.Errorf("audio before any sample = %v, want only available:false", a)
	}
	if !containsAll(features, "awake", "audio", "media") {
		t.Errorf("features = %v, want awake, audio, media", features)
	}

	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	noteAudioSample(useraction.Audio{Volume: 42, Muted: true, Device: ""}, at)
	a, _ = status()
	want := map[string]any{"available": true, "volume": float64(42), "muted": true, "device": "", "updated_at": at.Format(time.RFC3339)}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("audio = %v, want %v", a, want)
	}

	// Nobody logged in: the stored sample is not reported.
	audioSessionPresent = func() bool { return false }
	if a, _ = status(); a["available"] != false || len(a) != 1 {
		t.Errorf("audio without a user = %v", a)
	}
	audioSessionPresent = func() bool { return true }

	// media.enabled off: neither the block nor the features.
	setConfig(Config{Port: 5001})
	a, features = status()
	if a["available"] != false || len(a) != 1 {
		t.Errorf("audio while disabled = %v", a)
	}
	for _, f := range features {
		if f == "audio" || f == "media" {
			t.Errorf("features while disabled = %v", features)
		}
	}
}

func containsAll(list []any, want ...string) bool {
	for _, w := range want {
		found := false
		for _, v := range list {
			if v == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestMediaConfigDefaultsOn(t *testing.T) {
	if !config.Default().Media.Enabled {
		t.Error("media.enabled defaults to off")
	}
	// An older config.json without the key keeps the default; an explicit
	// false is kept.
	for body, want := range map[string]bool{`{"port":5001}`: true, `{"media":{"enabled":false}}`: false, `{"media":{}}`: true} {
		cfg := config.Default()
		if err := json.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.WithDefaults().Media.Enabled != want {
			t.Errorf("%s: media.enabled = %v, want %v", body, cfg.Media.Enabled, want)
		}
	}
	old := config.Default().WithDefaults()
	changed := old
	changed.Media.Enabled = false
	if keys := config.ChangedKeys(old, changed); !reflect.DeepEqual(keys, []string{"media.enabled"}) {
		t.Errorf("changed keys = %v", keys)
	}
}

// ---- heartbeat sampled_at (#104) -------------------------------------------

func TestHeartbeatSampledAtOrdering(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	idleSetup(t)
	resetAudioSample()
	received := time.Date(2026, 9, 30, 12, 0, 10, 0, time.UTC)
	audioNow = func() time.Time { return received }
	t.Cleanup(func() { resetAudioSample(); audioNow = time.Now })

	post := func(body string) int {
		t.Helper()
		return heartbeatDo(t, body, true, true).Code
	}

	// A command finished at 12:00:05 and stored 40.
	noteAudioSample(useraction.Audio{Volume: 40}, received.Add(-5*time.Second))

	// A heartbeat sampled at 12:00:03 — before the command — but delivered
	// now must not undo it.
	if c := post(`{"audio":{"volume":30,"muted":false,"device":"","sampled_at":"2026-09-30T12:00:03Z"}}`); c != http.StatusOK {
		t.Fatalf("heartbeat: %d", c)
	}
	if s, _ := currentAudio(); s.Volume != 40 {
		t.Errorf("an older heartbeat replaced the command result: %+v", s)
	}

	// One sampled after the command wins, with its own time (fractional
	// seconds included).
	post(`{"audio":{"volume":35,"muted":true,"device":"","sampled_at":"2026-09-30T21:00:07.250+09:00"}}`)
	s, _ := currentAudio()
	if want := time.Date(2026, 9, 30, 12, 0, 7, 250e6, time.UTC); s.Volume != 35 || !s.UpdatedAt.Equal(want) {
		t.Errorf("newer heartbeat: %+v, want 35 at %v", s, want)
	}

	// Without sampled_at (an older tray app): the receive time.
	post(`{"audio":{"volume":36,"muted":false,"device":""}}`)
	if s, _ := currentAudio(); s.Volume != 36 || !s.UpdatedAt.Equal(received) {
		t.Errorf("no sampled_at: %+v, want 36 at the receive time", s)
	}

	// A time in the future is clamped to the receive time rather than
	// pinning the store.
	post(`{"audio":{"volume":37,"muted":false,"device":"","sampled_at":"2027-01-01T00:00:00Z"}}`)
	if s, _ := currentAudio(); s.Volume != 37 || !s.UpdatedAt.Equal(received) {
		t.Errorf("future sampled_at: %+v, want 37 at the receive time", s)
	}

	// Garbage is a 400 and stores nothing — idle included.
	if c := post(`{"idle_seconds":5,"audio":{"volume":50,"muted":false,"device":"","sampled_at":"yesterday"}}`); c != http.StatusBadRequest {
		t.Errorf("bad sampled_at: %d, want 400", c)
	}
	if s, _ := currentAudio(); s.Volume != 37 {
		t.Errorf("a rejected body stored audio: %+v", s)
	}
	if _, ok := lastIdleSeconds(); ok {
		t.Error("a rejected body stored the idle time")
	}
}

// ---- audio.changed push -----------------------------------------------------

func TestAudioChangedPush(t *testing.T) {
	stPushSetup(t, mediaOn())
	resetAudioSample()
	savedPresent := audioSessionPresent
	audioSessionPresent = func() bool { return true }
	t.Cleanup(func() { resetAudioSample(); audioSessionPresent = savedPresent })
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	t0 := time.Now()
	// The first reading is a baseline, not a change.
	recordAudioSample(useraction.Audio{Volume: 20, Device: "스피커"}, t0)
	time.Sleep(150 * time.Millisecond)
	if n := cb.hits.Load(); n != 0 {
		t.Fatalf("the baseline pushed (%d)", n)
	}

	// A command through the real runUserAction path.
	fakeUserAction(t, func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"ok":true,"audio":{"volume":55,"muted":false,"device":"스피커"}}`), nil
	})
	recordAudioSample(useraction.Audio{Volume: 20, Device: "스피커"}, t0) // fakeUserAction reset the store
	if w := stDo(t, "POST", "/st/v1/command", "127.0.0.1", "", `{"command":"volume","value":55}`); w.Code != http.StatusOK {
		t.Fatalf("volume: %d (%s)", w.Code, w.Body.String())
	}
	got := cb.wait(t)
	if got["type"] != "audio.changed" {
		t.Fatalf("type = %v", got["type"])
	}
	data, _ := got["data"].(map[string]any)
	if data["volume"] != "55" || data["muted"] != "false" || data["device"] != "스피커" {
		t.Errorf("data = %v", data)
	}
	status, _ := got["status"].(map[string]any)
	if a, _ := status["audio"].(map[string]any); a["volume"] != float64(55) {
		t.Errorf("status.audio = %v", status["audio"])
	}

	// The same state again is not a change.
	before := cb.hits.Load()
	recordAudioSample(useraction.Audio{Volume: 55, Device: "스피커"}, time.Now())
	time.Sleep(200 * time.Millisecond)
	if after := cb.hits.Load(); after != before {
		t.Errorf("an unchanged sample pushed again (%d → %d)", before, after)
	}
}

// ---- Telegram (#104, #105) --------------------------------------------------

func TestParseVolArg(t *testing.T) {
	ok := map[string]struct {
		name  string
		value int
	}{
		"30": {"volume", 30}, "0": {"volume", 0}, "100": {"volume", 100}, "30%": {"volume", 30},
		"+10": {"volumeup", 10}, "-10": {"volumedown", 10}, "+1": {"volumeup", 1}, "-100": {"volumedown", 100},
	}
	for arg, want := range ok {
		name, value, good := parseVolArg(arg)
		if !good || name != want.name || value != want.value {
			t.Errorf("parseVolArg(%q) = %s %d %v, want %s %d", arg, name, value, good, want.name, want.value)
		}
	}
	for _, arg := range []string{"", "101", "+0", "-0", "+101", "loud", "1e2", "+", "3 0", "0x10", "1000"} {
		if _, _, good := parseVolArg(arg); good {
			t.Errorf("parseVolArg(%q) accepted", arg)
		}
	}
}

func TestTelegramAudioState(t *testing.T) {
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	if got := tgAudioState(useraction.Audio{Volume: 30, Device: "스피커"}); got != "볼륨 30% · 음소거 꺼짐 · 스피커" {
		t.Errorf("ko = %q", got)
	}
	if got := tgAudioState(useraction.Audio{Volume: 0, Muted: true}); got != "볼륨 0% · 음소거 켜짐" {
		t.Errorf("ko without device = %q", got)
	}
	if got := tgAudioState(useraction.Audio{Volume: 5, Device: "<HDMI>"}); !strings.HasSuffix(got, "&lt;HDMI&gt;") {
		t.Errorf("device not escaped: %q", got)
	}
	setConfig(Config{Telegram: TelegramConfig{Lang: "en"}})
	if got := tgAudioState(useraction.Audio{Volume: 30, Device: "Speakers"}); got != "Volume 30% · mute off · Speakers" {
		t.Errorf("en = %q", got)
	}
}

func TestTelegramVolumeCommands(t *testing.T) {
	initLogger()
	setConfig(Config{Media: MediaConfig{Enabled: true}, Telegram: TelegramConfig{Lang: "ko"}})
	run := stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 30, Device: "스피커"}}, nil)
	var h telegramControl
	do := func(cmd string, args ...string) (string, error) {
		t.Helper()
		html, kb, err := h.HandleCommand(context.Background(), "42", cmd, args)
		if kb != nil {
			t.Errorf("/%s has a keyboard", cmd)
		}
		return tgBody(html), err
	}

	for _, tc := range []struct {
		cmd  string
		args []string
		want []string
	}{
		{"vol", nil, []string{"audio", "get"}},
		{"vol", []string{"30"}, []string{"audio", "set", "30"}},
		{"vol", []string{"+10"}, []string{"audio", "step", "+10"}},
		{"vol", []string{"-10"}, []string{"audio", "step", "-10"}},
		{"mute", nil, []string{"audio", "mute", "on"}},
		{"unmute", nil, []string{"audio", "mute", "off"}},
	} {
		n := len(run.Calls())
		body, err := do(tc.cmd, tc.args...)
		if err != nil || body != "볼륨 30% · 음소거 꺼짐 · 스피커" {
			t.Errorf("/%s %v = %q, %v", tc.cmd, tc.args, body, err)
		}
		if calls := run.Calls(); len(calls) != n+1 || !reflect.DeepEqual(calls[n], tc.want) {
			t.Errorf("/%s %v ran %q, want %q", tc.cmd, tc.args, calls[n:], tc.want)
		}
	}

	// Bad /vol values answer with the usage and run nothing.
	n := len(run.Calls())
	if body, err := do("vol", "loud"); err == nil || !strings.Contains(body, "/vol +10") {
		t.Errorf("/vol loud = %q, %v", body, err)
	}
	if len(run.Calls()) != n {
		t.Error("/vol loud ran user-action")
	}

	// Media keys.
	run.res = UserActionResult{OK: true}
	for cmd, want := range map[string]string{
		"play": "⏯ 재생/일시정지 키를 보냈습니다", "pause": "⏯ 재생/일시정지 키를 보냈습니다",
		"next": "⏭ 다음 곡 키를 보냈습니다", "prev": "⏮ 이전 곡 키를 보냈습니다", "stop": "⏹ 정지 키를 보냈습니다",
	} {
		if body, err := do(cmd); err != nil || body != want {
			t.Errorf("/%s = %q, %v; want %q", cmd, body, err, want)
		}
		if calls := run.Calls(); !reflect.DeepEqual(calls[len(calls)-1], []string{"media", cmd}) {
			t.Errorf("/%s ran %q", cmd, calls[len(calls)-1])
		}
	}

	// Nobody logged in; media off.
	run.err = fmt.Errorf("get session: %w", errNoUserSession)
	if body, err := do("vol"); err != nil || body != "로그인한 사용자가 없어 실행할 수 없습니다" {
		t.Errorf("no user = %q, %v", body, err)
	}
	run.err = &userActionError{Code: useraction.CodeUnsupported, Message: "no default playback device"}
	if body, err := do("mute"); err == nil || !strings.Contains(body, "no default playback device") {
		t.Errorf("unsupported = %q, %v", body, err)
	}
	run.err = nil
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	n = len(run.Calls())
	for _, cmd := range []string{"vol", "mute", "unmute", "play", "next"} {
		if body, _ := do(cmd); !strings.Contains(body, "media.enabled") {
			t.Errorf("/%s while disabled = %q", cmd, body)
		}
	}
	if len(run.Calls()) != n {
		t.Error("a disabled command ran user-action")
	}
}

// /mute with a duration still pauses notifications (the pre-#104 meaning);
// /quiet is the new name, and /unmute points at /quiet off while a pause is
// on.
func TestTelegramQuietAndLegacyMute(t *testing.T) {
	initLogger()
	setConfig(Config{Media: MediaConfig{Enabled: true}, Telegram: TelegramConfig{Lang: "ko"}})
	run := stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 30}}, nil)
	captureNotifications(t)
	var h telegramControl

	if html, _, err := h.HandleCommand(context.Background(), "42", "mute", []string{"2h"}); err != nil || !strings.HasPrefix(tgBody(html), "🔕") {
		t.Errorf("/mute 2h = %q, %v", html, err)
	}
	if currentBus().MutedUntil().IsZero() {
		t.Fatal("/mute 2h did not pause notifications")
	}
	if len(run.Calls()) != 0 {
		t.Error("/mute 2h touched the PC's audio")
	}

	html, _, _ := h.HandleCommand(context.Background(), "42", "unmute", nil)
	if !strings.HasPrefix(tgBody(html), "볼륨 30%") || !strings.Contains(html, "/quiet off") {
		t.Errorf("/unmute during a pause = %q", html)
	}
	if currentBus().MutedUntil().IsZero() {
		t.Error("/unmute ended the notification pause")
	}

	if html, _, _ := h.HandleCommand(context.Background(), "42", "quiet", []string{"off"}); !strings.HasPrefix(tgBody(html), "🔔") {
		t.Errorf("/quiet off = %q", html)
	}
	if !currentBus().MutedUntil().IsZero() {
		t.Error("still paused after /quiet off")
	}
	if html, _, err := h.HandleCommand(context.Background(), "42", "quiet", []string{"30m"}); err != nil || !strings.HasPrefix(tgBody(html), "🔕") {
		t.Errorf("/quiet 30m = %q, %v", html, err)
	}
	if _, _, err := h.HandleCommand(context.Background(), "42", "quiet", []string{"soon"}); err == nil {
		t.Error("/quiet soon accepted")
	}
	if _, _, err := h.HandleCommand(context.Background(), "42", "quiet", nil); err != nil {
		t.Errorf("/quiet usage should not be an error: %v", err)
	}
}
