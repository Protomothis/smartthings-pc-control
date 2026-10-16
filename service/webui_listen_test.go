package service

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A WebUI port taken by another program leaves the service running but
// the app unable to connect; the failure must reach service.log, which the
// app's logs tab can still read then (#140). Nothing here touches the real
// firewall: browser access is off, and the cleanup it does goes to the
// fake netsh.
func TestWebUIBindFailureIsLogged(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port

	prev := getConfig()
	t.Cleanup(func() { setConfig(prev) })
	setConfig(Config{Port: taken - 1}) // the WebUI runs on port+1
	withFakeNetsh(t, newFakeNetsh())
	logs := captureLog(t)

	stop := make(chan struct{})
	defer close(stop)
	done := make(chan struct{})
	go func() {
		StartWebUI(stop)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartWebUI kept running although its port is taken")
	}
	got := logs.String()
	if !strings.Contains(got, "WebUI server on port "+strconv.Itoa(taken)+" failed") {
		t.Errorf("the bind failure is not in the log:\n%s", got)
	}
}
