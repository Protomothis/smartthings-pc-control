package tgcontrol

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// fakeMonitor records the commands and answers with m or err.
type fakeMonitor struct {
	list []status.Monitor
	m    status.Monitor
	err  error
	runs []string
}

func (f *fakeMonitor) Monitors(cfg config.Config, _ bool) *[]status.Monitor {
	if !cfg.Monitor.Enabled {
		return nil
	}
	l := append([]status.Monitor(nil), f.list...)
	return &l
}

func (f *fakeMonitor) Run(_ context.Context, name, value string) (status.Monitor, error) {
	f.runs = append(f.runs, name+" "+value)
	if f.err != nil {
		return status.Monitor{}, f.err
	}
	if _, err := action.MonitorArgs(name, value); err != nil {
		return status.Monitor{}, err
	}
	return f.m, nil
}

func (f *fakeMonitor) Read(context.Context) ([]status.Monitor, error) {
	return f.list, f.err
}

func ip(n int) *int       { return &n }
func sp(s string) *string { return &s }
func monitorOn() config.Config {
	return config.Config{Monitor: config.MonitorConfig{Enabled: true}, Telegram: config.TelegramConfig{Lang: "ko"}}
}

var dell = status.Monitor{ID: "m1", Name: "DELL <U2720Q>", Primary: true, DDC: true, Brightness: ip(40),
	Input: sp("hdmi1"), Inputs: []string{"dp1", "hdmi1", "vcp:0x1b"}, Power: "on"}

func withMonitor(t *testing.T, cfg config.Config, f *fakeMonitor) *testControl {
	t.Helper()
	c := newTestControl(t, cfg)
	c.d.Monitor = f
	return c
}

func TestTelegramMonitorCommands(t *testing.T) {
	ctx := context.Background()
	f := &fakeMonitor{list: []status.Monitor{{ID: "m2", Inputs: []string{}, Power: "unknown"}, dell}, m: dell}
	c := withMonitor(t, monitorOn(), f)

	for _, tc := range []struct {
		cmd  string
		args []string
		want string
	}{
		{"brightness", nil, "🔆 밝기 40% · DELL &lt;U2720Q&gt;"},
		{"brightness", []string{"40"}, "🔆 밝기 40% · DELL &lt;U2720Q&gt;"},
		{"brightness", []string{"140"}, "사용법: <code>/brightness</code>"},
		{"input", nil, "🖥 입력: <code>dp1</code> · <code>hdmi1</code> (현재) · <code>vcp:0x1b</code> · DELL &lt;U2720Q&gt;\n<code>/input 이름</code>으로 바꿉니다"},
		{"input", []string{"HDMI", "1"}, "🖥 입력: HDMI1 · DELL"},
		{"input", []string{"hdmi9"}, "사용법: <code>/input</code>"},
		{"monitor", []string{"OFF"}, "🌑 모니터를 껐습니다"},
		{"monitor", []string{"on"}, "💡 모니터를 켰습니다"},
		{"monitor", nil, "사용법: <code>/monitor on</code>"},
		{"monitor", []string{"toggle"}, "사용법: <code>/monitor on</code>"},
	} {
		got, _, _ := c.handleCommand(ctx, "1", tc.cmd, tc.args)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("/%s %v = %q, want prefix %q", tc.cmd, tc.args, got, tc.want)
		}
	}
	want := []string{"monitor_brightness 40", "monitor_brightness 140", "monitor_input HDMI 1", "monitor_input hdmi9", "monitor_power off", "monitor_power on"}
	if strings.Join(f.runs, ",") != strings.Join(want, ",") {
		t.Errorf("runs = %q", f.runs)
	}
}

func TestTelegramMonitorErrors(t *testing.T) {
	ctx := context.Background()
	c := withMonitor(t, config.Config{}, &fakeMonitor{err: action.ErrMonitorDisabled})
	for _, cmd := range []string{"brightness", "input"} {
		if got, _, err := c.handleCommand(ctx, "1", cmd, nil); !strings.Contains(got, "모니터 제어가 꺼져") || err != nil {
			t.Errorf("/%s while off = %q, %v", cmd, got, err)
		}
	}
	for _, tc := range []struct {
		err    error
		want   string
		logged bool
	}{
		{action.ErrMonitorDisabled, "모니터 제어가 꺼져", false},
		{session.ErrNoConsoleSession, "로그인한 사용자가 없어", false},
		{session.ErrTimeout, "제때 답하지", true},
		{action.ErrMonitorUnsupported, "DDC/CI로 제어할 수 없습니다", true},
		{&session.ActionError{Code: "failed", Message: "the monitor did not take power on"}, "모니터 제어 실패: the monitor did not take power on", true},
		{errors.New("start: C:\\secret\\path"), "모니터 제어 실패: the action could not be run", true},
	} {
		c := withMonitor(t, monitorOn(), &fakeMonitor{err: tc.err})
		got, _, err := c.handleCommand(ctx, "1", "monitor", []string{"on"})
		if !strings.Contains(got, tc.want) || (err != nil) != tc.logged {
			t.Errorf("%v: %q, %v", tc.err, got, err)
		}
	}
	// No monitor control at all (an older wiring): off.
	c = newTestControl(t, monitorOn())
	if got, _, _ := c.handleCommand(ctx, "1", "monitor", []string{"off"}); !strings.Contains(got, "모니터 제어가 꺼져") {
		t.Errorf("nil monitor = %q", got)
	}
}

func TestTelegramMonitorStatusLine(t *testing.T) {
	f := &fakeMonitor{list: []status.Monitor{dell}}
	c := withMonitor(t, monitorOn(), f)
	if got := c.monitorLine(c.d.Config()); got != "모니터: 밝기 40% · HDMI1" {
		t.Errorf("line = %q", got)
	}
	if !strings.Contains(c.statusText(), "\n모니터: 밝기 40% · HDMI1") {
		t.Errorf("/status:\n%s", c.statusText())
	}
	standby := dell
	standby.Power = "standby"
	f.list = []status.Monitor{standby}
	c.setConfig(config.Config{Monitor: config.MonitorConfig{Enabled: true}, Telegram: config.TelegramConfig{Lang: "en"}})
	if got := c.monitorLine(c.d.Config()); got != "Monitor: standby · brightness 40% · HDMI1" {
		t.Errorf("en line = %q", got)
	}
	f.list = []status.Monitor{{ID: "m1", Primary: true, Inputs: []string{}, Power: "unknown"}}
	if got := c.monitorLine(c.d.Config()); got != "" {
		t.Errorf("no ddc: %q", got)
	}
	c.setConfig(config.Config{})
	f.list = []status.Monitor{dell}
	if got := c.monitorLine(c.d.Config()); got != "" {
		t.Errorf("off: %q", got)
	}
}
