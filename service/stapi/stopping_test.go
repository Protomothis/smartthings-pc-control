package stapi

// The power.stopping push at a stop (§3.5): its body is built from memory
// only, and how long it may hold the stop depends on the reason. On a real
// hub (2026-10-03) the push missed its 1.5s deadline because the status
// build had to rescan the adapters (PowerShell, about a second) first.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// countingSource counts the calls to the sources that are not memory
// reads — the adapter scan, the WTS query, the update record — and can
// make the scan slow.
type countingSource struct {
	fakeSource
	scans, sessions, updates atomic.Int32
	// scanGate, when set, holds every scan until it is closed.
	scanGate chan struct{}
}

func (c *countingSource) WoLScan() status.WoLStatus {
	c.scans.Add(1)
	if c.scanGate != nil {
		<-c.scanGate
	}
	return status.WoLStatus{Adapters: []status.NetAdapter{
		{Name: "Ethernet", MacAddress: "AA-BB-CC-DD-EE-FF", Status: "Up", WoLEnabled: true, WoLCapable: true},
	}}
}

func (c *countingSource) Session() (session.Info, error) {
	c.sessions.Add(1)
	return session.Info{Locked: true, User: "tester"}, nil
}

func (c *countingSource) Update() status.Update {
	c.updates.Add(1)
	return status.Update{Available: true, Latest: "v9.9.9"}
}

func (c *countingSource) slowCalls() (scans, sessions, updates int32) {
	return c.scans.Load(), c.sessions.Load(), c.updates.Load()
}

func newCountingServer(t *testing.T, src *countingSource) *Server {
	t.Helper()
	cfg := config.Config{Port: 5001, SmartThings: config.SmartThingsConfig{ExposeSession: true}}
	return New(Deps{
		Config:   func() config.Config { return cfg },
		Version:  func() string { return testVersion },
		Emit:     func(string, string, map[string]string) {},
		Status:   src,
		Commands: fakeCommands{},
		Awake:    fakeAwake{},
		Media:    fakeMedia{},
		Presets:  fakePresets{},
		Notify:   fakeNotifier{},
	})
}

// subscribeServer registers url on s the way the hub does.
func subscribeServer(t *testing.T, s *Server, url string) {
	t.Helper()
	s.subs.subscribe(url+"/pc/evt", "1.0.0", time.Minute*10)
}

func stoppingEvent(reason string) notify.Event {
	return notify.Event{Category: "power", Kind: "stopping", At: time.Now(), Fields: map[string]string{"reason": reason}}
}

// After a status build, the stopping push asks none of the slow sources —
// even with the adapter scan long expired, which is what made it miss the
// deadline — and still carries what that build saw.
func TestStoppingPushUsesOnlyCachedState(t *testing.T) {
	src := &countingSource{}
	s := newCountingServer(t, src)
	hub := newHubListener(t)
	subscribeServer(t, s, hub.URL)

	s.BuildStatus(s.d.Config()) // a poll: scan, WTS, update record
	if sc, se, up := src.slowCalls(); sc != 1 || se != 1 || up != 1 {
		t.Fatalf("a status build made %d scans, %d session and %d update calls, want 1 each", sc, se, up)
	}
	// The scan is an hour old: a status build would rescan now.
	s.wol.mu.Lock()
	s.wol.at = time.Now().Add(-time.Hour)
	s.wol.mu.Unlock()
	src.scans.Store(0)
	src.sessions.Store(0)
	src.updates.Store(0)

	start := time.Now()
	s.PushTap(stoppingEvent("app_stop"))
	took := time.Since(start)
	if sc, se, up := src.slowCalls(); sc != 0 || se != 0 || up != 0 {
		t.Errorf("the stopping push made %d scans, %d session and %d update calls, want none", sc, se, up)
	}
	if took > 500*time.Millisecond {
		t.Errorf("the stopping push took %s against a hub that answers at once", took)
	}

	var body struct {
		Type   string `json:"type"`
		Status Status `json:"status"`
	}
	select {
	case raw := <-hub.got:
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stopping push did not reach the hub")
	}
	if body.Type != "power.stopping" {
		t.Errorf("type = %q", body.Type)
	}
	st := body.Status
	if st.WoL.Selected == nil || st.WoL.Selected.MAC != "AA-BB-CC-DD-EE-FF" {
		t.Errorf("wol block = %+v, want the cached adapter", st.WoL)
	}
	if st.Session.Locked == nil || !*st.Session.Locked {
		t.Errorf("session = %+v, want the last known lock state", st.Session)
	}
	if !st.Update.Available || st.Update.Latest != "v9.9.9" {
		t.Errorf("update = %+v, want the last known one", st.Update)
	}
}

// Before any status build (no poll yet) the push still asks no slow
// source: no adapters, the lock state null.
func TestStoppingPushBeforeAnyStatusScansNothing(t *testing.T) {
	src := &countingSource{}
	s := newCountingServer(t, src)
	hub := newHubListener(t)
	subscribeServer(t, s, hub.URL)

	s.PushTap(stoppingEvent("shutdown"))
	if sc, se, _ := src.slowCalls(); sc != 0 || se != 0 {
		t.Errorf("the stopping push made %d scans and %d session calls, want none", sc, se)
	}
	select {
	case raw := <-hub.got:
		var body struct{ Status Status }
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		if body.Status.WoL.Selected != nil || body.Status.Session.Locked != nil {
			t.Errorf("wol %+v, session %+v: want empty, nothing was cached", body.Status.WoL, body.Status.Session)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stopping push did not reach the hub")
	}
}

// A hub poll that is rescanning the adapters (about a second of
// PowerShell) when the stop arrives must not hold the push up.
func TestStoppingPushDoesNotWaitForARunningScan(t *testing.T) {
	src := &countingSource{scanGate: make(chan struct{})}
	s := newCountingServer(t, src)
	hub := newHubListener(t)
	subscribeServer(t, s, hub.URL)

	polled := make(chan struct{})
	go func() {
		defer close(polled)
		s.BuildStatus(s.d.Config()) // blocks in WoLScan until the gate opens
	}()
	for src.scans.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	done := make(chan time.Duration)
	go func() {
		start := time.Now()
		s.PushTap(stoppingEvent("app_stop"))
		done <- time.Since(start)
	}()
	select {
	case took := <-done:
		if took > 500*time.Millisecond {
			t.Errorf("the stopping push took %s", took)
		}
	case <-time.After(3 * time.Second):
		t.Error("the stopping push waited for the running scan")
	}
	close(src.scanGate)
	<-polled
	if n := hub.hits.Load(); n != 1 {
		t.Errorf("hub hit %d times, want 1", n)
	}
}

func TestStoppingDeadlinePerReason(t *testing.T) {
	for reason, want := range map[string]time.Duration{
		"app_stop":  PushAppStopDeadline,
		"shutdown":  PushStoppingDeadline,
		"restart":   PushStoppingDeadline,
		"suspend":   PushStoppingDeadline,
		"hibernate": PushStoppingDeadline,
		"unknown":   PushStoppingDeadline,
		"":          PushStoppingDeadline,
	} {
		if got := StoppingDeadline(reason); got != want {
			t.Errorf("StoppingDeadline(%q) = %s, want %s", reason, got, want)
		}
	}
	if PushAppStopDeadline <= pushTimeout {
		t.Errorf("app_stop deadline %s leaves no room for the retry after a %s attempt", PushAppStopDeadline, pushTimeout)
	}
}

// app_stop waits out a hub that does not answer for longer than the OS
// deadline — one full attempt and the retry — but no longer than its own.
func TestPushAppStopHoldsTheLongerDeadline(t *testing.T) {
	t.Parallel()
	s := newLiveServer(t, config.Config{Port: 5001})
	block := make(chan struct{})
	var hits atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
		<-block
	}))
	defer slow.Close()
	defer close(block)
	subscribe(t, s, slow.URL)

	start := time.Now()
	s.PushTap(stoppingEvent("app_stop"))
	took := time.Since(start)
	if took < PushStoppingDeadline+500*time.Millisecond {
		t.Errorf("app_stop gave up after %s, want about %s", took, PushAppStopDeadline)
	}
	if took > PushAppStopDeadline+time.Second {
		t.Errorf("app_stop held the stop for %s, want ≤ %s plus slack", took, PushAppStopDeadline)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("hub hit %d times, want the attempt and the retry", n)
	}
}
