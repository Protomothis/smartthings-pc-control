// Package trayexec runs the service's user-session jobs in the tray app
// (#164). The service lives in session 0, where the default audio
// endpoint, media keys and toasts mean nothing; the tray app runs in the
// user's session anyway, so it takes the jobs (volume, mute, media keys,
// media info, toasts, presets, screen off/on) from the service and runs
// them in its own process with package useraction — the very parser,
// validation and handlers of the `user-action` subcommand, with the same
// one-line reply. Until #164 the service started that subcommand as a new
// process for every command, which Smart App Control re-judged each time
// and sometimes blocked (#163).
//
// The protocol is a long poll on the WebUI API, under the tray app's own
// session cookie (the GUI's cookie jar) and CSRF header:
//
//	POST /api/tray/poll    → 200 {"job":{"id":"…","args":[…]}}, or 204 after ~25s
//	POST /api/tray/result  {"id":"…","result":{"ok":…}} → 200, 404 unknown_job
//
// The service decides which session a poll is for from the process that
// opened the connection, not from anything in the request
// (service/webui/tray.go).
//
// This package knows nothing of the GUI toolkit: the GUI starts it with
// Start and stops it on quit.
package trayexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// The endpoints.
const (
	PathPoll   = "/api/tray/poll"
	PathResult = "/api/tray/result"
)

// Job is one action: the useraction argument vector, as it follows
// "user-action" on the command line.
type Job struct {
	ID   string   `json:"id"`
	Args []string `json:"args"`
}

// PollReply is the body of a 200 poll answer.
type PollReply struct {
	Job *Job `json:"job"`
}

// ResultBody is the body of a result post: the reply line as JSON.
type ResultBody struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
}

const (
	// pollTimeout bounds one poll; the service answers within ~25s.
	pollTimeout = 40 * time.Second
	// resultTimeout bounds one result post.
	resultTimeout = 5 * time.Second
	// retryDelay is the pause after a failed poll (the service restarting,
	// not listening yet); waitDelay after an answer that will not change
	// soon (no session, an older service without the endpoints).
	retryDelay = 2 * time.Second
	waitDelay  = 30 * time.Second
	// loginDelay is the pause while a login is missing: the user may log
	// in from the window any moment.
	loginDelay = 10 * time.Second
	// maxRunning bounds the jobs running at once. The service waits 3s for
	// each, so more would only be jobs nobody waits for.
	maxRunning = 4
)

// Options are what the executor needs from the GUI.
type Options struct {
	// Base is the WebUI address ("http://127.0.0.1:5002"), read per
	// request: the port can move (#121).
	Base func() string
	// Jar holds the GUI's session cookie.
	Jar http.CookieJar
	// Relogin is called after a 401: it asks for the local session again
	// (#131) and reports whether that worked, in which case the request
	// is retried at once. Nil: wait and retry.
	Relogin func() bool
	// Logf writes the GUI log. Only changes of state are logged.
	Logf func(format string, args ...any)

	// run performs one job; useraction.Run. Replaced by the tests.
	run func(args []string) []byte
	// sleep waits d or until ctx ends; replaced by the tests.
	sleep func(ctx context.Context, d time.Duration)
}

// executor is one running Start.
type executor struct {
	o    Options
	http *http.Client
	sem  chan struct{}
	// state is the last logged state ("connected", "unauthorized", …).
	state string
}

// Start polls for jobs until the returned stop is called. stop waits for
// the poll loop to end, not for jobs still running.
func Start(o Options) (stop func()) {
	if o.run == nil {
		o.run = runJob
	}
	if o.sleep == nil {
		o.sleep = sleepCtx
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	e := &executor{
		o:    o,
		http: &http.Client{Jar: o.Jar, Timeout: pollTimeout},
		sem:  make(chan struct{}, maxRunning),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.loop(ctx)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// runJob runs one job on an OS thread of its own. The handlers set COM up
// themselves, each the way it needs (core audio and the media session in
// the MTA, ShellExecute for a url preset in an STA), exactly as they did
// in the subcommand's process; one shared worker thread initialised for
// either would break the other (RPC_E_CHANGED_MODE). The goroutine never
// unlocks, so Go ends the thread with it and whatever a handler left
// behind on it goes too — the nearest thing to the process per job it
// replaces.
func runJob(args []string) []byte {
	out := make(chan []byte, 1)
	go func() {
		runtime.LockOSThread()
		line, _ := useraction.Run(args)
		out <- line
	}()
	return <-out
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// pollOutcome is what one poll came to.
type pollOutcome int

const (
	pollJob pollOutcome = iota
	pollEmpty
	pollUnauthorized
	pollRefused  // 403: the service does not take this process for a tray app
	pollNoRoute  // 404: a service older than #164
	pollNotReady // 409: nobody's session to take jobs for, or similar
	pollFailed   // the service is unreachable or answered something else
)

func (e *executor) loop(ctx context.Context) {
	for ctx.Err() == nil {
		job, outcome, err := e.poll(ctx)
		if ctx.Err() != nil {
			return
		}
		if outcome == pollUnauthorized && e.o.Relogin != nil && e.o.Relogin() {
			continue
		}
		e.note(outcome, err)
		switch outcome {
		case pollJob:
			e.start(ctx, job)
		case pollEmpty:
		case pollFailed:
			e.o.sleep(ctx, retryDelay)
		case pollUnauthorized:
			e.o.sleep(ctx, loginDelay)
		default:
			e.o.sleep(ctx, waitDelay)
		}
	}
}

// note logs a change of state.
func (e *executor) note(outcome pollOutcome, err error) {
	var state, msg string
	switch outcome {
	case pollJob, pollEmpty:
		state, msg = "connected", "taking user-session commands from the service"
	case pollUnauthorized:
		state, msg = "unauthorized", "the service wants a login; user-session commands wait for it"
	case pollRefused:
		state, msg = "refused", "the service does not take this process as the tray app: "+errText(err)
	case pollNoRoute:
		state, msg = "noroute", "the service has no tray job endpoint (older than v1.2.4)"
	case pollNotReady:
		state, msg = "notready", "the service takes no jobs from this session now: "+errText(err)
	default:
		state, msg = "failed", "cannot reach the service: "+errText(err)
	}
	if state != e.state {
		e.state = state
		e.o.Logf("%s", msg)
	}
}

func errText(err error) string {
	if err == nil {
		return "-"
	}
	return err.Error()
}

// poll asks for one job.
func (e *executor) poll(ctx context.Context) (Job, pollOutcome, error) {
	resp, err := e.post(ctx, PathPoll, nil, pollTimeout)
	if err != nil {
		return Job{}, pollFailed, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return Job{}, pollEmpty, nil
	case http.StatusUnauthorized:
		return Job{}, pollUnauthorized, nil
	case http.StatusForbidden:
		return Job{}, pollRefused, replyError(resp)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return Job{}, pollNoRoute, nil
	case http.StatusConflict:
		return Job{}, pollNotReady, replyError(resp)
	default:
		return Job{}, pollFailed, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r PollReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return Job{}, pollFailed, fmt.Errorf("poll reply: %w", err)
	}
	if r.Job == nil || r.Job.ID == "" {
		return Job{}, pollEmpty, nil
	}
	return *r.Job, pollJob, nil
}

// replyError reads {"error": code} from a refusal.
func replyError(resp *http.Response) error {
	var r struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&r)
	switch {
	case r.Error != "":
		return errors.New(r.Error)
	case r.Code != "":
		return errors.New(r.Code)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// start runs job in the background and posts its reply; the loop polls
// again at once.
func (e *executor) start(ctx context.Context, job Job) {
	go func() {
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		line := e.o.run(job.Args)
		<-e.sem
		if err := e.report(ctx, job.ID, line); err != nil && ctx.Err() == nil {
			e.o.Logf("reply to the service for %s: %v", argName(job.Args), err)
		}
	}()
}

func argName(args []string) string {
	if len(args) == 0 {
		return "a job"
	}
	return args[0]
}

// report posts the reply line of job id, logging in again once after a
// 401. A 404 means the service stopped waiting (it timed out); nothing to
// do about it.
func (e *executor) report(ctx context.Context, id string, line []byte) error {
	body := ResultBody{ID: id, Result: json.RawMessage(bytes.TrimSpace(line))}
	for attempt := 0; ; attempt++ {
		resp, err := e.post(ctx, PathResult, body, resultTimeout)
		if err != nil {
			return err
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusNotFound:
			return nil
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0 && e.o.Relogin != nil && e.o.Relogin():
			continue
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// post sends one API request with the CSRF header the WebUI wants.
func (e *executor) post(ctx context.Context, path string, body any, timeout time.Duration) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, e.o.Base()+path, &buf)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.http.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

// cancelOnClose releases a request's context once its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
