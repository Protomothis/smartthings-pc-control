package stapi

// #142: the push subscriptions survive a service restart, and
// power.started / power.resumed also reach the hubs whose subscription ran
// out while the PC was off.

import (
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

// memSubs is a SubscriptionStore in memory: what state.json would hold.
type memSubs struct {
	mu    sync.Mutex
	list  []SavedSubscription
	saves int
}

func (m *memSubs) LoadSubscriptions() []SavedSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SavedSubscription(nil), m.list...)
}

func (m *memSubs) SaveSubscriptions(list []SavedSubscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list = append([]SavedSubscription(nil), list...)
	m.saves++
	return nil
}

func (m *memSubs) saved() []SavedSubscription { return m.LoadSubscriptions() }

// newStoredServer is a liveServer writing its subscriptions to store, on a
// clock the test moves.
func newStoredServer(t *testing.T, store *memSubs, now *time.Time) *liveServer {
	t.Helper()
	l := newLiveServer(t, config.Config{Port: 5001})
	d := l.d
	d.Subscriptions = store
	l.Server = New(d)
	l.PushNow = func() time.Time { return *now }
	return l
}

func TestSubscriptionsSurviveARestart(t *testing.T) {
	store := &memSubs{}
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	const hubA, hubB = "http://192.168.1.20:41234/pc/evt", "http://192.168.1.21:41234/pc/evt"

	s := newStoredServer(t, store, &now)
	idA := decodeJSON(t, s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(hubA, 600)))["id"]
	idB := decodeJSON(t, s.do(t, "POST", "/st/v1/subscribe", "192.168.1.21", "", subscribeBody(hubB, 600)))["id"]
	if got := store.saved(); len(got) != 2 || got[0].Callback != hubA || got[0].ID != idA {
		t.Fatalf("saved %+v, want both hubs, A first", got)
	}
	// A renewal is written through too: its expiry is what a restart reads.
	now = now.Add(8 * time.Minute)
	s.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(hubA, 600))
	if got := store.saved()[0].ExpiresAt; !got.Equal(now.Add(600 * time.Second)) {
		t.Errorf("saved expiry %s after the renewal, want %s", got, now.Add(600*time.Second))
	}
	// A hub that unsubscribes is gone from the file as well.
	s.do(t, "DELETE", "/st/v1/subscribe/"+idB.(string), "192.168.1.21", "", "")
	if got := store.saved(); len(got) != 1 || got[0].Callback != hubA {
		t.Fatalf("saved %+v after DELETE, want hub A only", got)
	}

	// The service restarts five minutes later: hub A's subscription is live
	// again under its old id, and its next renewal finds it.
	now = now.Add(5 * time.Minute)
	restarted := newStoredServer(t, store, &now)
	restarted.RestoreSubscriptions()
	if n := restarted.Subscriptions(); n != 1 {
		t.Fatalf("%d subscriptions after the restart, want 1", n)
	}
	renewed := decodeJSON(t, restarted.do(t, "POST", "/st/v1/subscribe", "192.168.1.20", "", subscribeBody(hubA, 600)))
	if renewed["id"] != idA {
		t.Errorf("renewal after the restart got id %v, want %v", renewed["id"], idA)
	}
	// A new subscription never takes the id a restored one holds.
	other := decodeJSON(t, restarted.do(t, "POST", "/st/v1/subscribe", "192.168.1.22", "", subscribeBody("http://192.168.1.22:41234/pc/evt", 600)))
	if other["id"] == idA {
		t.Errorf("a new subscription after the restart got id %v", other["id"])
	}
}

func TestRestoreSkipsWhatSubscribeWouldRefuse(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	store := &memSubs{list: []SavedSubscription{
		{ID: "sub-1", Callback: "http://203.0.113.7:41234/pc/evt", ExpiresAt: now.Add(time.Hour)},
		{ID: "sub-2", Callback: "https://192.168.1.20:41234/pc/evt", ExpiresAt: now.Add(time.Hour)},
		{ID: "sub-3", Callback: "http://hub.local:41234/pc/evt", ExpiresAt: now.Add(time.Hour)},
		{ID: "sub-4", Callback: "http://192.168.1.20:41234/pc/evt", ExpiresAt: now.Add(-8 * 24 * time.Hour)},
		{ID: "sub-5", Callback: "http://192.168.1.21:41234/pc/evt", ExpiresAt: now.Add(time.Hour)},
		// The same id twice (a hand-edited file): both are kept, one renamed.
		{ID: "sub-5", Callback: "http://192.168.1.22:41234/pc/evt", ExpiresAt: now.Add(time.Hour)},
	}}
	s := newStoredServer(t, store, &now)
	s.RestoreSubscriptions()
	live, lapsed := s.subs.wakeTargets()
	if len(live) != 2 || lapsed != 0 {
		t.Errorf("restored %d (%d lapsed), want the two private http callbacks only: %+v", len(live), lapsed, live)
	}
	if len(live) == 2 && live[0].ID == live[1].ID {
		t.Errorf("two restored subscriptions share id %s", live[0].ID)
	}
}

// wakeHub is a hub listener that refuses the first fail requests (500) and
// reports every body it accepted.
type wakeHub struct {
	*httptest.Server
	fail int32
	hits atomic.Int32
	got  chan string
}

func newWakeHub(t *testing.T, fail int32) *wakeHub {
	t.Helper()
	h := &wakeHub{fail: fail, got: make(chan string, 16)}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.hits.Add(1) <= h.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		h.got <- b.String()
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *wakeHub) expect(t *testing.T, typ string) {
	t.Helper()
	select {
	case body := <-h.got:
		if !strings.Contains(body, `"type":"`+typ+`"`) {
			t.Errorf("hub got %.80s…, want %s", body, typ)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never reached the hub", typ)
	}
}

func (h *wakeHub) expectNothing(t *testing.T, s *liveServer) {
	t.Helper()
	if !s.FlushPush(5 * time.Second) {
		t.Fatal("push worker did not drain")
	}
	select {
	case body := <-h.got:
		t.Errorf("hub got %.80s…, want nothing", body)
	default:
	}
}

func TestWakeEventsReachLapsedSubscriptions(t *testing.T) {
	now := time.Now()
	recent, stale := newWakeHub(t, 0), newWakeHub(t, 0)
	store := &memSubs{list: []SavedSubscription{
		// The PC was off for two days: long past the TTL.
		{ID: "sub-7", Callback: recent.URL + "/pc/evt", ExpiresAt: now.Add(-48 * time.Hour)},
		// Off for longer than the recall window: that hub is not told.
		{ID: "sub-3", Callback: stale.URL + "/pc/evt", ExpiresAt: now.Add(-subRecallWindow - time.Hour)},
	}}
	s := newStoredServer(t, store, &now)
	s.RestoreSubscriptions()
	if n := s.Subscriptions(); n != 0 {
		t.Fatalf("%d live subscriptions, want none (the saved one expired)", n)
	}

	// Ordinary device state does not go to a lapsed subscription…
	s.PushTap(notify.Event{Category: "display", Kind: "changed", At: now, Fields: map[string]string{"display": "on"}})
	recent.expectNothing(t, s)
	// …the PC coming up does, once per event.
	s.PushTap(notify.Event{Category: "power", Kind: "started", At: now, Fields: map[string]string{"version": "v1.3.0"}})
	recent.expect(t, "power.started")
	s.PushTap(notify.Event{Category: "power", Kind: "resumed", At: now, Fields: map[string]string{"since": "2h 05m"}})
	recent.expect(t, "power.resumed")
	stale.expectNothing(t, s)
	if hits := stale.hits.Load(); hits != 0 {
		t.Errorf("the stale hub was called %d times", hits)
	}

	// The driver subscribes again for that callback: live, with a new id,
	// and no longer recalled on top.
	s.do(t, "POST", "/st/v1/subscribe", "127.0.0.1", "", subscribeBody(recent.URL+"/pc/evt", 600))
	if subs, lapsed := s.subs.wakeTargets(); len(subs) != 1 || lapsed != 0 || subs[0].ID == "sub-7" {
		t.Errorf("after re-subscribing: %+v (%d lapsed), want one live subscription with a new id", subs, lapsed)
	}
}

func TestSubscriptionThatFailedOutIsStillRecalled(t *testing.T) {
	s := newLiveServer(t, config.Config{Port: 5001})
	hub := newWakeHub(t, 2*PushMaxFailures)
	subscribe(t, s, hub.URL)
	for range PushMaxFailures {
		s.Deliver(t.Context(), "remote.received", time.Now(), map[string]string{"command": "ping"})
	}
	if n := s.Subscriptions(); n != 0 {
		t.Fatalf("%d subscriptions after %d failures, want 0", n, PushMaxFailures)
	}
	s.Deliver(t.Context(), "power.started", time.Now(), nil)
	hub.expect(t, "power.started")
}

func TestWakePushIsRetriedWhileTheNetworkComesUp(t *testing.T) {
	orig := wakeRetryDelays
	wakeRetryDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { wakeRetryDelays = orig })

	s := newLiveServer(t, config.Config{Port: 5001})
	// The first attempt and its immediate retry fail, the delayed one lands.
	hub := newWakeHub(t, 2)
	subscribe(t, s, hub.URL)
	s.Deliver(t.Context(), "power.started", time.Now(), nil)
	hub.expect(t, "power.started")
	if hits := hub.hits.Load(); hits != 3 {
		t.Errorf("hub called %d times, want 3", hits)
	}
	// The success after the retry clears the failure the first attempt
	// counted: two more failures do not remove it.
	hub.fail = 100
	s.Deliver(t.Context(), "remote.received", time.Now(), nil)
	s.Deliver(t.Context(), "remote.received", time.Now(), nil)
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions, want 1: the delayed success should have reset the count", n)
	}

	// Anything else is not retried.
	other := newWakeHub(t, 100)
	s2 := newLiveServer(t, config.Config{Port: 5001})
	subscribe(t, s2, other.URL)
	s2.Deliver(t.Context(), "display.changed", time.Now(), nil)
	time.Sleep(100 * time.Millisecond)
	if hits := other.hits.Load(); hits != 2 {
		t.Errorf("display.changed tried %d times, want 2 (one POST and its immediate retry)", hits)
	}
}
