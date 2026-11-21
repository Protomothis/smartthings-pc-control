package service

// Modern standby (S0 low power idle) laptops (#161, #162): suspend turns
// the display off instead of calling SetSuspendState, which would
// hibernate them, and the screen commands say what standby means there.
// The capabilities and the user-session run are fakes: no test reads the
// machine's sleep states, suspends it or turns its display off.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/power"
)

var (
	modernStandbyLaptop = power.Capabilities{AoAc: true, S4: true, HiberFile: true}
	s3Desktop           = power.Capabilities{S3: true, S4: true, HiberFile: true}
)

// fakePowerCaps answers caps (or err) for the sleep states, from a fresh
// cache, and counts the reads.
func fakePowerCaps(t *testing.T, caps power.Capabilities, err error) *int {
	t.Helper()
	reads := new(int)
	saved := sys.powerCaps
	sys.powerCaps = func() (power.Capabilities, error) { *reads++; return caps, err }
	resetPowerCaps()
	t.Cleanup(func() { sys.powerCaps = saved; resetPowerCaps() })
	return reads
}

// stubScreenRun records the `user-action screen` runs and answers *runErr.
func stubScreenRun(t *testing.T) (got *[][]string, runErr *error) {
	t.Helper()
	got, runErr = new([][]string), new(error)
	saved := userRun.screen
	userRun.screen = func(_ context.Context, args ...string) (UserActionResult, error) {
		*got = append(*got, args)
		return UserActionResult{OK: *runErr == nil}, *runErr
	}
	t.Cleanup(func() { userRun.screen = saved; setDisplayState("unknown") })
	setDisplayState("unknown")
	return got, runErr
}

// noSetSuspendState fails the test when SetSuspendState is called.
func noSetSuspendState(t *testing.T) {
	t.Helper()
	saved := sys.suspend
	sys.suspend = func(hibernate bool) error {
		t.Errorf("SetSuspendState(%v) called on a modern standby PC", hibernate)
		return nil
	}
	t.Cleanup(func() { sys.suspend = saved })
}

// The sleep states are read once per process, however often the
// commands ask.
func TestPowerCapsReadOnce(t *testing.T) {
	reads := fakePowerCaps(t, modernStandbyLaptop, nil)
	for range 3 {
		if !modernStandby() {
			t.Fatal("modernStandby() = false for AoAc")
		}
	}
	if *reads != 1 {
		t.Errorf("GetPwrCapabilities read %d times, want 1", *reads)
	}
}

func TestLogPowerCapabilities(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	buf := captureLog(t)
	logPowerCapabilities()
	if want := "Power: modern standby (S0 low power idle), no S3, hibernate on"; !strings.Contains(buf.String(), want) {
		t.Errorf("log %q, want %q", buf.String(), want)
	}

	fakePowerCaps(t, power.Capabilities{}, errors.New("GetPwrCapabilities: boom"))
	logPowerCapabilities()
	if !strings.Contains(buf.String(), "Power: sleep states unreadable (GetPwrCapabilities: boom); suspend uses SetSuspendState") {
		t.Errorf("log %q", buf.String())
	}
	if modernStandby() {
		t.Error("unreadable capabilities count as modern standby")
	}
}

// #162: suspend on a modern standby laptop is the screen-off user action
// in the console session, never SetSuspendState. The power.stopping hint
// is left alone, so a later stop is not mistaken for this suspend.
func TestSuspendOnModernStandbyTurnsTheDisplayOff(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	noSetSuspendState(t)
	runs, _ := stubScreenRun(t)
	resetPowerCommandHint()
	t.Cleanup(resetPowerCommandHint)
	events := captureNotifications(t)
	buf := captureLog(t)

	Commands["suspend"].Execute()
	if want := [][]string{{"screen", "off"}}; !reflect.DeepEqual(*runs, want) {
		t.Errorf("user-action runs %q, want %q", *runs, want)
	}
	if getDisplayState() != "off" {
		t.Errorf("display state = %q, want off", getDisplayState())
	}
	if got := stoppingReason("unknown"); got != "unknown" {
		t.Errorf("stoppingReason = %q, want unknown (no hint)", got)
	}
	log := buf.String()
	for _, want := range []string{
		"suspend: modern standby - turning the display off to enter standby instead of SetSuspendState, which would hibernate",
		"suspend ok: display off, Windows enters modern standby",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q lacks %q", log, want)
		}
	}
	expectNoNotification(t, events)
}

// Desktop processes are throttled as the display goes off, so a timed-out
// run is the usual case and still counts.
func TestSuspendOnModernStandbyTimeoutCounts(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	noSetSuspendState(t)
	_, runErr := stubScreenRun(t)
	*runErr = fmt.Errorf("%w after 3s (user-action screen)", errUserActionTimeout)
	events := captureNotifications(t)
	buf := captureLog(t)

	suspendPC()
	if getDisplayState() != "off" {
		t.Errorf("display state = %q, want off", getDisplayState())
	}
	if !strings.Contains(buf.String(), "assuming Windows enters modern standby") {
		t.Errorf("log %q", buf.String())
	}
	expectNoNotification(t, events)
}

// A run that did not happen is system.exec_failed, and there is no
// fallback to SetSuspendState: hibernating is the bug.
func TestSuspendOnModernStandbyFailureDoesNotHibernate(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	noSetSuspendState(t)
	_, runErr := stubScreenRun(t)
	*runErr = errNoConsoleSession
	events := captureNotifications(t)
	buf := captureLog(t)

	suspendPC()
	ev := expectNotification(t, events, "system.exec_failed")
	if ev.Fields["command"] != "suspend" {
		t.Errorf("fields = %v", ev.Fields)
	}
	if want := "modern standby: could not turn the display off (nobody is signed in on the PC's own screen); not hibernating instead"; ev.Fields["error"] != want {
		t.Errorf("error = %q, want %q", ev.Fields["error"], want)
	}
	if getDisplayState() != "unknown" {
		t.Errorf("display state = %q, want unknown (unchanged)", getDisplayState())
	}
	if !strings.Contains(buf.String(), "not falling back to SetSuspendState, which would hibernate") {
		t.Errorf("log %q", buf.String())
	}
}

// The S3 path is the one it always was; a PC without S3 that is not
// modern standby still gets SetSuspendState, with a warning in the log.
func TestSuspendWithoutModernStandbyCallsSetSuspendState(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps power.Capabilities
		err  error
		warn bool
	}{
		{"S3", s3Desktop, nil, false},
		{"no S3", power.Capabilities{S4: true, HiberFile: true}, nil, true},
		{"unreadable", power.Capabilities{}, errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakePowerCaps(t, tc.caps, tc.err)
			runs, _ := stubScreenRun(t)
			var calls []bool
			saved := sys.suspend
			sys.suspend = func(hibernate bool) error { calls = append(calls, hibernate); return nil }
			t.Cleanup(func() { sys.suspend = saved })
			resetPowerCommandHint()
			t.Cleanup(resetPowerCommandHint)
			buf := captureLog(t)

			suspendPC()
			if !reflect.DeepEqual(calls, []bool{false}) || len(*runs) != 0 {
				t.Errorf("SetSuspendState %v, screen runs %q", calls, *runs)
			}
			if got := stoppingReason("unknown"); got != "suspend" {
				t.Errorf("stoppingReason = %q, want suspend", got)
			}
			if warned := strings.Contains(buf.String(), "reports no S3 sleep"); warned != tc.warn {
				t.Errorf("no-S3 warning %v, want %v: %q", warned, tc.warn, buf.String())
			}
		})
	}
}

// screenHoldFake records the screen-off hold's power request and lets the
// test drive the display state.
type screenHoldFake struct {
	calls   []string
	display func(on bool)
	setErr  error
}

// fakeScreenHold installs a screen-off hold on fakes (#161).
func fakeScreenHold(t *testing.T) *screenHoldFake {
	t.Helper()
	f := &screenHoldFake{}
	saved := screenHold
	screenHold = power.NewScreenHold(power.ScreenHoldHooks{
		Set:   func() error { f.calls = append(f.calls, "set"); return f.setErr },
		Clear: func() error { f.calls = append(f.calls, "clear"); return nil },
		Watch: func(onChange func(bool)) (func(), error) {
			f.display = onChange
			return func() {}, nil
		},
		AfterFunc: func(time.Duration, func()) func() bool { return func() bool { return true } },
		Logf:      logMsg,
	})
	t.Cleanup(func() { screenHold = saved })
	return f
}

// #161: screen off holds a system-required power request on a modern
// standby PC, so it stays awake with the display off and screen on still
// reaches the tray; the hold ends when the display is on again.
func TestScreenOffOnModernStandbyHoldsTheSystemAwake(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	hold := fakeScreenHold(t)
	runs, _ := stubScreenRun(t)
	events := captureNotifications(t)
	buf := captureLog(t)

	Commands["turnscreenoff"].Execute()
	if !reflect.DeepEqual(hold.calls, []string{"set"}) || !screenHold.Held() {
		t.Fatalf("power request calls %q, held %v", hold.calls, screenHold.Held())
	}
	if want := "screen off: modern standby - holding a system-required power request so the PC stays awake with the display off and screen on still works"; !strings.Contains(buf.String(), want) {
		t.Errorf("log %q lacks %q", buf.String(), want)
	}
	if getDisplayState() != "off" {
		t.Errorf("display state = %q, want off", getDisplayState())
	}
	hold.display(false)

	Commands["turnscreenon"].Execute()
	if want := [][]string{{"screen", "off"}, {"screen", "on"}}; !reflect.DeepEqual(*runs, want) {
		t.Errorf("user-action runs %q, want %q", *runs, want)
	}
	if getDisplayState() != "on" {
		t.Errorf("display state = %q, want on", getDisplayState())
	}
	hold.display(true)
	if screenHold.Held() || !reflect.DeepEqual(hold.calls, []string{"set", "clear"}) {
		t.Errorf("after the display came on: calls %q, held %v", hold.calls, screenHold.Held())
	}
	if !strings.Contains(buf.String(), "Screen-off hold: released (the display is on again)") {
		t.Errorf("log %q", buf.String())
	}
	expectNoNotification(t, events)
}

// A screen off that did not run (no tray, nobody at the console) does not
// leave the hold behind; one that timed out keeps it (the display is going
// off). A hold that cannot be taken leaves screen off as it was.
func TestScreenOffHoldOnFailures(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	hold := fakeScreenHold(t)
	_, runErr := stubScreenRun(t)
	buf := captureLog(t)

	*runErr = errTrayUnavailable
	Commands["turnscreenoff"].Execute()
	if screenHold.Held() || !reflect.DeepEqual(hold.calls, []string{"set", "clear"}) {
		t.Errorf("failed screen off: calls %q, held %v", hold.calls, screenHold.Held())
	}
	if !strings.Contains(buf.String(), "Screen-off hold: released (screen off failed)") {
		t.Errorf("log %q", buf.String())
	}

	hold.calls = nil
	*runErr = fmt.Errorf("%w after 3s (user-action screen)", errUserActionTimeout)
	Commands["turnscreenoff"].Execute()
	if !screenHold.Held() {
		t.Error("a timed-out screen off dropped the hold")
	}
	screenHold.Release("test")

	hold.calls = nil
	hold.setErr = errors.New("PowerSetRequest(SystemRequired): denied")
	*runErr = nil
	Commands["turnscreenoff"].Execute()
	if screenHold.Held() || getDisplayState() != "off" {
		t.Errorf("held %v, display %q", screenHold.Held(), getDisplayState())
	}
	if want := "screen off: modern standby - on this PC the display off puts it into standby; screen on may not wake it (only real input or the power button does); keeping it awake failed: PowerSetRequest(SystemRequired): denied"; !strings.Contains(buf.String(), want) {
		t.Errorf("log %q lacks %q", buf.String(), want)
	}
}

// Suspend on a modern standby PC is meant to sleep: it drops a screen-off
// hold before turning the display off.
func TestSuspendReleasesTheScreenOffHold(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	noSetSuspendState(t)
	hold := fakeScreenHold(t)
	stubScreenRun(t)
	buf := captureLog(t)

	Commands["turnscreenoff"].Execute()
	hold.display(false)
	Commands["suspend"].Execute()
	if screenHold.Held() || !reflect.DeepEqual(hold.calls, []string{"set", "clear"}) {
		t.Errorf("calls %q, held %v", hold.calls, screenHold.Held())
	}
	if !strings.Contains(buf.String(), "Screen-off hold: released (suspend)") {
		t.Errorf("log %q", buf.String())
	}
}

// #161: a screen on that times out (the hold did not keep the PC out of
// standby: battery, lid, power button) is not taken for done.
func TestScreenCommandsOnModernStandby(t *testing.T) {
	fakePowerCaps(t, modernStandbyLaptop, nil)
	fakeScreenHold(t)
	_, runErr := stubScreenRun(t)
	events := captureNotifications(t)
	buf := captureLog(t)

	Commands["turnscreenoff"].Execute()
	if getDisplayState() != "off" {
		t.Errorf("display state = %q, want off", getDisplayState())
	}
	expectNoNotification(t, events)

	*runErr = fmt.Errorf("%w after 3s (user-action screen)", errUserActionTimeout)
	Commands["turnscreenon"].Execute()
	log := buf.String()
	if want := "screen on: modern standby - the PC is in standby after screen off; Windows only leaves it on real input or the power button (user-action timed out after 3s (user-action screen))"; !strings.Contains(log, want) {
		t.Errorf("log %q lacks %q", log, want)
	}
	if strings.Contains(log, "assuming it took effect") {
		t.Errorf("screen on claimed success: %q", log)
	}
	if getDisplayState() != "off" {
		t.Errorf("display state = %q, want off (unchanged)", getDisplayState())
	}
	ev := expectNotification(t, events, "system.exec_failed")
	if ev.Fields["command"] != "turnscreenon" || !strings.Contains(ev.Fields["error"], "real input or the power button") {
		t.Errorf("fields = %v", ev.Fields)
	}

	// A screen on that answered (the PC was not in standby yet) worked.
	*runErr = nil
	Commands["turnscreenon"].Execute()
	if getDisplayState() != "on" {
		t.Errorf("display state = %q, want on", getDisplayState())
	}
	expectNoNotification(t, events)
}

// On a PC with S3 a timed-out screen on is still assumed to have worked
// and nothing is said about standby. No power request is taken (#161).
func TestScreenCommandsWithoutModernStandby(t *testing.T) {
	fakePowerCaps(t, s3Desktop, nil)
	hold := fakeScreenHold(t)
	_, runErr := stubScreenRun(t)
	events := captureNotifications(t)
	buf := captureLog(t)
	t.Cleanup(func() {
		if len(hold.calls) != 0 || screenHold.Held() {
			t.Errorf("S3 PC: power request calls %q, held %v", hold.calls, screenHold.Held())
		}
	})

	Commands["turnscreenoff"].Execute()
	*runErr = fmt.Errorf("%w after 3s", errUserActionTimeout)
	Commands["turnscreenon"].Execute()
	if getDisplayState() != "on" {
		t.Errorf("display state = %q, want on", getDisplayState())
	}
	log := buf.String()
	if strings.Contains(log, "modern standby") || !strings.Contains(log, "(assuming it took effect)") {
		t.Errorf("log %q", log)
	}
	expectNoNotification(t, events)
}

// The Telegram replies to sleep and the screen commands carry a note on a
// modern standby PC, in the bot's language, and nothing on other PCs.
func TestTelegramRepliesNoteModernStandby(t *testing.T) {
	initLogger()
	setConfig(Config{})
	t.Cleanup(func() { setConfig(Config{}) })
	off := stubCommand(t, "turnscreenoff")
	on := stubCommand(t, "turnscreenon")
	sleep := stubCommand(t, "suspend")
	lock := stubCommand(t, "lock")
	h := tgCtl
	ctx := context.Background()

	fakePowerCaps(t, modernStandbyLaptop, nil)
	html, _, _ := h.HandleCommand(ctx, "42", "screenoff", nil)
	expectExecuted(t, off, "turnscreenoff")
	if !strings.Contains(html, "화면이 꺼져 있는 동안 절전에 들어가지 않게 붙잡아 둡니다") {
		t.Errorf("/screenoff reply = %q", html)
	}
	html, _, _ = h.HandleCommand(ctx, "42", "screenon", nil)
	expectExecuted(t, on, "turnscreenon")
	if !strings.Contains(html, "실제 키보드 · 마우스 입력이나 전원 버튼으로만 깨어납니다") {
		t.Errorf("/screenon reply = %q", html)
	}
	html, _, _ = h.HandleCommand(ctx, "42", "lock", nil)
	expectExecuted(t, lock, "lock")
	if strings.Contains(html, "모던 스탠바이") {
		t.Errorf("/lock reply carries the note: %q", html)
	}
	edit, _, err := h.HandleCallback(ctx, "42", 7, "", "exec:suspend")
	if err != nil {
		t.Fatal(err)
	}
	expectExecuted(t, sleep, "suspend")
	if !strings.Contains(edit, "✅ 실행됨 · ") || !strings.HasSuffix(edit, "화면을 꺼서 Windows 절전에 들어갑니다. 깨울 때는 키보드 · 마우스 · 전원 버튼을 쓰세요.") {
		t.Errorf("exec:suspend edit = %q", edit)
	}
	edit, _, _ = h.HandleCallback(ctx, "42", 7, "", "cmd:turnscreenoff")
	expectExecuted(t, off, "turnscreenoff")
	if !strings.Contains(edit, "모던 스탠바이") {
		t.Errorf("cmd:turnscreenoff edit = %q", edit)
	}
	setConfig(Config{Telegram: TelegramConfig{Lang: "en"}})
	html, _, _ = h.HandleCommand(ctx, "42", "screenon", nil)
	expectExecuted(t, on, "turnscreenon")
	if !strings.Contains(html, "This PC uses modern standby (S0)") {
		t.Errorf("en /screenon reply = %q", html)
	}

	fakePowerCaps(t, s3Desktop, nil)
	html, _, _ = h.HandleCommand(ctx, "42", "screenoff", nil)
	expectExecuted(t, off, "turnscreenoff")
	edit, _, _ = h.HandleCallback(ctx, "42", 7, "", "exec:suspend")
	expectExecuted(t, sleep, "suspend")
	if strings.Contains(html+edit, "modern standby") {
		t.Errorf("S3 PC replies carry the note: %q / %q", html, edit)
	}
}
