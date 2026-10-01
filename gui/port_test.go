package gui

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// configServer answers /api/config with a config whose secret names who answered.
func configServer(t *testing.T, name string, status int) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, `{"secret":"`+name+`"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, srv.Listener.Addr().(*net.TCPAddr).Port
}

// withPorts sets the current and the tray.json port for one test.
func withPorts(t *testing.T, cur int, disk *int) {
	t.Helper()
	savedCur, savedDisk := webUIPort.Load(), diskWebUIPort
	webUIPort.Store(int32(cur))
	diskWebUIPort = func() int { return *disk }
	t.Cleanup(func() { webUIPort.Store(savedCur); diskWebUIPort = savedDisk })
}

func answeredBy(t *testing.T, c serviceAPI) string {
	t.Helper()
	cfg, err := c.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	return cfg.Secret
}

func TestSetPortMovesTheSameClient(t *testing.T) {
	_, portA := configServer(t, "A", http.StatusOK)
	_, portB := configServer(t, "B", http.StatusOK)
	withPorts(t, portA, &portA)
	u := &ui{client: NewClient(portA)}
	c := u.client

	if got := answeredBy(t, c); got != "A" {
		t.Fatalf("before: %q", got)
	}
	if !u.setPort(portB) {
		t.Fatal("setPort to a new port reported no change")
	}
	if u.client != c {
		t.Error("setPort replaced the *Client; the polling goroutines would race on the field")
	}
	if got := answeredBy(t, c); got != "B" || currentWebUIPort() != portB {
		t.Errorf("after: %q on port %d, want B on %d", got, currentWebUIPort(), portB)
	}
	if u.setPort(portB) {
		t.Error("setPort to the same port reported a change")
	}

	// A poll in flight while the port moves (meaningful under -race).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			c.GetConfig()
		}
	}()
	for i := 0; i < 20; i++ {
		u.setPort([]int{portA, portB}[i%2])
	}
	<-done
}

func TestFollowPortChange(t *testing.T) {
	srvA, portA := configServer(t, "A", http.StatusOK)
	_, portB := configServer(t, "B", http.StatusUnauthorized) // a secret is set
	disk := portA
	withPorts(t, portA, &disk)
	u := &ui{client: NewClient(portA)}

	// tray.json still names the current port: nothing to follow.
	if u.followPortChange() {
		t.Fatal("moved without a port change")
	}

	// Saved a new port, service not restarted yet: nothing answers there
	// (a closed listener), so the app stays where the service still is.
	closed := httptest.NewServer(http.NotFoundHandler())
	disk = closed.Listener.Addr().(*net.TCPAddr).Port
	closed.Close()
	if u.followPortChange() || currentWebUIPort() != portA {
		t.Fatalf("moved to a port nobody listens on (now %d)", currentWebUIPort())
	}

	// The service restarted on the new port: a login-required reply is a
	// service all the same.
	srvA.Close()
	disk = portB
	if !u.followPortChange() {
		t.Fatal("did not follow the service to its new port")
	}
	if currentWebUIPort() != portB {
		t.Errorf("port = %d, want %d", currentWebUIPort(), portB)
	}
	if _, err := u.client.GetConfig(); !errors.Is(err, errUnauthorized) {
		t.Errorf("GetConfig on the new port: %v, want errUnauthorized", err)
	}
}
