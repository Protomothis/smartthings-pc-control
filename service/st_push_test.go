package service

// Tests for the hub push subscriptions and the SmartThings push sink
// (edge-driver doc §3.5, #68).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	stSrv.RestartSubscriptionIDs()
	resetPowerCommandHint()
	orig := stSrv.PushNow
	t.Cleanup(func() {
		stPushFlush(t)
		stSrv.PushNow = orig
		stSrv.RestartSubscriptionIDs()
		resetPowerCommandHint()
	})
}

// stPushFlush waits until the async push worker has delivered everything
// queued so far, so a delivery left over from this test cannot read
// package state (Version, the config) that the next test is rewriting.
func stPushFlush(t *testing.T) {
	t.Helper()
	if !stSrv.FlushPush(5 * time.Second) {
		t.Error("ST push worker did not drain within 5s")
	}
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

// Subscribe validation, expiry and delivery (retries, removal, the
// synchronous power.stopping) are tested in service/stapi (http_test.go);
// the push bodies by TestContractPushes.

// ---- event mapping (§3.5) --------------------------------------------------

// TestSTPushTapBypassesNotifyFilters is the §3.5 requirement that the hub
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

// ---- power.stopping reason (§3.5, §6.2) ------------------------------------

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
	powerHint.Now = func() time.Time { return time.Now().Add(4 * time.Minute) }
	t.Cleanup(func() { powerHint.Now = nil })
	if got := stoppingReason("shutdown"); got != "shutdown" {
		t.Errorf("stale hint = %q, want the fallback", got)
	}
}

// ---- display.changed (§3.5) ------------------------------------------------

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

// ---- update (§3.2) ---------------------------------------------------------

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
