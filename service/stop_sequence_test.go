package service

// The first half of an SCM stop (announceStop): the power.stopping push
// goes out while the service is still whole, and the SCM is told how long
// that may take.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/stapi"

	"golang.org/x/sys/windows/svc"
)

// keepStateFile puts state.json back as it was: announceStop marks the
// shutdown clean in it.
func keepStateFile(t *testing.T) {
	t.Helper()
	path := statePath()
	orig, err := os.ReadFile(path)
	t.Cleanup(func() {
		stateMu.Lock()
		defer stateMu.Unlock()
		if err != nil {
			os.Remove(path)
			return
		}
		os.WriteFile(path, orig, 0o644)
	})
}

func TestAnnounceStopPushesBeforeTheStopChannelCloses(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	keepStateFile(t)
	startNotifier(nil) // a bus with the push tap, no notification sink
	t.Cleanup(stopNotifier)

	s := &shutdownService{stop: make(chan struct{})}
	// 1: the push arrived with s.stop still open; 2: already closed.
	var seen atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-s.stop:
			seen.CompareAndSwap(0, 2)
		default:
			seen.CompareAndSwap(0, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hub.Close)
	if w := stDo(t, "POST", "/st/v1/subscribe", "127.0.0.1", getConfig().Secret, subscribeBody(hub.URL+"/pc/evt", 600)); w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d (%s)", w.Code, w.Body.String())
	}

	changes := make(chan svc.Status, 4)
	s.announceStop(false, changes)

	switch seen.Load() {
	case 0:
		t.Fatal("the power.stopping push never reached the hub")
	case 2:
		t.Error("the power.stopping push went out after s.stop closed")
	}
	select {
	case <-s.stop:
	default:
		t.Error("announceStop left s.stop open")
	}

	close(changes)
	var got []svc.Status
	for c := range changes {
		got = append(got, c)
	}
	if len(got) != 2 {
		t.Fatalf("reported %d statuses, want 2: %+v", len(got), got)
	}
	first, second := got[0], got[1]
	if first.State != svc.StopPending || first.CheckPoint != 1 {
		t.Errorf("first status = %+v, want StopPending checkpoint 1", first)
	}
	if hint := time.Duration(first.WaitHint) * time.Millisecond; hint < stapi.PushAppStopDeadline {
		t.Errorf("stop wait hint %s is shorter than the app_stop push deadline %s", hint, stapi.PushAppStopDeadline)
	}
	if second.State != svc.StopPending || second.CheckPoint != 2 {
		t.Errorf("second status = %+v, want StopPending checkpoint 2", second)
	}
}

func TestStopWaitHintCoversThePush(t *testing.T) {
	if got, floor := stopWaitHint(false), stapi.StoppingDeadline("app_stop"); got <= floor {
		t.Errorf("plain stop hint %s, want more than the app_stop deadline %s", got, floor)
	}
	if got, floor := stopWaitHint(true), localShutdownTimeout+stapi.StoppingDeadline("shutdown"); got <= floor {
		t.Errorf("system shutdown hint %s, want more than the reason lookup and the push (%s)", got, floor)
	}
}
