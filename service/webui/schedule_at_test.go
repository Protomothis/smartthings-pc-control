package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
)

// atCommands records what /api/schedule armed.
type atCommands struct {
	fakeCommands
	delay time.Duration
	at    time.Time
}

func (c *atCommands) Schedule(_ string, d time.Duration) error { c.delay = d; return nil }
func (c *atCommands) ScheduleAt(_ string, at time.Time) error  { c.at = at; return nil }

// POST /api/schedule takes a clock time (#173): "at" wins over "minutes",
// which a service from before it reads instead; a bad "at" is refused.
func TestScheduleAt(t *testing.T) {
	ts := newTestServer(t, config.Config{})
	rec := &atCommands{}
	ts.d.Commands = rec
	post := func(body string) int {
		w := httptest.NewRecorder()
		ts.serveSchedule(w, httptest.NewRequest(http.MethodPost, "/api/schedule", strings.NewReader(body)))
		return w.Code
	}
	if code := post(`{"command":"shutdown","minutes":600,"at":"2026-11-23T07:30:00+09:00"}`); code != http.StatusOK {
		t.Fatalf("at: %d", code)
	}
	want := time.Date(2026, 11, 23, 7, 30, 0, 0, time.FixedZone("", 9*3600))
	if !rec.at.Equal(want) || rec.delay != 0 {
		t.Errorf("armed at %v delay %v, want at %v", rec.at, rec.delay, want)
	}
	if code := post(`{"command":"shutdown","minutes":10080}`); code != http.StatusOK || rec.delay != 7*24*time.Hour {
		t.Errorf("a week of minutes: %d, %v", code, rec.delay)
	}
	for _, bad := range []string{
		`{"command":"shutdown","at":"tomorrow"}`,
		`{"command":"shutdown","minutes":10081}`,
		`{"command":"shutdown","minutes":0}`,
	} {
		if code := post(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
}
