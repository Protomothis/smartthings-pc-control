package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The command tab, tray menu and toast run commands through POST
// /api/command with the CSRF header (#120); GET /api/test is gone.
func TestTestCommandPostsToCommandAPI(t *testing.T) {
	var got struct{ method, path, csrf, command, by string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.csrf = r.Method, r.URL.Path, r.Header.Get("X-Requested-With")
		var body struct{ Command, By string }
		json.NewDecoder(r.Body).Decode(&body)
		got.command, got.by = body.Command, body.By
		switch body.Command {
		case "lock":
			w.Write([]byte(`{"status":"ok","command":"lock","message":"Command sent"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"status":"error","command":"nope","message":"Unknown command"}`))
		}
	}))
	defer srv.Close()
	c := &Client{base: srv.URL, http: srv.Client()}

	msg, err := c.TestCommand("lock")
	if err != nil || msg != "Command sent" {
		t.Fatalf("TestCommand(lock) = %q, %v", msg, err)
	}
	if got.method != "POST" || got.path != "/api/command" || got.csrf != "XMLHttpRequest" || got.command != "lock" || got.by != "app" {
		t.Errorf("request = %+v, want POST /api/command with the CSRF header and {command: lock, by: app}", got)
	}
	if _, err := c.TestCommand("nope"); err == nil || err.Error() != "Unknown command" {
		t.Errorf("unknown command error = %v, want the service's message", err)
	}
}
