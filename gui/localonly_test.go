package gui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientLocalOnlyReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"local_only","message":"local session required"}`))
	}))
	defer srv.Close()
	c := clientFor(t, srv)
	if _, err := c.SaveConfig(Config{}); !errors.Is(err, errLocalOnly) {
		t.Errorf("SaveConfig = %v", err)
	}
	if _, err := c.RunningProcesses(); !errors.Is(err, errLocalOnly) {
		t.Errorf("RunningProcesses = %v", err)
	}
	if _, err := c.TestPreset(Preset{Slot: 1}); !errors.Is(err, errLocalOnly) {
		t.Errorf("TestPreset = %v", err)
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","message":"saved","warnings":[{"slot":2,"code":"writable_by_others","path":"C:\\x.ps1"}]}`))
	}))
	defer ok.Close()
	c = clientFor(t, ok)
	reply, err := c.SaveConfig(Config{})
	if err != nil || reply.Message != "saved" || len(reply.Warnings) != 1 || reply.Warnings[0].Slot != 2 || reply.Warnings[0].Path != `C:\x.ps1` {
		t.Errorf("SaveConfig = %+v, %v", reply, err)
	}
	ws, err := c.TestPreset(Preset{Slot: 2})
	if err != nil || len(ws) != 1 || ws[0].Code != "writable_by_others" {
		t.Errorf("TestPreset = %+v, %v", ws, err)
	}
}
