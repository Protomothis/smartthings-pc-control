package service

import (
	"testing"
	"time"
)

// Which commands every front end announces, in one table (#127).
func TestNotifiesCommand(t *testing.T) {
	cases := []struct {
		name, from string
		origin     scheduleOrigin
		want       bool
	}{
		{"lock", "10.0.0.5", originRemote, true},
		{"lock", "10.0.0.20", originSmartThings, true},
		{"ping", "10.0.0.20", originSmartThings, false},
		{"lock", "webui", originUI, true},
		{"lock", "app", originUI, false},
		{"lock", "tray", originUI, false},
		{"lock", "toast", originUI, false},
		{"lock", "telegram", originTelegram, false},
	}
	for _, c := range cases {
		if got := notifiesCommand(c.name, c.from, c.origin); got != c.want {
			t.Errorf("notifiesCommand(%s, %s, %s) = %v", c.name, c.from, c.origin, got)
		}
	}
}

// Every path records last_command under its own origin, except ping and
// the bot's own commands.
func TestDispatchCommandRecordsAndRuns(t *testing.T) {
	saved := getConfig()
	t.Cleanup(func() { setConfig(saved) })
	setConfig(Config{Port: 5001})
	t.Cleanup(func() { lastRemoteMu.Lock(); lastRemote = remoteRecord{}; lastRemoteMu.Unlock() })
	executed := stubCommand(t, "lock")

	for _, c := range []struct {
		from   string
		origin scheduleOrigin
		want   string
	}{
		{"10.0.0.5", originRemote, "remote"},
		{"10.0.0.20", originSmartThings, "smartthings"},
		{"app", originUI, "ui"},
	} {
		if deferred, ok := dispatchCommand("lock", c.from, c.origin, dispatchDefault); !ok || deferred != 0 {
			t.Fatalf("%s: deferred %v ok %v", c.origin, deferred, ok)
		}
		expectExecuted(t, executed, "lock")
		if lr := getLastRemote(); lr.Command != "lock" || lr.From != c.from || lr.Origin != c.want {
			t.Errorf("%s: last command %+v", c.origin, lr)
		}
	}

	noteRemoteCommandBy("restart", "10.0.0.5", "remote")
	dispatchCommand("lock", "telegram", originTelegram, dispatchImmediate)
	expectExecuted(t, executed, "lock")
	dispatchCommand("ping", "10.0.0.5", originRemote, dispatchDefault)
	if lr := getLastRemote(); lr.Command != "restart" {
		t.Errorf("telegram or ping was recorded: %+v", lr)
	}
	if _, ok := dispatchCommand("nope", "x", originUI, dispatchImmediate); ok {
		t.Error("an unknown command was dispatched")
	}
}

// dispatchImmediate and forceshutdown never wait; dispatchGrace waits even
// with shutdown_grace off.
func TestDispatchCommandGraceModes(t *testing.T) {
	saved := getConfig()
	t.Cleanup(func() { setConfig(saved) })
	stubTrayLauncher(t, nil)
	t.Cleanup(func() { cancelScheduleBy("api") })
	stubCommand(t, "restart")
	stubCommand(t, "forceshutdown")

	setConfig(Config{Port: 5001, ShutdownGrace: false, GraceSeconds: 30})
	if d, _ := dispatchCommand("restart", "h", originSmartThings, dispatchDefault); d != 0 {
		t.Errorf("grace off, default mode deferred %v", d)
	}
	if d, _ := dispatchCommand("restart", "h", originSmartThings, dispatchGrace); d != 30*time.Second {
		t.Errorf("grace mode deferred %v, want 30s", d)
	}
	cancelScheduleBy("api")

	setConfig(Config{Port: 5001, ShutdownGrace: true, GraceSeconds: 30})
	if d, _ := dispatchCommand("restart", "h", originUI, dispatchImmediate); d != 0 {
		t.Errorf("immediate deferred %v", d)
	}
	if d, _ := dispatchCommand("forceshutdown", "h", originRemote, dispatchGrace); d != 0 {
		t.Errorf("forceshutdown deferred %v", d)
	}
	if d, _ := dispatchCommand("restart", "h", originRemote, dispatchDefault); d != 30*time.Second {
		t.Errorf("grace on deferred %v, want 30s", d)
	}
	if s := getSchedule(); s["active"] != true || s["origin"] != "remote" {
		t.Errorf("schedule %v", s)
	}
}
