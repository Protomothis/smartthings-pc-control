package action

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

func TestMonitorArgs(t *testing.T) {
	ok := map[[2]string]string{
		{MonitorBrightness, "0"}:        "--brightness 0",
		{MonitorBrightness, " 40% "}:    "--brightness 40",
		{MonitorInput, "hdmi1"}:         "--input hdmi1",
		{MonitorInput, "HDMI 2"}:        "--input hdmi2",
		{MonitorInput, "DisplayPort-1"}: "--input dp1",
		{MonitorInput, "VCP:0x1B"}:      "--input vcp:0x1b",
		{MonitorPower, "OFF"}:           "--power off",
		{MonitorPower, "on"}:            "--power on",
	}
	for in, want := range ok {
		got, err := MonitorArgs(in[0], in[1])
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("MonitorArgs(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range [][2]string{
		{MonitorBrightness, "101"}, {MonitorBrightness, "-1"}, {MonitorBrightness, ""},
		{MonitorInput, "hdmi3"}, {MonitorInput, "vcp:0x11"}, {MonitorInput, ""},
		{MonitorPower, "standby"}, {"volume", "1"},
	} {
		var ve *ValueError
		if _, err := MonitorArgs(in[0], in[1]); !errors.As(err, &ve) {
			t.Errorf("MonitorArgs(%q) = %v", in, err)
		}
	}
}

func TestMonitorValueText(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		ok              bool
	}{
		{MonitorBrightness, `40`, "40", true},
		{MonitorBrightness, `"40"`, "", false},
		{MonitorBrightness, `40.5`, "", false},
		{MonitorInput, `"hdmi1"`, "hdmi1", true},
		{MonitorInput, `1`, "", false},
		{MonitorPower, `"off"`, "off", true},
		{MonitorPower, `true`, "", false},
		{MonitorPower, `null`, "", false},
		{MonitorPower, ``, "", false},
	} {
		got, err := MonitorValueText(tc.name, json.RawMessage(tc.raw))
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("MonitorValueText(%s, %s) = %q, %v", tc.name, tc.raw, got, err)
		}
	}
}

func TestClassifyMonitor(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrMonitorDisabled, 403, "monitor_disabled"},
		{ErrMonitorUnsupported, 501, "monitor_unsupported"},
		{session.ErrNoConsoleSession, 409, "no_user_session"},
		{session.ErrNoUserSession, 409, "no_user_session"},
		{session.ErrTimeout, 504, "timeout"},
		{&ValueError{"value must be on or off"}, 400, "value must be on or off"},
		{&session.ActionError{Code: "unsupported", Message: "m3 not found"}, 501, "monitor_unsupported"},
		{&session.ActionError{Code: "failed", Message: "refused"}, 502, "monitor_failed"},
		{&session.ActionError{Code: "bad_args", Message: "x"}, 400, "bad_args"},
		{errors.New("start failed"), 502, "monitor_failed"},
	} {
		f := ClassifyMonitor(tc.err)
		if f.Status != tc.status || f.Code != tc.code {
			t.Errorf("%v: %d %s", tc.err, f.Status, f.Code)
		}
	}
}
