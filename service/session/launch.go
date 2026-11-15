package session

// Starting the tray app where it is missing (#167). Since #164 the
// user-session commands run in the tray app, so a session without one
// (the install script ended every tray during an update, the app crashed,
// the console belongs to another Windows account that never started it)
// answered tray_unavailable until somebody started the app by hand. The
// Launcher closes that gap: a command for a logged-in session whose tray
// does not poll starts "<exe> gui --minimized" there (WTS.StartIn, the
// grace toast's CreateProcessAsUser) and waits briefly for it to poll;
// so does the service start, once, for the sessions commands go to.
//
// Policy:
//
//   - One attempt at a time per session: the jobs that arrive while it runs
//     all wait for the same attempt (no thundering herd).
//   - An attempt that fails (the start is refused — Smart App Control — or
//     the tray does not poll within Wait) holds further attempts for
//     Backoff; commands in between fail at once with tray_unavailable
//     instead of each waiting Wait for a tray that will not come. A
//     successful attempt sets nothing: a tray that dies later is started
//     again by the next command.
//   - The user quitting the tray from its menu (POST /api/tray/quit) stops
//     every automatic start in that logon session until the tray polls
//     again (the user started it, or autostart at the next logon) or a new
//     logon takes the session (another logon ID). This memory is the
//     service process's: a service restart forgets it, and then only the
//     start-up launch can bring the tray back, which the user's autostart
//     setting gates.
//   - The start-up launch (LaunchMissing) also skips a user who turned
//     autostart off: they do not want the tray at logon, so a service
//     restart should not impose it. A command still starts it on demand —
//     the user asked for something only the tray can do.

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultLaunchWait bounds how long a command waits for a tray it
	// started: process start (Smart App Control may look the exe up on
	// first run), the single-instance check, the tray icon, the tray.json
	// read, a local login when a secret is set, and the first poll. A
	// warm start polls in well under 2s; 6s leaves room for a cold one
	// while the whole command (this plus the 3s job timeout) still ends
	// within ~9s. The SmartThings driver gives up after 5s, so the very
	// command that starts a tray may time out there — the attempt goes on
	// regardless of who waits, and the next command finds the tray up.
	DefaultLaunchWait = 6 * time.Second
	// DefaultLaunchBackoff is how long a failed attempt holds the next
	// one for the same session.
	DefaultLaunchBackoff = 5 * time.Minute
)

// LogonInfo identifies who is logged on to a session.
type LogonInfo struct {
	// ID is the logon session (the token's AuthenticationId): a new logon
	// in the same Windows session has another.
	ID string
	// SID is the user's SID, for their registry hive (the autostart value).
	SID string
}

// Launcher starts the tray app in sessions where none polls. Every
// function field is a dependency the service fills in and a test fakes.
type Launcher struct {
	// Broker is where trays poll.
	Broker *Broker
	// Start launches the tray app ("<exe> gui --minimized") in session.
	// nil disables launching: Ensure returns at once and the broker
	// answers as before.
	Start func(session uint32) error
	// Logon reads the session's logon (QueryLogon). An error is taken as
	// nobody to start a tray for.
	Logon func(session uint32) (LogonInfo, error)
	// Autostart reports whether the user (by SID) keeps the app's login
	// autostart on. It gates LaunchMissing only. nil: on.
	Autostart func(sid string) bool
	// Wait and Backoff are DefaultLaunchWait and DefaultLaunchBackoff.
	Wait    time.Duration
	Backoff time.Duration
	// Now is the clock of Backoff.
	Now func() time.Time
	// Logf writes the service log; every change of state is one line.
	Logf func(format string, args ...any)

	mu       sync.Mutex
	sessions map[uint32]*launchState
}

// launchState is what the launcher remembers about one session.
type launchState struct {
	// logon is the logon this state belongs to; another one resets it.
	logon string
	// inflight is closed when the running attempt ends; nil when none runs.
	inflight chan struct{}
	// failedAt is when the last attempt failed; zero after a success.
	failedAt time.Time
	// heldLogged: the "held by the backoff" line was written for this
	// failure.
	heldLogged bool
	// quit: the user quit the tray in this logon, at broker poll count
	// quitPolls; a later poll lifts it.
	quit      bool
	quitPolls uint64
}

// NewLauncher returns a launcher over b with the default timings.
func NewLauncher(b *Broker, start func(uint32) error, logon func(uint32) (LogonInfo, error)) *Launcher {
	return &Launcher{
		Broker:  b,
		Start:   start,
		Logon:   logon,
		Wait:    DefaultLaunchWait,
		Backoff: DefaultLaunchBackoff,
		Now:     time.Now,
	}
}

func (l *Launcher) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// state returns session's state for logon, a fresh one when the session
// has another logon now. Call with mu held.
func (l *Launcher) state(session uint32, logon string) *launchState {
	if l.sessions == nil {
		l.sessions = map[uint32]*launchState{}
	}
	st := l.sessions[session]
	if st == nil || st.logon != logon {
		// A running attempt of the old logon keeps its own state and ends
		// on it; the new logon starts clean.
		st = &launchState{logon: logon}
		l.sessions[session] = st
	}
	return st
}

// quitHolds reports whether st's quit still stands, lifting it once the
// session's tray has polled since. Call with mu held.
func (l *Launcher) quitHolds(session uint32, st *launchState) bool {
	if !st.quit {
		return false
	}
	if l.Broker.PollCount(session) > st.quitPolls {
		st.quit = false
		l.logf("Tray app in session %d is running again; automatic starts resume", session)
		return false
	}
	return true
}

// Ensure makes sure session has a polling tray before a job is handed to
// it: at once when one polls, else it starts one (or joins the attempt
// already running) and waits for it up to Wait. It returns nil when a tray
// polls, ErrTrayUnavailable (wrapped, with the reason) when none came or
// none may be started now, and ctx's error when the caller gave up first.
// The attempt itself does not depend on ctx.
func (l *Launcher) Ensure(ctx context.Context, session uint32) error {
	if l == nil || l.Start == nil || !launchAllowed(ctx) || l.Broker.Connected(session) {
		return nil
	}
	logon, err := l.Logon(session)
	if err != nil {
		return fmt.Errorf("%w: session %d: %v", ErrTrayUnavailable, session, err)
	}
	done, err := l.begin(session, logon, "a command")
	if err != nil {
		return err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !l.Broker.Connected(session) {
		return fmt.Errorf("%w: the tray app started in session %d did not connect within %v", ErrTrayUnavailable, session, l.Wait)
	}
	return nil
}

// begin starts an attempt for session, or returns the running one's done
// channel. It refuses (ErrTrayUnavailable) while the user's quit or a
// failed attempt's backoff holds.
func (l *Launcher) begin(session uint32, logon LogonInfo, reason string) (<-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state(session, logon.ID)
	if st.inflight != nil {
		return st.inflight, nil
	}
	if l.quitHolds(session, st) {
		return nil, fmt.Errorf("%w: the user quit the tray app in session %d", ErrTrayUnavailable, session)
	}
	if !st.failedAt.IsZero() {
		if until := st.failedAt.Add(l.Backoff); l.Now().Before(until) {
			if !st.heldLogged {
				st.heldLogged = true
				l.logf("Tray app in session %d: not starting it again before %s (the last start failed)", session, until.Format("15:04:05"))
			}
			return nil, fmt.Errorf("%w: the last start of the tray app in session %d failed; next try after %s", ErrTrayUnavailable, session, until.Format("15:04:05"))
		}
	}
	done := make(chan struct{})
	st.inflight = done
	go l.attempt(session, st, done, reason)
	return done, nil
}

// attempt starts the tray and waits up to Wait for it to poll, then
// records the outcome and closes done.
func (l *Launcher) attempt(session uint32, st *launchState, done chan struct{}, reason string) {
	l.logf("Tray app is not running in session %d; starting it (%s)", session, reason)
	ok := false
	var why string
	if err := l.Start(session); err != nil {
		why = err.Error()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), l.Wait)
		ok = l.Broker.WaitConnected(ctx, session)
		cancel()
		why = fmt.Sprintf("it did not connect within %v", l.Wait)
	}

	l.mu.Lock()
	st.inflight = nil
	if ok {
		st.failedAt = time.Time{}
		st.heldLogged = false
	} else {
		st.failedAt = l.Now()
		st.heldLogged = false
	}
	l.mu.Unlock()
	if ok {
		l.logf("Tray app in session %d connected", session)
	} else {
		l.logf("Tray app in session %d could not be started: %s; next try after %v at the earliest", session, why, l.Backoff)
	}
	close(done)
}

// UserQuit records that the user quit the tray app of session from its
// menu: no automatic start there until it polls again or another logon
// takes the session. The tray stops counting as connected now rather than
// after the broker's Linger.
func (l *Launcher) UserQuit(session uint32) {
	if l == nil {
		return
	}
	l.Broker.Forget(session)
	if l.Logon == nil {
		return
	}
	logon, err := l.Logon(session)
	if err != nil {
		return
	}
	l.mu.Lock()
	st := l.state(session, logon.ID)
	already := st.quit
	st.quit = true
	st.quitPolls = l.Broker.PollCount(session)
	l.mu.Unlock()
	if !already {
		l.logf("Tray app in session %d was quit by the user; not starting it automatically until it runs again or the next logon", session)
	}
}

// LaunchMissing is the start-up launch: for each of sessions (the ones
// commands go to, already past the grace period that lets autostart run
// first) with a logged-on user, no polling tray, no quit standing and
// autostart on, it starts an attempt. It does not wait for the attempts;
// they log their outcome.
func (l *Launcher) LaunchMissing(sessions []uint32) {
	if l == nil || l.Start == nil {
		return
	}
	for _, id := range sessions {
		if l.Broker.Connected(id) {
			continue
		}
		logon, err := l.Logon(id)
		if err != nil {
			continue
		}
		if l.Autostart != nil && !l.Autostart(logon.SID) {
			l.logf("Tray app is not running in session %d; its user turned autostart off, so it is not started now (a command still starts it)", id)
			continue
		}
		_, _ = l.begin(id, logon, "service start")
	}
}

// noLaunchKey marks a context whose runs must not start a tray app.
type noLaunchKey struct{}

// WithoutLaunch marks ctx for a background reading (the status fill): a
// run under it finds the tray app or fails, but never starts one. Only a
// command someone sent starts a tray; a status poll every few seconds
// must not bring back a tray the user turned off or keep retrying one
// that cannot start.
func WithoutLaunch(ctx context.Context) context.Context {
	return context.WithValue(ctx, noLaunchKey{}, true)
}

func launchAllowed(ctx context.Context) bool {
	return ctx.Value(noLaunchKey{}) == nil
}
