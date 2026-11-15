package webui

// Tests for the tray job endpoints (#164) over a real loopback connection:
// the TCP table maps it to this test process, whose inspection is faked.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/trayexec"
)

// trayEndpointSetup serves the WebUI mux with peer as whoever connects.
func trayEndpointSetup(t *testing.T, cfg config.Config, peer *peerProcess) (*testServer, *httptest.Server) {
	t.Helper()
	s := newTestServer(t, cfg)
	s.trays.PollWait = 300 * time.Millisecond
	s.peerInspect = func(pid uint32) (peerProcess, error) {
		p := *peer
		p.PID = pid
		return p, nil
	}
	s.peerSameExe = isTray
	srv := httptest.NewServer(s.Routes(false))
	t.Cleanup(srv.Close)
	return s, srv
}

// trayPost posts body to path the way the tray app does.
func trayPost(t *testing.T, base, path string, body any) (status int, reply []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("POST", base+path, &buf)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		// Also called from goroutines, where t.Fatal must not be.
		t.Error(err)
		return 0, nil
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

// The tray polls, gets the job sent to its session, posts the reply, and
// the caller gets it. A standard user's tray qualifies.
func TestTrayPollAndResult(t *testing.T) {
	peer := trustedTray()
	peer.Admin = false
	s, srv := trayEndpointSetup(t, config.Config{Port: 5001}, &peer)

	type outcome struct {
		line []byte
		err  error
	}
	done := make(chan outcome, 1)
	polled := make(chan trayexec.PollReply, 1)
	go func() {
		code, body := trayPost(t, srv.URL, trayexec.PathPoll, nil)
		var r trayexec.PollReply
		if code == http.StatusOK {
			json.Unmarshal(body, &r)
		}
		polled <- r
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !s.trays.Connected(1) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		line, err := s.trays.Do(ctx, 1, []string{"audio", "mute", "on"})
		done <- outcome{line, err}
	}()
	r := <-polled
	if r.Job == nil || strings.Join(r.Job.Args, " ") != "audio mute on" {
		t.Fatalf("poll got %+v", r.Job)
	}
	code, body := trayPost(t, srv.URL, trayexec.PathResult, trayexec.ResultBody{ID: r.Job.ID, Result: json.RawMessage(`{"ok":true,"audio":{"volume":10,"muted":true,"device":"x"}}`)})
	if code != http.StatusOK {
		t.Fatalf("result: %d %s", code, body)
	}
	o := <-done
	if o.err != nil || !strings.Contains(string(o.line), `"muted":true`) {
		t.Errorf("caller got %q, %v", o.line, o.err)
	}
	// A second reply for the same job finds nobody waiting.
	if code, _ := trayPost(t, srv.URL, trayexec.PathResult, trayexec.ResultBody{ID: r.Job.ID, Result: json.RawMessage(`{"ok":true}`)}); code != http.StatusNotFound {
		t.Errorf("late result: %d, want 404", code)
	}
	// Nothing queued: the poll ends with 204.
	if code, _ := trayPost(t, srv.URL, trayexec.PathPoll, nil); code != http.StatusNoContent {
		t.Errorf("empty poll: %d, want 204", code)
	}
}

// A tray in another session than the command's never gets the job, and
// cannot answer it either; the session comes from the connection, not the
// request.
func TestTrayWrongSessionPoller(t *testing.T) {
	peer := trustedTray()
	peer.SessionID = 2
	s, srv := trayEndpointSetup(t, config.Config{Port: 5001}, &peer)

	polled := make(chan int, 1)
	go func() {
		code, _ := trayPost(t, srv.URL, trayexec.PathPoll, map[string]any{"session_id": 1})
		polled <- code
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !s.trays.Connected(2) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.trays.Connected(1) {
		t.Error("a poll from session 2 made session 1 connected")
	}
	if _, err := s.trays.Do(context.Background(), 1, []string{"media", "next"}); !errors.Is(err, session.ErrTrayUnavailable) {
		t.Errorf("session 1's command: %v, want ErrTrayUnavailable", err)
	}
	if code := <-polled; code != http.StatusNoContent {
		t.Errorf("session 2's poll: %d, want 204", code)
	}
	if code, _ := trayPost(t, srv.URL, trayexec.PathResult, trayexec.ResultBody{ID: "0123", Result: json.RawMessage(`{"ok":true}`)}); code != http.StatusNotFound {
		t.Errorf("result for an unknown job: %d, want 404", code)
	}
}

func TestTrayRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*peerProcess)
		code   string
	}{
		{"a service in session 0", func(p *peerProcess) { p.SessionID = 0 }, "session_0"},
		{"another program", func(p *peerProcess) { p.Image = `C:\Windows\explorer.exe` }, "other_exe"},
		{"runas of another account", func(p *peerProcess) { p.UserSID = "S-1-5-21-1-2-3-1002" }, "not_session_user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := trustedTray()
			tc.change(&peer)
			s, srv := trayEndpointSetup(t, config.Config{Port: 5001}, &peer)
			for _, path := range []string{trayexec.PathPoll, trayexec.PathResult, trayexec.PathQuit} {
				code, body := trayPost(t, srv.URL, path, trayexec.ResultBody{ID: "x", Result: json.RawMessage(`{"ok":true}`)})
				var r map[string]string
				json.Unmarshal(body, &r)
				if code != http.StatusForbidden || r["error"] != "not_tray" || r["code"] != tc.code {
					t.Errorf("%s: %d %s, want 403 not_tray %s", path, code, body, tc.code)
				}
			}
			if s.trays.Connected(peer.SessionID) {
				t.Error("a refused caller counts as a connected tray")
			}
			select {
			case id := <-s.quits:
				t.Errorf("a refused caller's quit notice was taken (session %d)", id)
			default:
			}
		})
	}
}

// With a secret set the endpoints want the session cookie like every
// other call of the tray app.
func TestTrayNeedsSession(t *testing.T) {
	peer := trustedTray()
	_, srv := trayEndpointSetup(t, config.Config{Port: 5001, Secret: "s3cr3t"}, &peer)
	if code, _ := trayPost(t, srv.URL, trayexec.PathPoll, nil); code != http.StatusUnauthorized {
		t.Errorf("poll without a session: %d, want 401", code)
	}
}

// The quit notice (#167) names the calling tray's session, whatever the
// request says.
func TestTrayQuitNotice(t *testing.T) {
	peer := trustedTray()
	peer.SessionID = 3
	s, srv := trayEndpointSetup(t, config.Config{Port: 5001}, &peer)
	if code, body := trayPost(t, srv.URL, trayexec.PathQuit, map[string]any{"session_id": 1}); code != http.StatusOK {
		t.Fatalf("quit: %d %s", code, body)
	}
	select {
	case id := <-s.quits:
		if id != 3 {
			t.Errorf("quit recorded for session %d, want the caller's 3", id)
		}
	default:
		t.Error("the quit notice was not passed on")
	}
}
