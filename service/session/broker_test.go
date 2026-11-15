package session

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// testBroker is a broker with short timings and a clock the test moves
// with advance.
func testBroker(t *testing.T) (b *Broker, advance func(time.Duration)) {
	t.Helper()
	b = NewBroker()
	b.PollWait = 2 * time.Second
	b.Linger = 5 * time.Second
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	b.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	return b, func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
}

// tray polls session in a loop, as the tray app does, and answers each
// job with reply(job). It stops when ctx ends; the returned channel is
// closed once the last poll has returned.
func tray(ctx context.Context, b *Broker, session uint32, reply func(Job) string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			job, ok := b.Poll(ctx, session, 4321)
			if !ok {
				continue
			}
			_ = b.Reply(session, job.ID, []byte(reply(job)))
		}
	}()
	return done
}

// waitConnected waits until session's tray counts as connected.
func waitConnected(t *testing.T, b *Broker, session uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !b.Connected(session) {
		if time.Now().After(deadline) {
			t.Fatalf("the tray of session %d never connected", session)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBrokerNoTrayFailsFast(t *testing.T) {
	b, _ := testBroker(t)
	start := time.Now()
	_, err := b.Do(context.Background(), 1, []string{"audio", "get"})
	if !errors.Is(err, ErrTrayUnavailable) {
		t.Fatalf("err = %v, want ErrTrayUnavailable", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("took %v; it should not wait for a tray that is not there", d)
	}
}

func TestBrokerRoutesJobAndReply(t *testing.T) {
	b, _ := testBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	var got []string
	done := tray(ctx, b, 1, func(j Job) string {
		got = j.Args
		return `{"ok":true,"audio":{"volume":30,"muted":true,"device":"스피커"}}` + "\n"
	})
	defer func() { cancel(); <-done }()
	waitConnected(t, b, 1)

	line, err := b.Do(context.Background(), 1, []string{"audio", "mute", "on"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"audio", "mute", "on"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tray got %q, want %q", got, want)
	}
	res, err := ParseOutput(line)
	if err != nil || !res.OK || res.Audio == nil || !res.Audio.Muted {
		t.Errorf("reply %q: %+v %v", line, res, err)
	}
}

// A job goes to the tray of its own session only: the other user's tray
// (fast user switch) or an RDP session's never sees it, and a reply from
// another session does not count.
func TestBrokerOtherSessionNeverSeesJob(t *testing.T) {
	b, _ := testBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan Job, 1)
	go func() {
		if j, ok := b.Poll(ctx, 2, 999); ok {
			seen <- j
		}
	}()
	waitConnected(t, b, 2)

	if _, err := b.Do(context.Background(), 1, []string{"media", "next"}); !errors.Is(err, ErrTrayUnavailable) {
		t.Errorf("session 1 without a tray: %v, want ErrTrayUnavailable", err)
	}
	cancel()
	select {
	case j := <-seen:
		t.Errorf("the tray of session 2 got session 1's job %+v", j)
	case <-time.After(50 * time.Millisecond):
	}

	// A reply posted from another session than the job's is refused.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	jobs := make(chan Job, 1)
	go func() {
		if j, ok := b.Poll(ctx2, 1, 1000); ok {
			jobs <- j
		}
	}()
	waitConnected(t, b, 1)
	result := make(chan error, 1)
	go func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer dcancel()
		_, err := b.Do(dctx, 1, []string{"media", "next"})
		result <- err
	}()
	j := <-jobs
	if err := b.Reply(2, j.ID, []byte(`{"ok":true}`)); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("reply from session 2: %v, want ErrUnknownJob", err)
	}
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do after a foreign reply: %v, want the timeout", err)
	}
	if err := b.Reply(1, j.ID, []byte(`{"ok":true}`)); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("late reply: %v, want ErrUnknownJob", err)
	}
}

func TestBrokerTimeoutAndStaleJobSkipped(t *testing.T) {
	b, _ := testBroker(t)
	// The tray polls once, then is busy: the job waits in the queue.
	ctx, cancel := context.WithCancel(context.Background())
	polled := make(chan struct{})
	go func() {
		b.Poll(ctx, 1, 1)
		close(polled)
	}()
	waitConnected(t, b, 1)
	cancel()
	<-polled // lingers now, no poll waiting

	dctx, dcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer dcancel()
	if _, err := b.Do(dctx, 1, []string{"audio", "get"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}

	// The next poll does not hand out the job its caller gave up on.
	pctx, pcancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer pcancel()
	if j, ok := b.Poll(pctx, 1, 1); ok {
		t.Errorf("a timed-out job was handed out: %+v", j)
	}
}

func TestBrokerTrayGoneAndBack(t *testing.T) {
	b, advance := testBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := tray(ctx, b, 1, func(Job) string { return `{"ok":true}` })
	waitConnected(t, b, 1)
	cancel()
	<-done

	// Within Linger the tray still counts (it is between two polls).
	if !b.Connected(1) {
		t.Error("not connected right after the last poll")
	}
	advance(b.Linger + time.Second)
	if b.Connected(1) {
		t.Error("still connected after Linger without a poll")
	}
	if _, err := b.Do(context.Background(), 1, []string{"audio", "get"}); !errors.Is(err, ErrTrayUnavailable) {
		t.Errorf("after the tray quit: %v, want ErrTrayUnavailable", err)
	}

	// The tray starts again (a new process): jobs reach it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := tray(ctx2, b, 1, func(Job) string { return `{"ok":true,"screen":"off"}` })
	defer func() { cancel2(); <-done2 }()
	waitConnected(t, b, 1)
	line, err := b.Do(context.Background(), 1, []string{"screen", "off"})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := ParseOutput(line); res.ReplyString("screen") != "off" {
		t.Errorf("reply %q", line)
	}
}

// A quitting tray (Forget, #167) leaves no Linger, even when the server
// sees its last poll end only after the quit notice.
func TestBrokerForgetWithPollOpen(t *testing.T) {
	b, _ := testBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	polled := make(chan struct{})
	go func() {
		b.Poll(ctx, 1, 1)
		close(polled)
	}()
	waitConnected(t, b, 1)
	b.Forget(1)
	if !b.Connected(1) {
		t.Error("the open poll stopped counting before it ended")
	}
	cancel()
	<-polled
	if b.Connected(1) {
		t.Error("connected after the forgotten tray's poll ended")
	}
	// A new poll (the user started the tray again) counts as usual.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Poll(ctx2, 1, 2); close(done) }()
	waitConnected(t, b, 1)
	cancel2()
	<-done
	if !b.Connected(1) {
		t.Error("a later tray's poll left no Linger")
	}
}
