package service

// Tests for now playing (#117): the store, the heartbeat media block, the
// status block and features, media.changed, the /api/media endpoint and
// the Telegram wording.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/session"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// No test may start the delayed `media info` refresh: it would outlive the
// test and call whatever runUserActionFn is by then.
func init() {
	scheduleMediaRefresh = func() {}
}

var spotifyTrack = useraction.NowPlaying{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", Album: "New Jeans", App: "Spotify"}

// nowPlayingSetup is mediaSetup with an empty media store and a fixed
// clock.
func nowPlayingSetup(t *testing.T, cfg Config) time.Time {
	t.Helper()
	mediaSetup(t, cfg)
	resetMediaSample()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	audioNow = func() time.Time { return now }
	t.Cleanup(resetMediaSample)
	return now
}

func optIn() Config {
	return Config{Port: 5001, Media: MediaConfig{Enabled: true, NowPlaying: true}}
}

func TestMediaStoreNewerWins(t *testing.T) {
	now := nowPlayingSetup(t, optIn())
	if stored, changed := noteMediaSampleChange(spotifyTrack, now); !stored || changed {
		t.Errorf("first sample: stored %v, changed %v (a baseline is no change)", stored, changed)
	}
	paused := spotifyTrack
	paused.Status = "paused"
	if stored, _ := noteMediaSampleChange(paused, now.Add(-time.Second)); stored {
		t.Error("an older sample replaced a newer one")
	}
	if stored, changed := noteMediaSampleChange(paused, now.Add(time.Second)); !stored || !changed {
		t.Errorf("newer sample: stored %v, changed %v", stored, changed)
	}
	if stored, changed := noteMediaSampleChange(paused, now.Add(2*time.Second)); !stored || changed {
		t.Errorf("same state again: stored %v, changed %v", stored, changed)
	}

	// Stale after the TTL.
	if _, ok := currentMedia(); !ok {
		t.Fatal("fresh sample not reported")
	}
	audioNow = func() time.Time { return now.Add(mediaSampleTTL + 3*time.Second) }
	if _, ok := currentMedia(); ok {
		t.Error("a sample older than the TTL is still reported")
	}
}

// Without the opt-in only the status is stored, whatever the sender sent.
func TestMediaStoreAppliesOptIn(t *testing.T) {
	now := nowPlayingSetup(t, mediaOn())
	recordMediaSample(spotifyTrack, now)
	if s, _ := currentMedia(); s.NowPlaying != (useraction.NowPlaying{Status: "playing"}) {
		t.Errorf("stored without opt-in: %+v", s.NowPlaying)
	}
}

func TestSTStatusMediaBlock(t *testing.T) {
	now := nowPlayingSetup(t, optIn())
	status := func() (map[string]any, []any) {
		t.Helper()
		got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
		m, ok := got["media"].(map[string]any)
		if !ok {
			t.Fatalf("media = %v, want an object", got["media"])
		}
		f, _ := got["features"].([]any)
		return m, f
	}

	// Nothing known yet.
	m, features := status()
	if !reflect.DeepEqual(m, map[string]any{"status": "none"}) {
		t.Errorf("media before any sample = %v", m)
	}
	if !containsAll(features, "audio", "media", "nowplaying") {
		t.Errorf("features = %v, want nowplaying too", features)
	}

	recordMediaSample(spotifyTrack, now)
	m, _ = status()
	want := map[string]any{"status": "playing", "title": "Hype Boy", "artist": "NewJeans", "album": "New Jeans", "app": "Spotify", "updated_at": now.Format(time.RFC3339)}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("media = %v, want %v", m, want)
	}

	// Opt-in off: a title stored a moment ago is not shown, and the
	// feature goes.
	setConfig(mediaOn())
	m, features = status()
	if !reflect.DeepEqual(m, map[string]any{"status": "playing", "updated_at": now.Format(time.RFC3339)}) {
		t.Errorf("media without opt-in = %v", m)
	}
	for _, f := range features {
		if f == "nowplaying" {
			t.Errorf("features without opt-in = %v", features)
		}
	}

	// Nobody logged in, media off: none.
	audioSessionPresent = func() bool { return false }
	if m, _ = status(); !reflect.DeepEqual(m, map[string]any{"status": "none"}) {
		t.Errorf("media without a user = %v", m)
	}
	audioSessionPresent = func() bool { return true }
	setConfig(Config{Port: 5001, Media: MediaConfig{NowPlaying: true}})
	if m, features = status(); !reflect.DeepEqual(m, map[string]any{"status": "none"}) || containsAll(features, "nowplaying") {
		t.Errorf("media while disabled = %v, features %v", m, features)
	}
}

func TestHeartbeatMediaBlock(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t", Media: MediaConfig{Enabled: true, NowPlaying: true}})
	idleSetup(t)
	resetMediaSample()
	received := time.Date(2026, 9, 30, 12, 0, 10, 0, time.UTC)
	audioNow = func() time.Time { return received }
	t.Cleanup(func() { resetMediaSample(); audioNow = time.Now })
	post := func(body string) int {
		t.Helper()
		return heartbeatDo(t, body, true, true).Code
	}

	// A body with only the media block (the 3s change post).
	if c := post(`{"media":{"status":"paused","title":"Ditto","app":"Spotify","sampled_at":"2026-09-30T12:00:08Z"}}`); c != http.StatusOK {
		t.Fatalf("heartbeat: %d", c)
	}
	s, _ := currentMedia()
	if s.NowPlaying != (useraction.NowPlaying{Status: "paused", Title: "Ditto", App: "Spotify"}) || !s.UpdatedAt.Equal(received.Add(-2*time.Second)) {
		t.Errorf("stored %+v", s)
	}
	// Older than the stored one: ignored.
	post(`{"media":{"status":"playing","sampled_at":"2026-09-30T12:00:01Z"}}`)
	if s, _ := currentMedia(); s.Status != "paused" {
		t.Errorf("an older heartbeat won: %+v", s)
	}
	// Invalid blocks are a 400 and store nothing.
	for _, body := range []string{
		`{"media":{"status":"loud"}}`,
		`{"media":{"status":"playing","title":"a\nb"}}`,
		`{"media":{"status":"playing","sampled_at":"soon"}}`,
		`{"idle_seconds":3,"media":{"status":"PLAYING"}}`,
	} {
		if c := post(body); c != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, c)
		}
	}
	if s, _ := currentMedia(); s.Status != "paused" {
		t.Errorf("a rejected body stored media: %+v", s)
	}
	if _, ok := lastIdleSeconds(); ok {
		t.Error("a rejected body stored the idle time")
	}
}

func TestMediaChangedPush(t *testing.T) {
	stPushSetup(t, optIn())
	resetMediaSample()
	savedPresent := audioSessionPresent
	audioSessionPresent = func() bool { return true }
	t.Cleanup(func() { resetMediaSample(); audioSessionPresent = savedPresent })
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	t0 := time.Now()
	recordMediaSample(useraction.NowPlaying{Status: "none"}, t0)
	time.Sleep(150 * time.Millisecond)
	if n := cb.hits.Load(); n != 0 {
		t.Fatalf("the baseline pushed (%d)", n)
	}
	recordMediaSample(spotifyTrack, t0.Add(time.Second))
	got := cb.wait(t)
	if got["type"] != "media.changed" {
		t.Fatalf("type = %v", got["type"])
	}
	data, _ := got["data"].(map[string]any)
	if data["status"] != "playing" || data["title"] != "Hype Boy" || data["app"] != "Spotify" {
		t.Errorf("data = %v", data)
	}
	status, _ := got["status"].(map[string]any)
	if m, _ := status["media"].(map[string]any); m["title"] != "Hype Boy" {
		t.Errorf("status.media = %v", status["media"])
	}
}

func TestMediaEventFieldsOmitUnknown(t *testing.T) {
	got := mediaEventFields(useraction.NowPlaying{Status: "paused", App: "VLC"})
	if !reflect.DeepEqual(got, map[string]string{"status": "paused", "app": "VLC"}) {
		t.Errorf("fields = %v", got)
	}
}

// A media key reply folds into the store at once, and the refresh is
// scheduled; the audio commands do neither.
func TestMediaCommandUpdatesStore(t *testing.T) {
	now := nowPlayingSetup(t, optIn())
	var refreshes atomic.Int32
	saved := scheduleMediaRefresh
	scheduleMediaRefresh = func() { refreshes.Add(1) }
	t.Cleanup(func() { scheduleMediaRefresh = saved })

	recordMediaSample(spotifyTrack, now.Add(-time.Second))
	reply := func(line string) UserActionResult {
		res, err := session.ParseOutput([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	stubMediaRun(t, reply(`{"ok":true,"app":"Spotify","media":"pause","status":"paused","via":"session"}`), nil)
	if _, err := runMediaCommand(context.Background(), "pause", nil); err != nil {
		t.Fatal(err)
	}
	want := spotifyTrack
	want.Status = "paused"
	if s, _ := currentMedia(); s.NowPlaying != want {
		t.Errorf("after pause: %+v, want the same track paused", s.NowPlaying)
	}
	if refreshes.Load() != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes.Load())
	}

	// Another app took over: its status, not the old track.
	stubMediaRun(t, reply(`{"ok":true,"app":"Chrome","media":"play","status":"playing","via":"session"}`), nil)
	runMediaCommand(context.Background(), "play", nil)
	if s, _ := currentMedia(); s.NowPlaying != (useraction.NowPlaying{Status: "playing", App: "Chrome"}) {
		t.Errorf("after play in Chrome: %+v", s.NowPlaying)
	}

	// The key path knows nothing about the result: the store stays.
	stubMediaRun(t, reply(`{"ok":true,"media":"next","via":"keys"}`), nil)
	runMediaCommand(context.Background(), "next", nil)
	if s, _ := currentMedia(); s.App != "Chrome" || refreshes.Load() != 3 {
		t.Errorf("after a key: %+v, refreshes %d", s.NowPlaying, refreshes.Load())
	}

	stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 3}}, nil)
	runMediaCommand(context.Background(), "volume", intp(3))
	if refreshes.Load() != 3 {
		t.Error("a volume command scheduled a media refresh")
	}
}

// A `media info` reply through the real runUserAction path is stored.
func TestRunUserActionStoresNowPlaying(t *testing.T) {
	fakeUserAction(t, func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"ok":true,"media":{"status":"playing","title":"Hype Boy","artist":"NewJeans","app":"Spotify"}}` + "\n"), nil
	})
	nowPlayingSetup(t, optIn())
	np, err := readNowPlayingNow(context.Background())
	if err != nil || np.Title != "Hype Boy" {
		t.Fatalf("readNowPlayingNow = %+v, %v", np, err)
	}
	if s, _ := currentMedia(); s.Title != "Hype Boy" {
		t.Errorf("not stored: %+v", s)
	}
	// A key reply's "media" is a string, not a session.
	res, _ := session.ParseOutput([]byte(`{"ok":true,"media":"next","via":"keys"}`))
	if _, ok := res.NowPlaying(); ok {
		t.Error("a key reply read as now playing")
	}
}

// ---- /api/media ------------------------------------------------------------

func mediaAPI(t *testing.T, method, body string) (int, mediaAPIBody, map[string]string) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/media", nil)
	} else {
		r = httptest.NewRequest(method, "/api/media", strings.NewReader(body))
	}
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	handleMediaAPI(w, r)
	var out mediaAPIBody
	json.Unmarshal(w.Body.Bytes(), &out)
	var errBody map[string]string
	json.Unmarshal(w.Body.Bytes(), &errBody)
	return w.Code, out, errBody
}

func TestMediaAPI(t *testing.T) {
	now := nowPlayingSetup(t, optIn())
	recordMediaSample(spotifyTrack, now)
	noteAudioSample(useraction.Audio{Volume: 40, Device: "스피커"}, now)

	code, v, _ := mediaAPI(t, "GET", "")
	if code != http.StatusOK || !v.Enabled || !v.NowPlaying || !v.Session || v.Media.Title != "Hype Boy" || !v.Audio.Available || *v.Audio.Volume != 40 {
		t.Fatalf("GET = %d %+v", code, v)
	}

	run := stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 55, Device: "스피커"}}, nil)
	code, v, _ = mediaAPI(t, "POST", `{"command":"volume","value":55}`)
	if code != http.StatusOK || *v.Audio.Volume != 55 {
		t.Errorf("POST volume = %d %+v", code, v.Audio)
	}
	if calls := run.Calls(); !reflect.DeepEqual(calls[len(calls)-1], []string{"audio", "set", "55"}) {
		t.Errorf("ran %q", calls)
	}
	if code, _, _ := mediaAPI(t, "POST", `{"command":"shutdown"}`); code != http.StatusBadRequest {
		t.Errorf("POST shutdown = %d, want 400", code)
	}
	if code, _, e := mediaAPI(t, "POST", `{"command":"volume","value":500}`); code != http.StatusBadRequest || e["error"] == "" {
		t.Errorf("POST volume 500 = %d %v", code, e)
	}
	run.err = errNoUserSession
	if code, _, e := mediaAPI(t, "POST", `{"command":"next"}`); code != http.StatusConflict || e["error"] != "no_user_session" {
		t.Errorf("no user = %d %v", code, e)
	}
	setConfig(Config{Port: 5001})
	if code, _, e := mediaAPI(t, "POST", `{"command":"next"}`); code != http.StatusForbidden || e["error"] != "media_disabled" {
		t.Errorf("disabled = %d %v", code, e)
	}
	if _, v, _ = mediaAPI(t, "GET", ""); v.Enabled || v.Media.Status != "none" {
		t.Errorf("GET while disabled = %+v", v)
	}

	// Changes need the CSRF header.
	r := httptest.NewRequest("POST", "/api/media", strings.NewReader(`{"command":"next"}`))
	w := httptest.NewRecorder()
	handleMediaAPI(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF = %d", w.Code)
	}
}

// ---- Telegram --------------------------------------------------------------

func TestTelegramNowPlayingText(t *testing.T) {
	initLogger()
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	cases := []struct {
		np    useraction.NowPlaying
		share bool
		want  string
	}{
		{spotifyTrack, true, "▶ Hype Boy — NewJeans · Spotify"},
		{useraction.NowPlaying{Status: "paused", Title: "<b>Ditto</b>", App: "Chrome"}, true, "⏸ &lt;b&gt;Ditto&lt;/b&gt; · Chrome"},
		{useraction.NowPlaying{Status: "playing", App: "VLC"}, true, "▶ 재생 중 · VLC"},
		{useraction.NowPlaying{Status: "stopped"}, true, "⏹ 정지"},
		{spotifyTrack, false, "▶ 재생 중 (재생 정보 공유가 꺼져 있습니다)"},
		{useraction.NowPlaying{Status: "paused"}, false, "⏸ 일시정지 (재생 정보 공유가 꺼져 있습니다)"},
		{useraction.NowPlaying{Status: "none"}, true, "재생 중인 미디어 없음"},
		{useraction.NowPlaying{Status: "none"}, false, "재생 중인 미디어 없음"},
	}
	for _, c := range cases {
		if got := tgNowPlayingText(c.np, c.share); got != c.want {
			t.Errorf("tgNowPlayingText(%+v, %v) = %q, want %q", c.np, c.share, got, c.want)
		}
	}
	setConfig(Config{Telegram: TelegramConfig{Lang: "en"}})
	if got := tgNowPlayingText(useraction.NowPlaying{Status: "none"}, true); got != "Nothing is playing" {
		t.Errorf("en none = %q", got)
	}
}

func TestTelegramMediaLine(t *testing.T) {
	initLogger()
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	m := stMedia{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", App: "Spotify"}
	if got := tgMediaLine(m, true); got != "미디어: ▶ Hype Boy — NewJeans · Spotify" {
		t.Errorf("line = %q", got)
	}
	if got := tgMediaLine(stMedia{Status: "playing"}, false); got != "미디어: ▶ 재생 중" {
		t.Errorf("opt-out line = %q", got)
	}
	for _, s := range []string{"paused", "stopped", "none"} {
		if got := tgMediaLine(stMedia{Status: s, Title: "x"}, true); got != "" {
			t.Errorf("%s line = %q, want none", s, got)
		}
	}
}

func TestTelegramMediaCommandResults(t *testing.T) {
	initLogger()
	nowPlayingSetup(t, Config{Media: MediaConfig{Enabled: true, NowPlaying: true}, Telegram: TelegramConfig{Lang: "ko"}})
	var h telegramControl
	do := func(cmd string) string {
		t.Helper()
		html, _, err := h.HandleCommand(context.Background(), "42", cmd, nil)
		if err != nil {
			t.Errorf("/%s: %v", cmd, err)
		}
		return tgBody(html)
	}
	reply := func(line string) UserActionResult {
		res, err := session.ParseOutput([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for cmd, tc := range map[string]struct{ line, want string }{
		"pause": {`{"ok":true,"app":"Spotify","media":"pause","status":"paused","via":"session"}`, "⏸ 일시정지했습니다 · Spotify"},
		"play":  {`{"ok":true,"app":"Spotify","media":"play","status":"playing","via":"session"}`, "▶ 재생했습니다 · Spotify"},
		"stop":  {`{"ok":true,"media":"stop","status":"stopped","via":"session"}`, "⏹ 정지했습니다"},
		"next":  {`{"ok":true,"app":"Chrome","media":"next","status":"playing","via":"session"}`, "⏭ 다음 곡으로 넘겼습니다 · Chrome"},
		"prev":  {`{"ok":true,"app":"VLC","media":"prev","status":"paused","via":"session"}`, "⏮ 이전 곡으로 돌아갔습니다 · VLC"},
	} {
		stubMediaRun(t, reply(tc.line), nil)
		if got := do(cmd); got != tc.want {
			t.Errorf("/%s = %q, want %q", cmd, got, tc.want)
		}
	}
	// The key path is worded as before.
	stubMediaRun(t, reply(`{"ok":true,"media":"pause","via":"keys"}`), nil)
	if got := do("pause"); got != "⏯ 재생/일시정지 키를 보냈습니다" {
		t.Errorf("/pause via keys = %q", got)
	}
	// Without the opt-in the app name stays home.
	setConfig(Config{Media: MediaConfig{Enabled: true}, Telegram: TelegramConfig{Lang: "ko"}})
	stubMediaRun(t, reply(`{"ok":true,"app":"Spotify","media":"pause","status":"paused","via":"session"}`), nil)
	if got := do("pause"); got != "⏸ 일시정지했습니다" {
		t.Errorf("/pause without opt-in = %q", got)
	}

	// /np runs media info.
	run := stubMediaRun(t, reply(`{"ok":true,"media":{"status":"paused","title":"Ditto","app":"Spotify"}}`), nil)
	if got := do("np"); got != "⏸ 일시정지 (재생 정보 공유가 꺼져 있습니다)" {
		t.Errorf("/np without opt-in = %q", got)
	}
	setConfig(Config{Media: MediaConfig{Enabled: true, NowPlaying: true}, Telegram: TelegramConfig{Lang: "ko"}})
	if got := do("np"); got != "⏸ Ditto · Spotify" {
		t.Errorf("/np = %q", got)
	}
	if calls := run.Calls(); !reflect.DeepEqual(calls[len(calls)-1], []string{"media", "info"}) {
		t.Errorf("/np ran %q", calls)
	}
	setConfig(Config{Telegram: TelegramConfig{Lang: "ko"}})
	if got := do("np"); !strings.Contains(got, "media.enabled") {
		t.Errorf("/np while disabled = %q", got)
	}
}

func TestTelegramStatusMediaLine(t *testing.T) {
	initLogger()
	now := nowPlayingSetup(t, Config{Media: MediaConfig{Enabled: true, NowPlaying: true}, Telegram: TelegramConfig{Lang: "ko"}})
	if strings.Contains(tgStatusText(), "미디어:") {
		t.Error("/status has a media line with nothing playing")
	}
	recordMediaSample(spotifyTrack, now)
	if got := tgStatusText(); !strings.Contains(got, "\n미디어: ▶ Hype Boy — NewJeans · Spotify") {
		t.Errorf("/status = %q", got)
	}
}
