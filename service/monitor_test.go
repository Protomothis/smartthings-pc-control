package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/stapi"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// No test may reach a real monitor: every `user-action monitor` run is
// refused unless a test fakes it (fakeMonitorRun), and the follow-up
// reading after a command never fires.
func init() {
	monitorRun = func(context.Context, ...string) (UserActionResult, error) {
		return UserActionResult{}, errors.New("monitor run not faked in this test")
	}
	monitorFollowUp = func() {}
}

// fakeMonitorRun answers `user-action monitor …` with reply(args), a
// user-action line, and records the calls.
func fakeMonitorRun(t *testing.T, reply func(args []string) (string, error)) *monitorCalls {
	t.Helper()
	calls := &monitorCalls{}
	saved := monitorRun
	monitorRun = func(_ context.Context, args ...string) (UserActionResult, error) {
		calls.add(args)
		line, err := reply(args)
		if err != nil {
			return UserActionResult{}, err
		}
		res, err := session.ParseOutput([]byte(line))
		if err == nil && !res.OK {
			err = &userActionError{Code: res.Error, Message: res.Message}
		}
		return res, err
	}
	t.Cleanup(func() { monitorRun = saved })
	return calls
}

type monitorCalls struct {
	mu    sync.Mutex
	calls [][]string
}

func (c *monitorCalls) add(args []string) {
	c.mu.Lock()
	c.calls = append(c.calls, args)
	c.mu.Unlock()
}

func (c *monitorCalls) all() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func strp(s string) *string { return &s }

// goldenMonitorList is the reading the golden world has: a DELL with
// DDC/CI as the primary, and a TV without.
func goldenMonitorList() []useraction.Monitor {
	return []useraction.Monitor{
		{ID: "m1", Name: "DELL U2720Q", Primary: true, DDC: true, Brightness: intp(40), Input: strp("hdmi1"),
			Inputs: []string{"dp1", "hdmi1", "hdmi2"}, Power: "on"},
		{ID: "m2", Name: "LG TV", Primary: false, DDC: false, Inputs: []string{}, Power: "unknown"},
	}
}

// seedMonitors stores list as a reading taken just now, so no status read
// in the next minute starts another one.
func seedMonitors(t *testing.T, list []useraction.Monitor) {
	t.Helper()
	monitors.reset()
	monitors.set(list, time.Now())
	if monitors.gate.Begin(time.Now(), monitorEvery, true) {
		monitors.gate.End(time.Now())
	}
	t.Cleanup(monitors.reset)
}

// goldenMonitors is the golden world's monitor state (contract tests).
func goldenMonitors(t *testing.T) {
	t.Helper()
	seedMonitors(t, goldenMonitorList())
}

// monitorReply is a commandCase setup: every monitor run answers line, or
// fails with err.
func monitorReply(line string, err error) func(t *testing.T) {
	return func(t *testing.T) {
		fakeMonitorRun(t, func([]string) (string, error) { return line, err })
	}
}

// commandRequestType is the type /st/v1/command decodes raw into: a
// monitor command's value may be a string (#144).
func commandRequestType(raw json.RawMessage) any {
	var peek struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &peek) == nil && action.IsMonitor(peek.Command) {
		return &stapi.MonitorCommandRequest{}
	}
	return &stapi.CommandRequest{}
}

const dellSetReply = `{"ok":true,"monitor":{"id":"m1","name":"DELL U2720Q","primary":true,"ddc":true,"brightness":70,"input":"hdmi1","inputs":["hdmi1"],"power":"on"}}`

func monitorCfg(enabled bool) Config {
	return Config{Port: 5001, Secret: "s", Monitor: MonitorConfig{Enabled: enabled}}
}

func TestMonitorCommandSetsThePrimaryAndKeepsTheInputList(t *testing.T) {
	stSetup(t, monitorCfg(true))
	seedMonitors(t, []useraction.Monitor{goldenMonitorList()[1], goldenMonitorList()[0]})
	calls := fakeMonitorRun(t, func([]string) (string, error) { return dellSetReply, nil })

	m, err := runMonitorCommand(context.Background(), action.MonitorBrightness, "70")
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"monitor", "set", "--id", "m1", "--brightness", "70"}}; !slices.EqualFunc(calls.all(), want, slices.Equal) {
		t.Errorf("ran %q", calls.all())
	}
	if *m.Brightness != 70 || !slices.Equal(m.Inputs, []string{"dp1", "hdmi1", "hdmi2"}) {
		t.Errorf("monitor = %+v", m)
	}
	list, _ := monitors.last()
	if *list[1].Brightness != 70 || list[0].ID != "m2" {
		t.Errorf("store = %+v", list)
	}
}

func TestMonitorCommandRefusals(t *testing.T) {
	stSetup(t, monitorCfg(false))
	seedMonitors(t, goldenMonitorList())
	calls := fakeMonitorRun(t, func([]string) (string, error) { return dellSetReply, nil })

	if _, err := runMonitorCommand(context.Background(), action.MonitorBrightness, "70"); !errors.Is(err, action.ErrMonitorDisabled) {
		t.Errorf("disabled: %v", err)
	}
	setConfig(monitorCfg(true))
	for _, tc := range []struct{ name, value string }{
		{action.MonitorBrightness, "101"}, {action.MonitorBrightness, "x"},
		{action.MonitorInput, "hdmi9"}, {action.MonitorPower, "standby"},
	} {
		var ve *action.ValueError
		if _, err := runMonitorCommand(context.Background(), tc.name, tc.value); !errors.As(err, &ve) {
			t.Errorf("%s %q: %v", tc.name, tc.value, err)
		}
	}
	// The primary without DDC/CI.
	seedMonitors(t, []useraction.Monitor{{ID: "m1", Name: "TV", Primary: true, Inputs: []string{}, Power: "unknown"}})
	if _, err := runMonitorCommand(context.Background(), action.MonitorPower, "off"); !errors.Is(err, action.ErrMonitorUnsupported) {
		t.Errorf("no ddc: %v", err)
	}
	if n := len(calls.all()); n != 0 {
		t.Errorf("%d runs", n)
	}
}

func TestMonitorCommandReadsFirstWhenNothingIsStored(t *testing.T) {
	stSetup(t, monitorCfg(true))
	monitors.reset()
	t.Cleanup(monitors.reset)
	get, _ := json.Marshal(map[string]any{"ok": true, "monitors": goldenMonitorList()})
	calls := fakeMonitorRun(t, func(args []string) (string, error) {
		if args[1] == "get" {
			return string(get), nil
		}
		return dellSetReply, nil
	})
	if _, err := runMonitorCommand(context.Background(), action.MonitorInput, "HDMI 2"); err != nil {
		t.Fatal(err)
	}
	got := calls.all()
	if len(got) != 2 || got[0][1] != "get" || strings.Join(got[1], " ") != "monitor set --id m1 --input hdmi2" {
		t.Errorf("ran %q", got)
	}
}

func TestMonitorReadingWithNobodyAtTheConsoleEmptiesTheStore(t *testing.T) {
	stSetup(t, monitorCfg(true))
	seedMonitors(t, goldenMonitorList())
	fakeMonitorRun(t, func([]string) (string, error) {
		return "", errors.New("find console: " + errNoConsoleSession.Error())
	})
	// A plain error keeps the reading.
	if _, err := readMonitors(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if list, _ := monitors.last(); len(list) != 2 {
		t.Errorf("store = %+v", list)
	}
	fakeMonitorRun(t, func([]string) (string, error) { return "", errNoConsoleSession })
	_, err := readMonitors(context.Background())
	if f := action.ClassifyMonitor(err); f.Code != "no_user_session" || f.Status != http.StatusConflict {
		t.Errorf("classified as %+v", f)
	}
	if list, ok := monitors.last(); !ok || len(list) != 0 {
		t.Errorf("store = %+v", list)
	}
}

func TestMonitorReadingDropsInvalidEntries(t *testing.T) {
	res, err := session.ParseOutput([]byte(`{"ok":true,"monitors":[{"id":"m1","name":"A","primary":true,"ddc":true,"brightness":140,"input":null,"inputs":[],"power":"on"},{"id":"m2","name":"B","primary":false,"ddc":false,"brightness":null,"input":null,"inputs":[],"power":"unknown"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	list, err := monitorsFromReply(res)
	if err != nil || len(list) != 1 || list[0].ID != "m2" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestMonitorStatusBlock(t *testing.T) {
	stSetup(t, monitorCfg(false))
	seedMonitors(t, goldenMonitorList())
	st := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s", ""))
	if _, ok := st["monitors"]; ok {
		t.Error("monitors reported while monitor.enabled is off")
	}
	if slices.Contains(toStrings(st["features"]), "monitor") {
		t.Error("monitor feature while off")
	}

	setConfig(monitorCfg(true))
	st = stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s", ""))
	list, _ := st["monitors"].([]any)
	if len(list) != 2 || !slices.Contains(toStrings(st["features"]), "monitor") {
		t.Errorf("monitors = %v, features = %v", st["monitors"], st["features"])
	}

	// Enabled before any reading: [] and a reading starts.
	monitors.reset()
	started := make(chan struct{}, 1)
	fakeMonitorRun(t, func([]string) (string, error) {
		started <- struct{}{}
		return `{"ok":true,"monitors":[]}`, nil
	})
	w := stDo(t, "GET", "/st/v1/status", "192.168.1.20", "s", "")
	if !strings.Contains(w.Body.String(), `"monitors":[]`) {
		t.Errorf("before a reading: %s", w.Body.String())
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no reading started")
	}
	waitMonitorGate(t)
}

// waitMonitorGate waits for a background reading to end.
func waitMonitorGate(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if monitors.gate.Begin(time.Now(), time.Hour, false) {
			monitors.gate.End(time.Now())
			return
		}
		if _, ok := monitors.last(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the monitor reading did not end")
}

func toStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestSTMonitorCommands(t *testing.T) {
	stSetup(t, monitorCfg(true))
	seedMonitors(t, goldenMonitorList())
	calls := fakeMonitorRun(t, func([]string) (string, error) { return dellSetReply, nil })

	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"command":"monitor_brightness","value":70}`, 200, `"brightness":70`},
		{`{"command":"Monitor_Input","value":"hdmi1"}`, 200, `"executed":true`},
		{`{"command":"monitor_power","value":"off"}`, 200, `"monitor":{`},
		{`{"command":"monitor_brightness","value":"70"}`, 400, `value must be between 0 and 100`},
		{`{"command":"monitor_brightness"}`, 400, `needs a value`},
		{`{"command":"monitor_input","value":7}`, 400, `input name`},
		{`{"command":"monitor_power","value":"on","minutes":5}`, 400, `minutes does not apply`},
		// The other commands still take their numeric value.
		{`{"command":"bogus","value":"x"}`, 400, `invalid JSON`},
	} {
		w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "s", tc.body)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	want := []string{"--brightness 70", "--input hdmi1", "--power off"}
	got := calls.all()
	if len(got) != len(want) {
		t.Fatalf("ran %q", got)
	}
	for i := range want {
		if strings.Join(got[i][4:], " ") != want[i] {
			t.Errorf("run %d: %q", i, got[i])
		}
	}
}

func TestSTMonitorCommandFailures(t *testing.T) {
	stSetup(t, monitorCfg(true))
	seedMonitors(t, goldenMonitorList())
	for _, tc := range []struct {
		reply string
		err   error
		code  int
		want  string
	}{
		{`{"ok":false,"error":"failed","message":"the monitor did not take power on"}`, nil, 502, `"error":"monitor_failed","message":"the monitor did not take power on"`},
		{`{"ok":false,"error":"unsupported","message":"monitor m1 not found"}`, nil, 501, `"error":"monitor_unsupported"`},
		{"", errNoConsoleSession, 409, `{"error":"no_user_session"}`},
		{"", session.ErrTimeout, 504, `{"error":"timeout"}`},
	} {
		fakeMonitorRun(t, func([]string) (string, error) { return tc.reply, tc.err })
		w := stDo(t, "POST", "/st/v1/command", "192.168.1.20", "s", `{"command":"monitor_power","value":"on"}`)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %v: %d %s", tc.reply, tc.err, w.Code, w.Body.String())
		}
	}
}

func TestAPIMonitor(t *testing.T) {
	cfg := monitorCfg(true)
	withLiveConfig(t, cfg)
	seedMonitors(t, goldenMonitorList())
	fakeMonitorRun(t, func([]string) (string, error) { return dellSetReply, nil })

	do := func(method, body string) *httptest.ResponseRecorder {
		r := asLocalApp(t, httptest.NewRequest(method, "/api/monitor", nil))
		if body != "" {
			r = localPostJSON(t, "/api/monitor", body)
		}
		w := httptest.NewRecorder()
		webAPI(w, r)
		return w
	}
	w := do("GET", "")
	var st struct {
		Enabled  bool
		Monitors []useraction.Monitor
	}
	if json.Unmarshal(w.Body.Bytes(), &st) != nil || !st.Enabled || len(st.Monitors) != 2 {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	w = do("POST", `{"command":"monitor_brightness","value":70}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"monitor":{"id":"m1"`) {
		t.Errorf("POST: %d %s", w.Code, w.Body.String())
	}
	w = do("POST", `{"command":"volume","value":70}`)
	if w.Code != 400 {
		t.Errorf("non-monitor command: %d %s", w.Code, w.Body.String())
	}
	cfg.Monitor.Enabled = false
	setConfig(cfg)
	w = do("POST", `{"command":"monitor_power","value":"off"}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "monitor_disabled") {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}
	w = do("GET", "")
	if !strings.Contains(w.Body.String(), `"enabled":false,"monitors":[]`) {
		t.Errorf("GET disabled: %s", w.Body.String())
	}
}
