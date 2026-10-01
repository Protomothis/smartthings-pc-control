package stapi

// What /st/v1 decides on its own, before any store is read: authentication,
// the hub allow-list, the per-source rate limit, the public description and
// the push subscriptions with their delivery (edge-driver doc §3.1, §3.5,
// §3.6). Each test gets a fresh Server over the fakes in fake_test.go, so
// none of them needs to reset state another test left behind. The status
// and push bodies themselves are pinned by the contract fixtures
// (testdata/st-v1, service/contract_golden_test.go).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// liveServer is a Server whose config the test can change between
// requests, and which records every event it emits.
type liveServer struct {
	*Server
	mu     sync.Mutex
	cfg    config.Config
	events []string // "category.kind from path"
}

func newLiveServer(t *testing.T, cfg config.Config) *liveServer {
	t.Helper()
	l := &liveServer{cfg: cfg}
	l.Server = New(Deps{
		Config:  func() config.Config { l.mu.Lock(); defer l.mu.Unlock(); return l.cfg },
		Version: func() string { return testVersion },
		Emit: func(category, kind string, fields map[string]string) {
			l.mu.Lock()
			l.events = append(l.events, category+"."+kind+" "+fields["from"]+" "+fields["path"])
			l.mu.Unlock()
		},
		Status:   fakeSource{},
		Commands: fakeCommands{},
		Awake:    fakeAwake{},
		Media:    fakeMedia{},
		Presets:  fakePresets{},
		Notify:   fakeNotifier{},
	})
	return l
}

func (l *liveServer) setConfig(cfg config.Config) {
	l.mu.Lock()
	l.cfg = cfg
	l.mu.Unlock()
}

func (l *liveServer) emitted() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// do sends one request through the handler tree from the source IP from;
// secret goes into X-PC-Secret when non-empty.
func (l *liveServer) do(t *testing.T, method, path, from, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.RemoteAddr = from + ":51234"
	if secret != "" {
		r.Header.Set("X-PC-Secret", secret)
	}
	r.Header.Set("User-Agent", DriverAgent+"/1.0.0")
	w := httptest.NewRecorder()
	l.Handler().ServeHTTP(w, r)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

// ---- auth (§3.1) -----------------------------------------------------------

// TestAuth: the secret header and the hub allow-list guard every control
// route, a rejection is reported as security.unauthorized without the
// attempted value, and only an accepted request counts as a hub contact.
func TestAuth(t *testing.T) {
	const secret = "s3cr3t"
	withSecret := config.Config{Port: 5001, Secret: secret}
	allowList := config.Config{Port: 5001, Secret: secret, SmartThings: config.SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}}}
	sub := `{"callback":"http://192.168.1.20:41234/pc/evt","ttl_seconds":600}`

	for _, tc := range []struct {
		name               string
		cfg                config.Config
		method, path, body string
		from, secret       string
		want               int
	}{
		{"right header", withSecret, "GET", "/st/v1/status", "", "192.168.1.20", secret, http.StatusOK},
		{"no secret configured, no header", config.Config{Port: 5001}, "GET", "/st/v1/status", "", "192.168.1.20", "", http.StatusOK},
		{"missing header", withSecret, "GET", "/st/v1/status", "", "192.168.1.20", "", http.StatusUnauthorized},
		{"wrong header", withSecret, "GET", "/st/v1/status", "", "192.168.1.21", "guessed", http.StatusUnauthorized},
		{"subscribe without the secret", withSecret, "POST", "/st/v1/subscribe", sub, "192.168.1.20", "", http.StatusUnauthorized},
		{"unsubscribe without the secret", withSecret, "DELETE", "/st/v1/subscribe/sub-1", "", "192.168.1.20", "", http.StatusUnauthorized},
		{"command without the secret", withSecret, "POST", "/st/v1/command", `{"command":"lock"}`, "192.168.1.20", "", http.StatusUnauthorized},
		{"allowed hub", allowList, "GET", "/st/v1/status", "", "192.168.1.20", secret, http.StatusOK},
		{"hub not on the allow-list", allowList, "GET", "/st/v1/status", "", "10.0.0.9", secret, http.StatusForbidden},
		{"an empty allow-list allows everyone", withSecret, "GET", "/st/v1/status", "", "10.0.0.9", secret, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newLiveServer(t, tc.cfg)
			w := s.do(t, tc.method, tc.path, tc.from, tc.secret, tc.body)
			if w.Code != tc.want {
				t.Fatalf("%d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			events := s.emitted()
			seen, hubSeen := s.HubLastSeen()
			if tc.want == http.StatusOK {
				if len(events) != 0 {
					t.Errorf("an accepted request emitted %v", events)
				}
				if !hubSeen || seen.IP != tc.from || seen.DriverVersion != "1.0.0" {
					t.Errorf("hub last seen = %+v, %v; want %s with driver 1.0.0", seen, hubSeen, tc.from)
				}
				return
			}
			if want := "security.unauthorized " + tc.from + " " + tc.path; len(events) != 1 || events[0] != want {
				t.Errorf("events = %q, want [%q]", events, want)
			}
			if hubSeen {
				t.Errorf("a rejected request counted as a hub contact: %+v", seen)
			}
			for _, leak := range []string{secret, "guessed"} {
				if strings.Contains(w.Body.String()+fmt.Sprint(events), leak) {
					t.Errorf("%q leaked into the response or the event", leak)
				}
			}
		})
	}
}

func TestRateLimitPerSourceIP(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	for _, path := range []string{"/st/v1/status", "/st/v1/description"} {
		s.ResetRateLimit()
		for i := 1; i <= RatePerSecond; i++ {
			if w := s.do(t, "GET", path, "192.168.1.20", "", ""); w.Code != http.StatusOK {
				t.Fatalf("%s request %d: %d, want 200", path, i, w.Code)
			}
		}
		if w := s.do(t, "GET", path, "192.168.1.20", "", ""); w.Code != http.StatusTooManyRequests {
			t.Errorf("%s request %d: %d, want 429", path, RatePerSecond+1, w.Code)
		}
		// The bucket is per source: another hub is unaffected.
		if w := s.do(t, "GET", path, "192.168.1.21", "", ""); w.Code != http.StatusOK {
			t.Errorf("%s from another source IP: %d, want 200", path, w.Code)
		}
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	// Unknown paths must not reach the legacy /{secret}/{command} handler.
	for _, path := range []string{"/st/v1/nope", "/st/v1/"} {
		if w := s.do(t, "GET", path, "192.168.1.20", "", ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
}

// ---- GET /st/v1/description (§3.6, #69) ------------------------------------

// The description is public — an unconfigured driver has no secret yet and
// the user does not know which hub IP to allow — so it must carry nothing
// secret, and an anonymous probe is not a hub contact.
func TestDescriptionIsPublicAndSecretFree(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001, Secret: "topsecret",
		SmartThings: config.SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}}})

	w := s.do(t, http.MethodGet, "/st/v1/description", "192.168.1.99", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decodeJSON(t, w)
	want := map[string]any{"protocol": float64(Protocol), "machine_id": testMachineID, "hostname": "TEST-PC",
		"service_version": testVersion, "port": float64(5001), "secret_set": true}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if strings.Contains(w.Body.String(), "topsecret") {
		t.Fatal("the description leaks the secret")
	}
	if _, ok := s.HubLastSeen(); ok {
		t.Error("the description recorded a hub contact")
	}

	// The live config: a save takes effect without a restart.
	s.setConfig(config.Config{Port: 5002})
	got = decodeJSON(t, s.do(t, http.MethodGet, "/st/v1/description", "192.168.1.99", "", ""))
	if got["secret_set"] != false || got["port"] != float64(5002) {
		t.Errorf("after a save: secret_set %v port %v, want false 5002", got["secret_set"], got["port"])
	}

	if w := s.do(t, http.MethodPost, "/st/v1/description", "192.168.1.99", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", w.Code)
	}
}

// ---- subscriptions (§3.5, #68) ---------------------------------------------

func subscribeBody(callback string, ttl int) string {
	return fmt.Sprintf(`{"callback": %q, "ttl_seconds": %d, "driver_version": "1.0.0"}`, callback, ttl)
}

// expiresIn is how far in the future a subscribe reply's expires_at is.
func expiresIn(t *testing.T, w *httptest.ResponseRecorder) time.Duration {
	t.Helper()
	expires, _ := decodeJSON(t, w)["expires_at"].(string)
	at, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.Fatalf("expires_at = %q: %v", expires, err)
	}
	return time.Until(at)
}

// TestSubscribeValidation: the callback must be plain http to the very
// private IP the request came from (§3.5, §8), and ttl_seconds is 60..3600
// with 0 meaning the 600s default.
func TestSubscribeValidation(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001, Secret: "s3cr3t"})

	w := s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "s3cr3t", subscribeBody("http://192.168.1.20:41234/pc/evt", 600))
	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d (%s)", w.Code, w.Body.String())
	}
	if id, _ := decodeJSON(t, w)["id"].(string); id == "" {
		t.Errorf("no id: %s", w.Body.String())
	}
	if d := expiresIn(t, w); d < 9*time.Minute || d > 11*time.Minute {
		t.Errorf("expires in %s, want ~10m", d)
	}
	if strings.Contains(w.Body.String(), "s3cr3t") {
		t.Errorf("the secret leaked into the subscribe response: %s", w.Body.String())
	}
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions stored, want 1", n)
	}

	s.setConfig(config.Config{Port: 5001})
	for _, tc := range []struct {
		name, from, callback string
		ttl                  int
	}{
		{"source IP mismatch", "192.168.1.20", "http://192.168.1.99:41234/pc/evt", 600},
		{"public IP", "203.0.113.7", "http://203.0.113.7:41234/pc/evt", 600},
		{"hostname", "192.168.1.20", "http://hub.local:41234/pc/evt", 600},
		{"https", "192.168.1.20", "https://192.168.1.20:41234/pc/evt", 600},
		{"empty", "192.168.1.20", "", 600},
		{"loopback from the LAN", "192.168.1.20", "http://127.0.0.1:41234/pc/evt", 600},
		{"ttl below 60", "192.168.1.20", "http://192.168.1.20:41234/pc/evt", 59},
		{"ttl above 3600", "192.168.1.20", "http://192.168.1.20:41234/pc/evt", 3601},
		{"negative ttl", "192.168.1.20", "http://192.168.1.20:41234/pc/evt", -1},
	} {
		if w := s.do(t, "POST", "/st/v1/subscribe", tc.from, "", subscribeBody(tc.callback, tc.ttl)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", tc.name, w.Code, w.Body.String())
		}
	}
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions stored after the refusals, want still 1", n)
	}

	for ttl, want := range map[int]time.Duration{0: 600 * time.Second, 60: 60 * time.Second, 3600: 3600 * time.Second} {
		w := s.do(t, "POST", "/st/v1/subscribe", "192.168.1.30", "", subscribeBody("http://192.168.1.30:41234/pc/evt", ttl))
		if w.Code != http.StatusOK {
			t.Fatalf("ttl_seconds=%d: %d (%s)", ttl, w.Code, w.Body.String())
		}
		if d := expiresIn(t, w); d > want || d < want-time.Minute {
			t.Errorf("ttl_seconds=%d expires in %s, want ~%s", ttl, d, want)
		}
	}
}

func TestSubscribeRenewsSameCallback(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	const callback = "http://192.168.1.20:41234/pc/evt"

	first := decodeJSON(t, s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(callback, 60)))
	second := decodeJSON(t, s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(callback, 3600)))
	if first["id"] != second["id"] {
		t.Errorf("re-subscribing made a new id: %v then %v", first["id"], second["id"])
	}
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions stored, want 1", n)
	}
	firstAt, _ := time.Parse(time.RFC3339, first["expires_at"].(string))
	secondAt, _ := time.Parse(time.RFC3339, second["expires_at"].(string))
	if !secondAt.After(firstAt) {
		t.Errorf("renewal did not extend the expiry: %s then %s", firstAt, secondAt)
	}

	// A different callback from the SAME host replaces the old one: the
	// hub's driver restarted on a new ephemeral port and the old listener
	// is gone.
	third := decodeJSON(t, s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody("http://192.168.1.20:41235/pc/evt", 600)))
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions stored after a same-host re-subscribe, want 1", n)
	}
	if third["id"] == first["id"] {
		t.Errorf("same-host replacement kept the old id %v", first["id"])
	}

	// A callback from another host (a second hub) is a second subscription.
	s.do(t, "POST", "/st/v1/subscribe", "192.168.1.21", "", subscribeBody("http://192.168.1.21:41234/pc/evt", 600))
	if n := s.Subscriptions(); n != 2 {
		t.Errorf("%d subscriptions stored for two hubs, want 2", n)
	}
}

func TestSubscriptionExpiresAndUnsubscribes(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	now := time.Date(2026, 9, 17, 23, 0, 0, 0, time.Local)
	s.PushNow = func() time.Time { return now }

	s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody("http://192.168.1.20:41234/pc/evt", 60))
	now = now.Add(59 * time.Second)
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions after 59s, want 1", n)
	}
	now = now.Add(2 * time.Second)
	if n := s.Subscriptions(); n != 0 {
		t.Errorf("%d subscriptions after the TTL, want 0", n)
	}

	w := s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody("http://192.168.1.20:41234/pc/evt", 600))
	id, _ := decodeJSON(t, w)["id"].(string)
	w = s.do(t, "DELETE", "/st/v1/subscribe/"+id, "192.168.1.20", "", "")
	if w.Code != http.StatusOK || decodeJSON(t, w)["removed"] != true {
		t.Fatalf("delete: %d (%s), want 200 removed:true", w.Code, w.Body.String())
	}
	if n := s.Subscriptions(); n != 0 {
		t.Errorf("%d subscriptions after DELETE, want 0", n)
	}
	// Deleting it again, or an id that never existed, is a plain false.
	w = s.do(t, "DELETE", "/st/v1/subscribe/"+id, "192.168.1.20", "", "")
	if w.Code != http.StatusOK || decodeJSON(t, w)["removed"] != false {
		t.Errorf("second delete: %d (%s), want 200 removed:false", w.Code, w.Body.String())
	}
}

// ---- delivery (§3.5) -------------------------------------------------------

// hubListener is a callback server on 127.0.0.1 answering status.
type hubListener struct {
	*httptest.Server
	got    chan string
	status atomic.Int32
	hits   atomic.Int32
}

func newHubListener(t *testing.T) *hubListener {
	t.Helper()
	h := &hubListener{got: make(chan string, 16)}
	h.status.Store(http.StatusOK)
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		w.WriteHeader(int(h.status.Load()))
		select {
		case h.got <- string(body):
		default:
		}
	}))
	t.Cleanup(h.Close)
	return h
}

func subscribe(t *testing.T, s *liveServer, url string) {
	t.Helper()
	if w := s.do(t, "POST", "/st/v1/subscribe", "127.0.0.1", "", subscribeBody(url+"/pc/evt", 600)); w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d (%s)", w.Code, w.Body.String())
	}
}

func TestPushRemovesAfterThreeFailures(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	hub := newHubListener(t)
	hub.status.Store(http.StatusInternalServerError)
	subscribe(t, s, hub.URL)

	deliver := func() {
		s.Deliver(t.Context(), "remote.received", time.Now(), map[string]string{"command": "ping"})
	}
	for i := 1; i <= PushMaxFailures; i++ {
		if n := s.Subscriptions(); n != 1 {
			t.Fatalf("subscription gone before attempt %d", i)
		}
		deliver()
	}
	if n := s.Subscriptions(); n != 0 {
		t.Errorf("%d subscriptions after %d failed deliveries, want 0", n, PushMaxFailures)
	}
	// Every failed delivery is one POST plus one retry (§3.5).
	if hits := hub.hits.Load(); hits != int32(2*PushMaxFailures) {
		t.Errorf("callback hit %d times, want %d (one retry each)", hits, 2*PushMaxFailures)
	}

	// A success in between resets the counter.
	hub.status.Store(http.StatusOK)
	subscribe(t, s, hub.URL)
	deliver()
	hub.status.Store(http.StatusInternalServerError)
	deliver()
	deliver()
	if n := s.Subscriptions(); n != 1 {
		t.Error("removed after 2 failures following a success")
	}
}

// power.stopping is delivered on the caller's goroutine (the stop path
// waits for it) but never for longer than PushStoppingDeadline; every other
// event goes through the worker.
func TestPushStoppingIsSynchronousAndBounded(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer slow.Close()
	defer close(block)
	subscribe(t, s, slow.URL)

	start := time.Now()
	s.PushTap(notify.Event{Category: "power", Kind: "stopping", At: time.Now(), Fields: map[string]string{"reason": "shutdown"}})
	took := time.Since(start)
	if took < 500*time.Millisecond {
		t.Errorf("power.stopping returned after %s — it was not delivered synchronously", took)
	}
	if took > PushStoppingDeadline+2*time.Second {
		t.Errorf("power.stopping held the stop for %s, want ≤ %s plus slack", took, PushStoppingDeadline)
	}
}

func TestPushDeliversAsynchronously(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	hub := newHubListener(t)
	subscribe(t, s, hub.URL)

	s.PushTap(notify.Event{Category: "display", Kind: "changed", At: time.Now(), Fields: map[string]string{"display": "off"}})
	select {
	case body := <-hub.got:
		if !strings.Contains(body, `"type":"display.changed"`) {
			t.Errorf("pushed %s, want display.changed", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the callback was never called")
	}
}
