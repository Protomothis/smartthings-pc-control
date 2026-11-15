package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStarts stands in for WTS.StartIn: it records the sessions started
// and runs onStart (nil: nothing comes up).
type fakeStarts struct {
	mu      sync.Mutex
	calls   []uint32
	onStart func(session uint32) error
}

func (f *fakeStarts) start(session uint32) error {
	f.mu.Lock()
	f.calls = append(f.calls, session)
	on := f.onStart
	f.mu.Unlock()
	if on != nil {
		return on(session)
	}
	return nil
}

func (f *fakeStarts) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeStarts) sessions() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.calls...)
}

// testLauncher is a launcher over a test broker with short timings; the
// logons are "logon-<session>" unless logons says otherwise, and every
// user keeps autostart on unless autostartOff names their SID.
type launcherFixture struct {
	b       *Broker
	l       *Launcher
	starts  *fakeStarts
	advance func(time.Duration)
	mu      sync.Mutex
	logons  map[uint32]string
	noUser  map[uint32]bool
	autoOff map[string]bool
	logs    []string
	ctx     context.Context
}

func newLauncherFixture(t *testing.T) *launcherFixture {
	t.Helper()
	b, advance := testBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &launcherFixture{b: b, starts: &fakeStarts{}, advance: advance,
		logons: map[uint32]string{}, noUser: map[uint32]bool{}, autoOff: map[string]bool{}, ctx: ctx}
	f.l = NewLauncher(b, f.starts.start, func(s uint32) (LogonInfo, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.noUser[s] {
			return LogonInfo{}, fmt.Errorf("%w: session %d", ErrNoUserSession, s)
		}
		id := f.logons[s]
		if id == "" {
			id = fmt.Sprintf("logon-%d", s)
		}
		return LogonInfo{ID: id, SID: fmt.Sprintf("S-1-5-21-%d", s)}, nil
	})
	f.l.Autostart = func(sid string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return !f.autoOff[sid]
	}
	f.l.Wait = 300 * time.Millisecond
	f.l.Backoff = 5 * time.Minute
	f.l.Now = b.Now
	f.l.Logf = func(format string, args ...any) {
		f.mu.Lock()
		f.logs = append(f.logs, fmt.Sprintf(format, args...))
		f.mu.Unlock()
	}
	return f
}

// trayComesUp makes each start bring up a tray in that session after
// delay, which answers every job with reply. Each tray runs until the
// test ends; the returned stop ends them early and waits for them.
func (f *launcherFixture) trayComesUp(t *testing.T, delay time.Duration, reply string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	var wg sync.WaitGroup
	f.starts.mu.Lock()
	f.starts.onStart = func(s uint32) error {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			<-tray(ctx, f.b, s, func(Job) string { return reply })
		}()
		return nil
	}
	f.starts.mu.Unlock()
	stop = func() { cancel(); wg.Wait() }
	t.Cleanup(stop)
	return stop
}

func TestLauncherStartsMissingTrayThenJobRuns(t *testing.T) {
	f := newLauncherFixture(t)
	f.trayComesUp(t, 50*time.Millisecond, `{"ok":true,"media":"next"}`)

	if err := f.l.Ensure(context.Background(), 1); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := f.starts.sessions(); !reflect.DeepEqual(got, []uint32{1}) {
		t.Fatalf("started %v, want [1]", got)
	}
	line, err := f.b.Do(context.Background(), 1, []string{"media", "next"})
	if err != nil {
		t.Fatalf("Do after the launch: %v", err)
	}
	if res, _ := ParseOutput(line); res.ReplyString("media") != "next" {
		t.Errorf("reply %q", line)
	}
	// A tray that polls needs no start.
	if err := f.l.Ensure(context.Background(), 1); err != nil || f.starts.count() != 1 {
		t.Errorf("Ensure with the tray up: %v, %d starts", err, f.starts.count())
	}
}

// Through the runner, as the service wires it: Ready starts the tray,
// and its wait does not eat the job's Timeout.
func TestRunnerReadyStartsTray(t *testing.T) {
	f := newLauncherFixture(t)
	f.trayComesUp(t, 150*time.Millisecond, `{"ok":true}`)
	rn := Runner{
		Ready: func(ctx context.Context, _ Target) error { return f.l.Ensure(ctx, 1) },
		Exec: func(ctx context.Context, _ Target, args []string) ([]byte, error) {
			return f.b.Do(ctx, 1, args)
		},
		Timeout: 100 * time.Millisecond, // shorter than the tray's start
	}
	if _, err := rn.Run(context.Background(), ActiveUser, "media", "next"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestLauncherTimeoutIsTrayUnavailable(t *testing.T) {
	f := newLauncherFixture(t) // the start "works", but no tray ever polls
	start := time.Now()
	err := f.l.Ensure(context.Background(), 1)
	if !errors.Is(err, ErrTrayUnavailable) {
		t.Fatalf("err = %v, want ErrTrayUnavailable", err)
	}
	if d := time.Since(start); d < f.l.Wait || d > f.l.Wait+time.Second {
		t.Errorf("waited %v, want about Wait (%v)", d, f.l.Wait)
	}

	// The runner keeps it whole.
	rn := Runner{
		Ready:   func(ctx context.Context, _ Target) error { return f.l.Ensure(ctx, 2) },
		Exec:    func(context.Context, Target, []string) ([]byte, error) { t.Fatal("Exec ran"); return nil, nil },
		Timeout: time.Second,
	}
	if _, err := rn.Run(context.Background(), ActiveUser, "audio", "get"); !errors.Is(err, ErrTrayUnavailable) {
		t.Errorf("Run: %v, want ErrTrayUnavailable", err)
	}
}

func TestLauncherStartErrorIsTrayUnavailable(t *testing.T) {
	f := newLauncherFixture(t)
	f.starts.onStart = func(uint32) error { return errors.New("An Application Control policy has blocked this file.") }
	start := time.Now()
	if err := f.l.Ensure(context.Background(), 1); !errors.Is(err, ErrTrayUnavailable) {
		t.Fatalf("err = %v, want ErrTrayUnavailable", err)
	}
	if d := time.Since(start); d > f.l.Wait/2 {
		t.Errorf("a refused start still waited %v", d)
	}
}

func TestLauncherConcurrentJobsShareOneStart(t *testing.T) {
	f := newLauncherFixture(t)
	f.trayComesUp(t, 100*time.Millisecond, `{"ok":true}`)
	const n = 12
	errs := make(chan error, n)
	for range n {
		go func() { errs <- f.l.Ensure(context.Background(), 1) }()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Errorf("Ensure: %v", err)
		}
	}
	if c := f.starts.count(); c != 1 {
		t.Errorf("%d starts for %d concurrent jobs, want 1", c, n)
	}
}

func TestLauncherBackoffAfterFailure(t *testing.T) {
	f := newLauncherFixture(t)
	if err := f.l.Ensure(context.Background(), 1); !errors.Is(err, ErrTrayUnavailable) {
		t.Fatalf("first: %v", err)
	}
	// Within Backoff: no new start, and no wait either.
	for range 3 {
		start := time.Now()
		if err := f.l.Ensure(context.Background(), 1); !errors.Is(err, ErrTrayUnavailable) {
			t.Fatalf("held: %v", err)
		}
		if d := time.Since(start); d > f.l.Wait/2 {
			t.Errorf("a held start waited %v", d)
		}
		f.advance(time.Minute)
	}
	if c := f.starts.count(); c != 1 {
		t.Fatalf("%d starts within the backoff, want 1", c)
	}
	// The hold is per session.
	_ = f.l.Ensure(context.Background(), 2)
	if c := f.starts.count(); c != 2 {
		t.Fatalf("session 2 was held by session 1's failure (%d starts)", c)
	}
	// After Backoff the next command tries again, and this time it works.
	f.advance(f.l.Backoff)
	f.trayComesUp(t, 10*time.Millisecond, `{"ok":true}`)
	if err := f.l.Ensure(context.Background(), 1); err != nil {
		t.Fatalf("after the backoff: %v", err)
	}
	if c := f.starts.count(); c != 3 {
		t.Errorf("%d starts, want 3", c)
	}
	// One "held" line for the whole failure, not one per command.
	held := 0
	f.mu.Lock()
	logs := append([]string(nil), f.logs...)
	f.mu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, "not starting it again") {
			held++
		}
	}
	if held != 1 {
		t.Errorf("%d held lines in %q, want 1", held, logs)
	}
}

func TestLauncherRespectsUserQuit(t *testing.T) {
	f := newLauncherFixture(t)
	stop := f.trayComesUp(t, 0, `{"ok":true}`)
	if err := f.l.Ensure(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	// The user quits: the tray's polls end, then it says so.
	stop()
	f.l.UserQuit(1)
	if f.b.Connected(1) {
		t.Fatal("still connected after the quit notice (Linger not ended)")
	}
	if err := f.l.Ensure(context.Background(), 1); !errors.Is(err, ErrTrayUnavailable) {
		t.Fatalf("after the quit: %v, want ErrTrayUnavailable", err)
	}
	f.l.LaunchMissing([]uint32{1})
	if c := f.starts.count(); c != 1 {
		t.Fatalf("%d starts after the quit, want none beyond the first", c)
	}

	// The user starts it again (a poll after the quit), then it goes away
	// unannounced (a crash): automatic starts are back.
	ctx, cancel := context.WithCancel(context.Background())
	done := tray(ctx, f.b, 1, func(Job) string { return `{"ok":true}` })
	waitConnected(t, f.b, 1)
	cancel()
	<-done
	f.advance(f.b.Linger + time.Second)
	f.trayComesUp(t, 0, `{"ok":true}`)
	if err := f.l.Ensure(context.Background(), 1); err != nil {
		t.Fatalf("after the user started it again and it crashed: %v", err)
	}
	if c := f.starts.count(); c != 2 {
		t.Errorf("%d starts, want 2", c)
	}
}

func TestLauncherQuitEndsWithTheLogon(t *testing.T) {
	f := newLauncherFixture(t)
	f.l.UserQuit(1)
	if err := f.l.Ensure(context.Background(), 1); !errors.Is(err, ErrTrayUnavailable) || f.starts.count() != 0 {
		t.Fatalf("after the quit: %v, %d starts", err, f.starts.count())
	}
	// Log off, log on again: same Windows session, another logon.
	f.mu.Lock()
	f.logons[1] = "logon-1-again"
	f.mu.Unlock()
	f.trayComesUp(t, 0, `{"ok":true}`)
	if err := f.l.Ensure(context.Background(), 1); err != nil {
		t.Fatalf("new logon: %v", err)
	}
	if c := f.starts.count(); c != 1 {
		t.Errorf("%d starts, want 1", c)
	}
}

// A background reading (WithoutLaunch) never starts a tray.
func TestLauncherWithoutLaunch(t *testing.T) {
	f := newLauncherFixture(t)
	if err := f.l.Ensure(WithoutLaunch(context.Background()), 1); err != nil {
		t.Fatalf("Ensure: %v (the broker answers for itself)", err)
	}
	if c := f.starts.count(); c != 0 {
		t.Errorf("%d starts for a background reading", c)
	}
}

// The start-up launch: only sessions with a user, no tray, no standing
// quit and autostart on, and it does not wait for them.
func TestLauncherLaunchMissing(t *testing.T) {
	f := newLauncherFixture(t)
	// 1: its tray polls.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tray(ctx, f.b, 1, func(Job) string { return `{"ok":true}` })
	waitConnected(t, f.b, 1)
	// 2: autostart off. 3: quit. 4: nobody logged on. 5: due.
	f.mu.Lock()
	f.autoOff["S-1-5-21-2"] = true
	f.noUser[4] = true
	f.mu.Unlock()
	f.l.UserQuit(3)

	start := time.Now()
	f.l.LaunchMissing([]uint32{1, 2, 3, 4, 5})
	if d := time.Since(start); d > f.l.Wait/2 {
		t.Errorf("LaunchMissing waited %v for its attempts", d)
	}
	deadline := time.Now().Add(time.Second)
	for f.starts.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := f.starts.sessions(); !reflect.DeepEqual(got, []uint32{5}) {
		t.Errorf("started %v, want [5]", got)
	}
	// A command right after joins that attempt instead of starting another.
	_ = f.l.Ensure(context.Background(), 5)
	if c := f.starts.count(); c != 1 {
		t.Errorf("%d starts, want 1", c)
	}
	// Autostart off only gates the start-up launch: a command still starts it.
	f.trayComesUp(t, 0, `{"ok":true}`)
	if err := f.l.Ensure(context.Background(), 2); err != nil {
		t.Errorf("command for the user without autostart: %v", err)
	}
}
