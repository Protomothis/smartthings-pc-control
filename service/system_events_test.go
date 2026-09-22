package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- system.updated (state.json version comparison) ----------------------

func TestRecordVersionEmitsUpdatedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	events := captureNotifications(t)

	// First ever run: nothing to compare with, but the version is stored.
	if recordVersion(path, "v0.3.4") {
		t.Errorf("first run emitted system.updated")
	}
	expectNoNotification(t, events)
	if st := loadState(path); st.LastVersion != "v0.3.4" {
		t.Fatalf("last_version = %q, want v0.3.4", st.LastVersion)
	}

	// Same version again: silent.
	if recordVersion(path, "v0.3.4") {
		t.Errorf("same version emitted system.updated")
	}
	expectNoNotification(t, events)

	// New binary: exactly one system.updated with both versions.
	if !recordVersion(path, "v0.3.5") {
		t.Errorf("changed version did not report an emit")
	}
	ev := expectNotification(t, events, "system.updated")
	if ev.Fields["version"] != "v0.3.5" || ev.Fields["previous"] != "v0.3.4" {
		t.Errorf("fields = %v", ev.Fields)
	}
	if st := loadState(path); st.LastVersion != "v0.3.5" {
		t.Errorf("last_version after update = %q", st.LastVersion)
	}

	// The next start of the same build is silent again.
	recordVersion(path, "v0.3.5")
	expectNoNotification(t, events)
}

func TestRecordVersionSkipsDevBuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	events := captureNotifications(t)

	for _, v := range []string{"dev", ""} {
		if recordVersion(path, v) {
			t.Errorf("recordVersion(%q) emitted", v)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("recordVersion(%q) wrote state.json", v)
		}
	}
	expectNoNotification(t, events)

	// A recorded release version must survive a dev run unchanged.
	if err := saveState(path, serviceState{LastVersion: "v0.3.4"}); err != nil {
		t.Fatal(err)
	}
	recordVersion(path, "dev")
	expectNoNotification(t, events)
	if st := loadState(path); st.LastVersion != "v0.3.4" {
		t.Errorf("dev run changed last_version to %q", st.LastVersion)
	}
}

func TestRecordVersionKeepsOtherStateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	captureNotifications(t)
	if err := saveState(path, serviceState{LastVersion: "v0.3.4", LastNotifiedTag: "v0.3.5"}); err != nil {
		t.Fatal(err)
	}
	recordVersion(path, "v0.3.5")
	if st := loadState(path); st.LastNotifiedTag != "v0.3.5" || st.LastVersion != "v0.3.5" {
		t.Errorf("state after update = %+v", st)
	}
}

func TestLoadStateTolerantOfGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{not json"), 0644)
	if st := loadState(path); st != (serviceState{}) {
		t.Errorf("garbage state parsed as %+v", st)
	}
	if st := loadState(filepath.Join(t.TempDir(), "missing.json")); st != (serviceState{}) {
		t.Errorf("missing state parsed as %+v", st)
	}
	if st := loadState(""); st != (serviceState{}) {
		t.Errorf("empty path parsed as %+v", st)
	}
	if err := saveState("", serviceState{LastVersion: "x"}); err != nil {
		t.Errorf("saveState(\"\") = %v", err)
	}
}

// --- system.update_available -------------------------------------------

// fakeReleaseServer serves a GitHub-shaped "latest release" whose tag the
// test can change, counting requests.
type fakeReleaseServer struct {
	*httptest.Server
	tag  atomic.Value
	hits atomic.Int32
}

func newFakeReleaseServer(t *testing.T, tag string) *fakeReleaseServer {
	t.Helper()
	s := &fakeReleaseServer{}
	s.tag.Store(tag)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		tag := s.tag.Load().(string)
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://x/releases/` + tag + `","assets":[]}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestUpdateCheckerNotifiesOncePerTag(t *testing.T) {
	srv := newFakeReleaseServer(t, "v0.4.0")
	path := filepath.Join(t.TempDir(), "state.json")
	events := captureNotifications(t)
	c := newUpdateChecker(path, "v0.3.4")
	c.url = srv.URL

	if !c.check(context.Background()) {
		t.Fatalf("first check with a newer tag did not emit")
	}
	ev := expectNotification(t, events, "system.update_available")
	want := map[string]string{"version": "v0.4.0", "current": "v0.3.4", "url": "https://x/releases/v0.4.0"}
	for k, v := range want {
		if ev.Fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, ev.Fields[k], v)
		}
	}
	if st := loadState(path); st.LastNotifiedTag != "v0.4.0" {
		t.Errorf("last_notified_tag = %q", st.LastNotifiedTag)
	}

	// Same tag on the next check: silent, even though it is still newer.
	if c.check(context.Background()) {
		t.Errorf("second check for the same tag emitted")
	}
	expectNoNotification(t, events)

	// A newer tag is announced again, once.
	srv.tag.Store("v0.4.1")
	if !c.check(context.Background()) {
		t.Errorf("newer tag did not emit")
	}
	if ev := expectNotification(t, events, "system.update_available"); ev.Fields["version"] != "v0.4.1" {
		t.Errorf("version = %q, want v0.4.1", ev.Fields["version"])
	}
	c.check(context.Background())
	expectNoNotification(t, events)

	// An older or equal tag never notifies and does not touch the state.
	for _, tag := range []string{"v0.3.4", "v0.3.0", "garbage"} {
		srv.tag.Store(tag)
		if c.check(context.Background()) {
			t.Errorf("tag %q emitted", tag)
		}
	}
	expectNoNotification(t, events)
	if st := loadState(path); st.LastNotifiedTag != "v0.4.1" {
		t.Errorf("last_notified_tag after older tags = %q", st.LastNotifiedTag)
	}
}

func TestUpdateCheckerSkipsDevBuildAndErrors(t *testing.T) {
	srv := newFakeReleaseServer(t, "v9.9.9")
	events := captureNotifications(t)
	for _, v := range []string{"dev", ""} {
		c := newUpdateChecker(filepath.Join(t.TempDir(), "state.json"), v)
		c.url = srv.URL
		if c.check(context.Background()) {
			t.Errorf("version %q emitted", v)
		}
		// run must return immediately for dev builds without scheduling.
		done := make(chan struct{})
		c.after = func(time.Duration) <-chan time.Time {
			t.Errorf("run(%q) scheduled a check", v)
			return nil
		}
		go func() { c.run(make(chan struct{})); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("run(%q) did not return", v)
		}
	}
	if srv.hits.Load() != 0 {
		t.Errorf("dev build contacted the release API %d times", srv.hits.Load())
	}

	// A failing API is logged and nothing is emitted or recorded.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer bad.Close()
	path := filepath.Join(t.TempDir(), "state.json")
	c := newUpdateChecker(path, "v0.3.4")
	c.url = bad.URL
	if c.check(context.Background()) {
		t.Errorf("HTTP 403 emitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("failed check wrote state.json")
	}
	expectNoNotification(t, events)
}

func TestUpdateCheckerRunSchedule(t *testing.T) {
	srv := newFakeReleaseServer(t, "v0.4.0")
	path := filepath.Join(t.TempDir(), "state.json")
	events := captureNotifications(t)
	c := newUpdateChecker(path, "v0.3.4")
	c.url = srv.URL

	// Fake clock: run asks for a wait, the test releases it.
	var mu sync.Mutex
	var waits []time.Duration
	ticks := make(chan chan time.Time, 4)
	c.after = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		ch := make(chan time.Time, 1)
		ticks <- ch
		return ch
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { c.run(stop); close(done) }()

	fire := func() {
		select {
		case ch := <-ticks:
			ch <- time.Now()
		case <-time.After(2 * time.Second):
			t.Fatalf("run did not schedule a wait")
		}
	}

	fire() // 10 min → first check
	expectNotification(t, events, "system.update_available")
	fire() // 24 h → same tag, silent
	expectNoNotification(t, events)
	srv.tag.Store("v0.5.0")
	fire() // 24 h → new tag
	if ev := expectNotification(t, events, "system.update_available"); ev.Fields["version"] != "v0.5.0" {
		t.Errorf("version = %q, want v0.5.0", ev.Fields["version"])
	}

	// The loop is now blocked on the 4th wait; stop must end it.
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatalf("run did not schedule the next wait")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("run did not return after stop")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 4 || waits[0] != updateCheckFirst || waits[1] != updateCheckEvery || waits[3] != updateCheckEvery {
		t.Errorf("waits = %v, want [10m 24h 24h 24h]", waits)
	}
	if srv.hits.Load() != 3 {
		t.Errorf("release API hit %d times, want 3", srv.hits.Load())
	}
}

// --- power.resumed --------------------------------------------------------

func TestPowerTrackerResume(t *testing.T) {
	events := captureNotifications(t)
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	p := &powerTracker{now: func() time.Time { return now }}

	// Resume without a recorded suspend: since is unknown.
	if !p.handle(pbtAPMResumeAutomatic) {
		t.Fatalf("resume did not emit")
	}
	if ev := expectNotification(t, events, "power.resumed"); ev.Fields["since"] != "-" {
		t.Errorf("since = %q, want -", ev.Fields["since"])
	}

	// Suspend, sleep 3h05m, resume. #87: power.stopping is on by default
	// now, so the suspend broadcast's own event is delivered too - it is
	// how the Edge driver's tile reaches "sleeping" (§6.2).
	if p.handle(pbtAPMSuspend) {
		t.Errorf("suspend emitted")
	}
	if ev := expectNotification(t, events, "power.stopping"); ev.Fields["reason"] != "suspend" {
		t.Errorf("suspend reason = %q, want suspend", ev.Fields["reason"])
	}
	now = now.Add(3*time.Hour + 5*time.Minute)
	p.handle(pbtAPMResumeAutomatic)
	if ev := expectNotification(t, events, "power.resumed"); ev.Fields["since"] != "3h 05m" {
		t.Errorf("since = %q, want 3h 05m", ev.Fields["since"])
	}

	// The suspend timestamp is consumed: a second resume is "-" again.
	p.handle(pbtAPMResumeAutomatic)
	if ev := expectNotification(t, events, "power.resumed"); ev.Fields["since"] != "-" {
		t.Errorf("since after consumed suspend = %q, want -", ev.Fields["since"])
	}

	// Other broadcasts (PBT_APMRESUMESUSPEND 0x7, PBT_POWERSETTINGCHANGE
	// 0x8013, PBT_APMPOWERSTATUSCHANGE 0xA) are ignored.
	for _, et := range []uint32{0x7, 0x8013, 0xA, 0} {
		if p.handle(et) {
			t.Errorf("event 0x%x emitted", et)
		}
	}
	expectNoNotification(t, events)
}

func TestFormatSpan(t *testing.T) {
	cases := map[time.Duration]string{
		-5 * time.Second:                "0 sec",
		0:                               "0 sec",
		45 * time.Second:                "45 sec",
		time.Minute:                     "1 min",
		12*time.Minute + 30*time.Second: "12 min",
		time.Hour:                       "1h 00m",
		3*time.Hour + 5*time.Minute:     "3h 05m",
		26 * time.Hour:                  "26h 00m",
	}
	for d, want := range cases {
		if got := formatSpan(d); got != want {
			t.Errorf("formatSpan(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestBootTimeIsInThePast(t *testing.T) {
	bt := bootTime()
	if bt.After(time.Now()) || time.Since(bt) < time.Second {
		t.Errorf("bootTime() = %v, not plausibly in the past", bt)
	}
}
