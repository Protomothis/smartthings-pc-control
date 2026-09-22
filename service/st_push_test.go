package service

// Tests for the hub push subscriptions and the SmartThings push sink
// (edge-driver doc §4.5, #68).

import (
	"context"
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

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// stPushSetup gives one test the /st/v1 fixtures plus an empty
// subscription store and the real clock back afterwards.
func stPushSetup(t *testing.T, cfg Config) {
	t.Helper()
	stSetup(t, cfg)
	stPushReset()
	resetPowerCommandHint()
	orig := stPushNow
	t.Cleanup(func() {
		stPushNow = orig
		stPushReset()
		resetPowerCommandHint()
	})
}

// subscribeBody builds a POST /st/v1/subscribe body.
func subscribeBody(callback string, ttl int) string {
	return fmt.Sprintf(`{"callback": %q, "ttl_seconds": %d, "driver_version": "1.0.0"}`, callback, ttl)
}

// callbackServer records every push body it is given and answers status.
type callbackServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	got    chan struct{}
	status atomic.Int32
	hits   atomic.Int32
}

// newCallbackServer starts a hub listener on 127.0.0.1 answering 200.
func newCallbackServer(t *testing.T) *callbackServer {
	t.Helper()
	c := &callbackServer{got: make(chan struct{}, 16)}
	c.status.Store(http.StatusOK)
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		w.WriteHeader(int(c.status.Load()))
		select {
		case c.got <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(c.Close)
	return c
}

// wait blocks until the callback has been hit, or fails.
func (c *callbackServer) wait(t *testing.T) map[string]any {
	t.Helper()
	select {
	case <-c.got:
	case <-time.After(5 * time.Second):
		t.Fatal("the callback was never called")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out map[string]any
	if err := json.Unmarshal(c.bodies[len(c.bodies)-1], &out); err != nil {
		t.Fatalf("push body is not JSON: %v", err)
	}
	return out
}

// subscribeTo registers cb (a 127.0.0.1 callback) and returns its id.
func subscribeTo(t *testing.T, cb *callbackServer, ttl int) string {
	t.Helper()
	w := stDo(t, "POST", "/st/v1/subscribe", "127.0.0.1", getConfig().Secret, subscribeBody(cb.URL+"/pc/evt", ttl))
	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d (%s)", w.Code, w.Body.String())
	}
	id, _ := stJSON(t, w)["id"].(string)
	if id == "" {
		t.Fatalf("subscribe returned no id: %s", w.Body.String())
	}
	return id
}

// ---- subscribe validation (§4.5) -------------------------------------------

func TestSTSubscribeAcceptsHubCallback(t *testing.T) {
	stPushSetup(t, Config{Port: 5001, Secret: "s3cr3t"})

	w := stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "s3cr3t",
		subscribeBody("http://192.168.1.20:41234/pc/evt", 600))
	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d (%s)", w.Code, w.Body.String())
	}
	got := stJSON(t, w)
	id, _ := got["id"].(string)
	if id == "" {
		t.Errorf("id = %v", got["id"])
	}
	expires, _ := got["expires_at"].(string)
	at, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.Fatalf("expires_at = %q: %v", expires, err)
	}
	if d := time.Until(at); d < 9*time.Minute || d > 10*time.Minute+time.Minute {
		t.Errorf("expires in %s, want ~10m", d)
	}
	if n := len(stSubs.active()); n != 1 {
		t.Errorf("%d subscriptions stored, want 1", n)
	}
	// The secret must not come back in the response (§8).
	if body := w.Body.String(); strings.Contains(body, "s3cr3t") {
		t.Errorf("the secret leaked into the subscribe response: %s", body)
	}
}

func TestSTSubscribeRejectsBadCallbacks(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})

	for _, tc := range []struct{ name, from, callback string }{
		{"source IP mismatch", "192.168.1.20", "http://192.168.1.99:41234/pc/evt"},
		{"public IP", "203.0.113.7", "http://203.0.113.7:41234/pc/evt"},
		{"hostname", "192.168.1.20", "http://hub.local:41234/pc/evt"},
		{"https", "192.168.1.20", "https://192.168.1.20:41234/pc/evt"},
		{"empty", "192.168.1.20", ""},
		{"loopback from the LAN", "192.168.1.20", "http://127.0.0.1:41234/pc/evt"},
	} {
		w := stDo(t, "POST", "/st/v1/subscribe", tc.from, "", subscribeBody(tc.callback, 600))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", tc.name, w.Code, w.Body.String())
		}
	}
	if n := len(stSubs.active()); n != 0 {
		t.Errorf("%d subscriptions stored, want 0", n)
	}
}

func TestSTSubscribeTTLBounds(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})

	for _, ttl := range []int{59, 3601, -1} {
		w := stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "",
			subscribeBody("http://192.168.1.20:41234/pc/evt", ttl))
		if w.Code != http.StatusBadRequest {
			t.Errorf("ttl_seconds=%d: %d, want 400", ttl, w.Code)
		}
	}

	// Omitted (0) falls back to the 600s default, and the bounds
	// themselves are accepted.
	for ttl, want := range map[int]time.Duration{0: stSubDefaultTTL, 60: stSubMinTTL, 3600: stSubMaxTTL} {
		stPushReset()
		w := stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "",
			subscribeBody("http://192.168.1.20:41234/pc/evt", ttl))
		if w.Code != http.StatusOK {
			t.Fatalf("ttl_seconds=%d: %d (%s)", ttl, w.Code, w.Body.String())
		}
		expires, _ := stJSON(t, w)["expires_at"].(string)
		at, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			t.Fatalf("expires_at = %q: %v", expires, err)
		}
		if d := time.Until(at); d > want || d < want-time.Minute {
			t.Errorf("ttl_seconds=%d expires in %s, want ~%s", ttl, d, want)
		}
	}
}

func TestSTSubscribeRenewsSameCallback(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	const callback = "http://192.168.1.20:41234/pc/evt"

	first := stJSON(t, stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(callback, 60)))
	second := stJSON(t, stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(callback, 3600)))

	if first["id"] != second["id"] {
		t.Errorf("re-subscribing made a new id: %v then %v", first["id"], second["id"])
	}
	if n := len(stSubs.active()); n != 1 {
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
	third := stJSON(t, stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody("http://192.168.1.20:41235/pc/evt", 600)))
	if n := len(stSubs.active()); n != 1 {
		t.Errorf("%d subscriptions stored after a same-host re-subscribe, want 1", n)
	}
	if third["id"] == first["id"] {
		t.Errorf("same-host replacement kept the old id %v", first["id"])
	}

	// A callback from another host (a second hub) is a second subscription.
	stDo(t, "POST", "/st/v1/subscribe", "192.168.1.21", "", subscribeBody("http://192.168.1.21:41234/pc/evt", 600))
	if n := len(stSubs.active()); n != 2 {
		t.Errorf("%d subscriptions stored for two hubs, want 2", n)
	}
}

func TestSTSubscriptionExpires(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	now := time.Date(2026, 9, 17, 23, 0, 0, 0, time.Local)
	stPushNow = func() time.Time { return now }

	stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "",
		subscribeBody("http://192.168.1.20:41234/pc/evt", 60))
	if n := len(stSubs.active()); n != 1 {
		t.Fatalf("%d subscriptions right after subscribing", n)
	}

	now = now.Add(59 * time.Second)
	if n := len(stSubs.active()); n != 1 {
		t.Errorf("%d subscriptions after 59s, want 1", n)
	}
	now = now.Add(2 * time.Second)
	if n := len(stSubs.active()); n != 0 {
		t.Errorf("%d subscriptions after the TTL, want 0", n)
	}
}

func TestSTUnsubscribe(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})

	w := stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "",
		subscribeBody("http://192.168.1.20:41234/pc/evt", 600))
	id, _ := stJSON(t, w)["id"].(string)

	w = stDo(t, "DELETE", "/st/v1/subscribe/"+id, "192.168.1.20", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d (%s)", w.Code, w.Body.String())
	}
	if removed, _ := stJSON(t, w)["removed"].(bool); !removed {
		t.Error(`removed = false, want true`)
	}
	if n := len(stSubs.active()); n != 0 {
		t.Errorf("%d subscriptions after DELETE, want 0", n)
	}

	// Deleting it again, or an id that never existed, is a plain false.
	w = stDo(t, "DELETE", "/st/v1/subscribe/"+id, "192.168.1.20", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("second delete: %d", w.Code)
	}
	if removed, _ := stJSON(t, w)["removed"].(bool); removed {
		t.Error("removed = true for an unknown id")
	}
}

func TestSTSubscribeNeedsAuth(t *testing.T) {
	stPushSetup(t, Config{Port: 5001, Secret: "s3cr3t"})

	w := stDo(t, "POST", "/st/v1/subscribe", "192.168.1.20", "",
		subscribeBody("http://192.168.1.20:41234/pc/evt", 600))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("subscribe without the secret: %d, want 401", w.Code)
	}
	if w := stDo(t, "DELETE", "/st/v1/subscribe/sub-1", "192.168.1.20", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("unsubscribe without the secret: %d, want 401", w.Code)
	}
}

// ---- delivery (§4.5) -------------------------------------------------------

func TestSTPushBodyShape(t *testing.T) {
	stPushSetup(t, Config{Port: 5001, Secret: "s3cr3t", ShutdownGrace: true, GraceSeconds: 300})
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	at := time.Date(2026, 9, 17, 23, 5, 0, 0, time.Local)
	stPushDispatch(context.Background(), stPushJob{
		Type: "schedule.created",
		At:   at,
		Data: map[string]string{"command": "shutdown", "origin": "smartthings"},
	})
	got := cb.wait(t)

	if got["protocol"] != float64(stProtocol) {
		t.Errorf("protocol = %v", got["protocol"])
	}
	if got["type"] != "schedule.created" {
		t.Errorf("type = %v", got["type"])
	}
	// machine_id is repeated at the top level so a hub serving several PCs
	// can route the event without parsing status.
	if got["machine_id"] != machineID() {
		t.Errorf("machine_id = %v, want %v", got["machine_id"], machineID())
	}
	if ts, err := time.Parse(time.RFC3339, fmt.Sprint(got["at"])); err != nil || !ts.Equal(at) {
		t.Errorf("at = %v (%v)", got["at"], err)
	}
	data, ok := got["data"].(map[string]any)
	if !ok || data["command"] != "shutdown" || data["origin"] != "smartthings" {
		t.Errorf("data = %v", got["data"])
	}
	status, ok := got["status"].(map[string]any)
	if !ok {
		t.Fatalf("status = %v, want the full status object", got["status"])
	}
	for _, key := range []string{"protocol", "service_version", "machine_id", "hostname",
		"power", "uptime_seconds", "secret_set", "grace", "schedule", "update", "wol",
		"display", "session"} {
		if _, ok := status[key]; !ok {
			t.Errorf("status is missing %q", key)
		}
	}
	if status["machine_id"] != got["machine_id"] {
		t.Errorf("status.machine_id %v != top-level %v", status["machine_id"], got["machine_id"])
	}
	// The secret is never in a push body (§8) — only "a secret is set".
	cb.mu.Lock()
	raw := string(cb.bodies[len(cb.bodies)-1])
	cb.mu.Unlock()
	if strings.Contains(raw, "s3cr3t") {
		t.Errorf("the secret leaked into the push body: %s", raw)
	}
	if status["secret_set"] != true {
		t.Errorf("status.secret_set = %v, want true", status["secret_set"])
	}
}

func TestSTPushDropsSecretValuedFields(t *testing.T) {
	stPushSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	got := stPushData(map[string]string{"command": "shutdown", "leak": "s3cr3t"}, "s3cr3t")
	if _, ok := got["leak"]; ok {
		t.Errorf("a field holding the secret was kept: %v", got)
	}
	if got["command"] != "shutdown" {
		t.Errorf("data = %v", got)
	}
}

func TestSTPushRemovesAfterThreeFailures(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	cb := newCallbackServer(t)
	cb.status.Store(http.StatusInternalServerError)
	id := subscribeTo(t, cb, 600)

	job := stPushJob{Type: "remote.received", At: time.Now(), Data: map[string]string{"command": "ping"}}
	for i := 1; i <= stPushMaxFailures; i++ {
		if n := len(stSubs.active()); n != 1 && i <= stPushMaxFailures {
			t.Fatalf("subscription gone before attempt %d", i)
		}
		stPushDispatch(context.Background(), job)
	}
	if n := len(stSubs.active()); n != 0 {
		t.Errorf("%d subscriptions after %d failed deliveries, want 0", n, stPushMaxFailures)
	}
	// Every failed delivery is one POST plus one retry (§4.5).
	if hits := cb.hits.Load(); hits != int32(2*stPushMaxFailures) {
		t.Errorf("callback hit %d times, want %d (one retry each)", hits, 2*stPushMaxFailures)
	}

	// A success in between resets the counter.
	stPushReset()
	cb.status.Store(http.StatusOK)
	id = subscribeTo(t, cb, 600)
	stPushDispatch(context.Background(), job)
	cb.status.Store(http.StatusInternalServerError)
	stPushDispatch(context.Background(), job)
	stPushDispatch(context.Background(), job)
	if n := len(stSubs.active()); n != 1 {
		t.Errorf("%s removed after 2 failures following a success", id)
	}
}

func TestSTPushStoppingIsSynchronousAndBounded(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	// A callback that never answers: the deadline, not the client, has to
	// release the stop path.
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer slow.Close()
	defer close(block)

	w := stDo(t, "POST", "/st/v1/subscribe", "127.0.0.1", "", subscribeBody(slow.URL+"/pc/evt", 600))
	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d (%s)", w.Code, w.Body.String())
	}

	start := time.Now()
	stPushTap(notify.Event{
		Category: "power", Kind: "stopping", At: time.Now(),
		Fields: map[string]string{"reason": "shutdown"},
	})
	took := time.Since(start)
	// It blocked (the point of the synchronous path) but not for longer
	// than the §4.5 budget.
	if took < 500*time.Millisecond {
		t.Errorf("power.stopping returned after %s — it was not delivered synchronously", took)
	}
	if took > stPushStoppingDeadline+2*time.Second {
		t.Errorf("power.stopping held the stop for %s, want ≤ %s plus slack", took, stPushStoppingDeadline)
	}
}

func TestSTPushDeliversAsynchronously(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	stPushTap(notify.Event{
		Category: "display", Kind: "changed", At: time.Now(),
		Fields: map[string]string{"display": "off"},
	})
	if got := cb.wait(t); got["type"] != "display.changed" {
		t.Errorf("type = %v, want display.changed", got["type"])
	}
}

// ---- event mapping (§4.5) --------------------------------------------------

func TestSTPushEventSelection(t *testing.T) {
	exposed := SmartThingsConfig{ExposeSession: true}
	var hidden SmartThingsConfig

	for _, tc := range []struct {
		cat, kind string
		cfg       SmartThingsConfig
		want      bool
	}{
		{"power", "stopping", hidden, true},
		{"power", "started", hidden, true},
		{"power", "resumed", hidden, true},
		{"schedule", "created", hidden, true},
		{"schedule", "cancelled", hidden, true},
		{"remote", "received", hidden, true},
		{"remote", "grace_scheduled", hidden, true},
		{"system", "updated", hidden, true},
		{"system", "update_available", hidden, true},
		{"display", "changed", hidden, true},
		{"session", "locked", exposed, true},
		{"session", "unlocked", exposed, true},
		{"session", "locked", hidden, false},
		{"security", "unauthorized", hidden, false},
		{"system", "exec_failed", hidden, false},
		{"system", "digest", hidden, false},
	} {
		typ, ok := stPushEventType(notify.Event{Category: tc.cat, Kind: tc.kind}, tc.cfg)
		if ok != tc.want {
			t.Errorf("%s.%s pushed = %v, want %v", tc.cat, tc.kind, ok, tc.want)
		}
		if ok && typ != tc.cat+"."+tc.kind {
			t.Errorf("type = %q, want %s.%s", typ, tc.cat, tc.kind)
		}
	}
}

// TestSTPushTapBypassesNotifyFilters is the §4.5 requirement that the hub
// sees device state even when the user has switched the matching
// notification off (or is in quiet hours): the Telegram sink gets nothing,
// the callback gets the event.
func TestSTPushTapBypassesNotifyFilters(t *testing.T) {
	stPushSetup(t, Config{
		Port: 5001,
		// power.stopping and schedule.created are off, so nothing on this
		// bus may reach a notification sink.
		Notify: notify.Config{
			"power":    {"stopping": false},
			"schedule": {"created": false},
		},
		Telegram: TelegramConfig{QuietHours: notify.QuietHours{Enabled: true, Start: "00:00", End: "23:59"}},
	})
	events := captureNotifications(t) // installs a bus with the push tap
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	emit("schedule", "created", map[string]string{"command": "shutdown"})
	if got := cb.wait(t); got["type"] != "schedule.created" {
		t.Errorf("pushed type = %v, want schedule.created", got["type"])
	}
	expectNoNotification(t, events)
}

// ---- power.stopping reason (§4.5, §6.2) ------------------------------------

func TestStoppingReasonFromLastCommand(t *testing.T) {
	resetPowerCommandHint()
	t.Cleanup(resetPowerCommandHint)

	// Nothing ran: the caller's own guess stands.
	if got := stoppingReason("shutdown"); got != "shutdown" {
		t.Errorf("without a hint = %q, want the fallback", got)
	}
	if got := stoppingReason("unknown"); got != "unknown" {
		t.Errorf("plain service stop = %q, want unknown", got)
	}

	for cmd, want := range map[string]string{
		"shutdown":      "shutdown",
		"forceshutdown": "shutdown",
		"restart":       "restart",
		"suspend":       "suspend",
		"hibernate":     "hibernate",
	} {
		resetPowerCommandHint()
		notePowerCommand(cmd)
		if got := stoppingReason("shutdown"); got != want {
			t.Errorf("after %s: reason = %q, want %q", cmd, got, want)
		}
	}

	// Commands that do not end the session leave the hint alone.
	resetPowerCommandHint()
	notePowerCommand("restart")
	for _, cmd := range []string{"ping", "lock", "turnscreenoff", "turnscreenon"} {
		notePowerCommand(cmd)
	}
	if got := stoppingReason("shutdown"); got != "restart" {
		t.Errorf("reason = %q, want restart (harmless commands must not clear the hint)", got)
	}

	// A stale hint is ignored.
	lastPowerCommandMu.Lock()
	lastPowerCommandAt = time.Now().Add(-2 * powerCommandHintTTL)
	lastPowerCommandMu.Unlock()
	if got := stoppingReason("shutdown"); got != "shutdown" {
		t.Errorf("stale hint = %q, want the fallback", got)
	}
}

// ---- display.changed (§4.5) ------------------------------------------------

func TestDisplayChangedPushesOnce(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	startNotifier(nil) // a bus with the push tap, no notification sink
	t.Cleanup(stopNotifier)
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)
	setDisplayState("unknown")

	setDisplayState("off")
	t.Cleanup(func() { setDisplayState("unknown") })
	got := cb.wait(t)
	if got["type"] != "display.changed" {
		t.Fatalf("type = %v", got["type"])
	}
	if data, _ := got["data"].(map[string]any); data["display"] != "off" {
		t.Errorf("data = %v", got["data"])
	}

	// Setting the same state again is not a change.
	before := cb.hits.Load()
	setDisplayState("off")
	time.Sleep(200 * time.Millisecond)
	if after := cb.hits.Load(); after != before {
		t.Errorf("an unchanged display state pushed again (%d → %d)", before, after)
	}
}

// ---- update (§4.2) ---------------------------------------------------------

func TestSTStatusUpdateUsesCheckerCache(t *testing.T) {
	orig := latestRelease.Load()
	origVersion := Version
	Version = "v1.1.0"
	t.Cleanup(func() {
		Version = origVersion
		if s, ok := orig.(string); ok {
			latestRelease.Store(s)
		} else {
			latestRelease.Store("")
		}
	})

	// A release older than or equal to this build: latest is reported, but
	// nothing is available.
	noteLatestRelease("v0.0.1")
	if got := stUpdateInfo(); got.Available || got.Latest != "v0.0.1" {
		t.Errorf("update = %+v, want {false v0.0.1}", got)
	}

	noteLatestRelease("v99.0.0")
	if got := stUpdateInfo(); !got.Available || got.Latest != "v99.0.0" {
		t.Errorf("update = %+v, want {true v99.0.0}", got)
	}
}
