package session

// The job broker between the service and the tray apps (#164). The
// service runs in session 0; what must happen in the user's session
// (volume, media keys, toasts, presets, the screen commands) is done by
// the tray app already running there. It long-polls the WebUI API for a
// job (service/webui/tray.go), runs it with package useraction in its own
// process (package trayexec) and posts the reply line back. Until #164 the
// service started its own exe there for every command
// (CreateProcessAsUser), which Smart App Control re-judged each time and
// sometimes blocked (#163).
//
// Jobs are keyed by Windows session: a tray app polls for the session its
// process runs in — the WebUI reads that from the connection's owning
// process, never from the request — and a job goes only to the session the
// command targets. A tray app in any other session (the other user after a
// fast user switch, an RDP session next to the console) never sees it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrTrayUnavailable means no tray app polls for jobs in the target
// session: it was quit, has not started yet, or cannot reach the service.
// Callers answer 409 tray_unavailable.
var ErrTrayUnavailable = errors.New("tray_unavailable")

// ErrUnknownJob is a reply for a job that is not waiting (any more): it
// timed out, was answered already, or belongs to another session.
var ErrUnknownJob = errors.New("unknown job")

const (
	// DefaultPollWait is how long one poll waits for a job before it
	// answers "nothing"; the tray app polls again at once.
	DefaultPollWait = 25 * time.Second
	// DefaultLinger is how long after its last poll ended a tray app still
	// counts as connected: the gap until its next poll, and a retry after
	// a failed one.
	DefaultLinger = 10 * time.Second
	// queueLen bounds the jobs waiting for one session's tray app.
	queueLen = 16
)

// Job is one action for a tray app: the useraction argument vector, as it
// follows "user-action" on the command line. Its wire form is
// trayexec.Job.
type Job struct {
	ID   string
	Args []string
}

// Broker hands jobs to the tray apps and their replies back. The zero
// value is not usable; NewBroker makes one.
type Broker struct {
	// PollWait and Linger are DefaultPollWait and DefaultLinger; the tests
	// shorten them.
	PollWait time.Duration
	Linger   time.Duration
	// Now is the clock of the Linger window.
	Now func() time.Time

	mu    sync.Mutex
	trays map[uint32]*trayConn
	jobs  map[string]*pendingJob
}

// trayConn is what the broker knows about one session's tray app.
type trayConn struct {
	queue chan *pendingJob
	// polling is the number of polls waiting right now; lastPoll is when
	// the last one ended.
	polling  int
	lastPoll time.Time
	// pid is the process that polled last, for the log.
	pid uint32
}

// pendingJob is a job waiting for its reply.
type pendingJob struct {
	Job
	session uint32
	reply   chan []byte   // buffered 1: the first reply wins
	gone    chan struct{} // closed when the caller stopped waiting
}

// NewBroker returns a broker with the default timings.
func NewBroker() *Broker {
	return &Broker{
		PollWait: DefaultPollWait,
		Linger:   DefaultLinger,
		Now:      time.Now,
		trays:    map[uint32]*trayConn{},
		jobs:     map[string]*pendingJob{},
	}
}

// connected reports whether t polls now or did within Linger. Call with
// mu held.
func (b *Broker) connected(t *trayConn) bool {
	return t != nil && (t.polling > 0 || b.Now().Sub(t.lastPoll) < b.Linger)
}

// Connected reports whether a tray app polls for session's jobs.
func (b *Broker) Connected(session uint32) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected(b.trays[session])
}

// Do hands args to session's tray app and waits for its reply line. It
// fails at once with ErrTrayUnavailable when no tray app polls there, and
// returns ctx's error when ctx ends first (the caller's timeout).
func (b *Broker) Do(ctx context.Context, session uint32, args []string) ([]byte, error) {
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	j := &pendingJob{
		Job:     Job{ID: id, Args: append([]string(nil), args...)},
		session: session,
		reply:   make(chan []byte, 1),
		gone:    make(chan struct{}),
	}

	b.mu.Lock()
	t := b.trays[session]
	if !b.connected(t) {
		b.mu.Unlock()
		return nil, fmt.Errorf("%w: no tray app polls in session %d", ErrTrayUnavailable, session)
	}
	select {
	case t.queue <- j:
	default:
		b.mu.Unlock()
		return nil, fmt.Errorf("%w: %d jobs already wait for the tray app in session %d", ErrTrayUnavailable, queueLen, session)
	}
	b.jobs[id] = j
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.jobs, id)
		b.mu.Unlock()
		close(j.gone)
	}()
	select {
	case line := <-j.reply:
		return line, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Poll waits up to PollWait for a job for session's tray app; ok is false
// when none came or ctx ended (the tray app went away). pid is the polling
// process, for the log. The tray app counts as connected while it polls
// and for Linger after.
func (b *Broker) Poll(ctx context.Context, session, pid uint32) (job Job, ok bool) {
	b.mu.Lock()
	t := b.trays[session]
	if t == nil {
		t = &trayConn{queue: make(chan *pendingJob, queueLen)}
		b.trays[session] = t
	}
	t.pid = pid
	t.polling++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		t.polling--
		t.lastPoll = b.Now()
		b.mu.Unlock()
	}()

	timer := time.NewTimer(b.PollWait)
	defer timer.Stop()
	for {
		select {
		case j := <-t.queue:
			select {
			case <-j.gone:
				continue // the caller gave up while it waited
			default:
			}
			return j.Job, true
		case <-ctx.Done():
			return Job{}, false
		case <-timer.C:
			return Job{}, false
		}
	}
}

// Reply delivers the reply line of job id, posted from session.
// ErrUnknownJob when no such job waits, or it was sent to another session.
func (b *Broker) Reply(session uint32, id string, line []byte) error {
	b.mu.Lock()
	j := b.jobs[id]
	b.mu.Unlock()
	if j == nil || j.session != session {
		return ErrUnknownJob
	}
	select {
	case j.reply <- line:
	default: // answered already
	}
	return nil
}

// newJobID is 128 random bits: a reply must name a job it was given.
func newJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("job id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
