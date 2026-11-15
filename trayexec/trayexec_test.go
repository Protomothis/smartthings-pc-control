package trayexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeService hands out the queued jobs one per poll, answers 204 when
// none is left, and collects the result posts.
type fakeService struct {
	mu      sync.Mutex
	jobs    []Job
	results chan ResultBody
	// unauthorized is how many requests still get a 401.
	unauthorized atomic.Int32
	csrfMissing  atomic.Bool
}

func newFakeService(t *testing.T, jobs ...Job) (*fakeService, *httptest.Server) {
	t.Helper()
	f := &fakeService{jobs: jobs, results: make(chan ResultBody, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc(PathPoll, func(w http.ResponseWriter, r *http.Request) {
		if !f.admit(w, r) {
			return
		}
		f.mu.Lock()
		if len(f.jobs) == 0 {
			f.mu.Unlock()
			// A real poll waits; a short wait keeps the loop from spinning.
			time.Sleep(10 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		j := f.jobs[0]
		f.jobs = f.jobs[1:]
		f.mu.Unlock()
		json.NewEncoder(w).Encode(PollReply{Job: &j})
	})
	mux.HandleFunc(PathResult, func(w http.ResponseWriter, r *http.Request) {
		if !f.admit(w, r) {
			return
		}
		var b ResultBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.results <- b
		w.Write([]byte(`{"status":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeService) admit(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		f.csrfMissing.Store(true)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	if f.unauthorized.Load() > 0 {
		f.unauthorized.Add(-1)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *fakeService) result(t *testing.T) ResultBody {
	t.Helper()
	select {
	case r := <-f.results:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no result was posted")
		return ResultBody{}
	}
}

func noSleep(context.Context, time.Duration) {}

// A job is run with the arguments as sent and its reply line posted back
// under the job's id.
func TestExecutorRunsJobAndPostsReply(t *testing.T) {
	f, srv := newFakeService(t, Job{ID: "j1", Args: []string{"notify", "--title", "t", "--text", `a & b | "c"`}})
	var got []string
	var mu sync.Mutex
	stop := Start(Options{
		Base: func() string { return srv.URL },
		run: func(args []string) []byte {
			mu.Lock()
			got = args
			mu.Unlock()
			return []byte(`{"ok":true,"shown":true}` + "\n")
		},
		sleep: noSleep,
	})
	defer stop()

	r := f.result(t)
	if r.ID != "j1" || string(r.Result) != `{"ok":true,"shown":true}` {
		t.Errorf("posted %s %s", r.ID, r.Result)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"notify", "--title", "t", "--text", `a & b | "c"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
	if f.csrfMissing.Load() {
		t.Error("a request went without the CSRF header")
	}
}

// The real path: useraction's own parser answers a job it rejects, in
// the subcommand's reply format.
func TestExecutorUsesUseraction(t *testing.T) {
	f, srv := newFakeService(t, Job{ID: "j2", Args: []string{"audio", "set", "250"}})
	stop := Start(Options{Base: func() string { return srv.URL }, sleep: noSleep})
	defer stop()

	r := f.result(t)
	var reply struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.Result, &reply); err != nil || reply.OK || reply.Error != "bad_args" {
		t.Errorf("reply %s (%v), want a bad_args line", r.Result, err)
	}
}

// A 401 asks the GUI to log in again and retries at once.
func TestExecutorLogsInAgainAfter401(t *testing.T) {
	f, srv := newFakeService(t, Job{ID: "j3", Args: []string{"media", "next"}})
	f.unauthorized.Store(1)
	var relogins atomic.Int32
	stop := Start(Options{
		Base:    func() string { return srv.URL },
		Relogin: func() bool { relogins.Add(1); return true },
		run:     func([]string) []byte { return []byte(`{"ok":true}`) },
		sleep:   noSleep,
	})
	defer stop()

	if r := f.result(t); r.ID != "j3" {
		t.Errorf("posted %+v", r)
	}
	if n := relogins.Load(); n != 1 {
		t.Errorf("relogins = %d, want 1", n)
	}
}

// stop ends a poll that is waiting.
func TestExecutorStopEndsPoll(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-block:
		}
	}))
	defer srv.Close()
	defer close(block)
	stop := Start(Options{Base: func() string { return srv.URL }, sleep: noSleep})
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not end the waiting poll")
	}
}

// The quit notice (#167) is one POST to PathQuit with the CSRF header; an
// older service without the endpoint is an error, not a hang.
func TestNotifyQuit(t *testing.T) {
	var got atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(PathQuit, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if err := NotifyQuit(Options{Base: func() string { return srv.URL }}); err != nil {
		t.Fatalf("NotifyQuit: %v", err)
	}
	if got.Load() != 1 {
		t.Errorf("%d quit posts, want 1", got.Load())
	}

	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if err := NotifyQuit(Options{Base: func() string { return old.URL }}); err == nil {
		t.Error("an older service's 404 was not reported")
	}
}
