package action

// Monitor control over DDC/CI (#144). SmartThings (/st/v1/command), the
// app (/api/monitor) and Telegram share these names, the argument rules
// and one error mapping:
//
//	monitor_brightness  value 0–100        → --brightness N
//	monitor_input       value "hdmi1", …   → --input NAME
//	monitor_power       value "on" | "off" → --power on|off
//
// Every command acts on the primary monitor.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// ErrMonitorDisabled is returned while monitor.enabled is off: 403
// monitor_disabled.
var ErrMonitorDisabled = errors.New("monitor_disabled")

// ErrMonitorUnsupported is returned when the primary monitor does not
// answer DDC/CI, or there is none: 501 monitor_unsupported.
var ErrMonitorUnsupported = errors.New("monitor_unsupported")

// Monitor command names.
const (
	MonitorBrightness = "monitor_brightness"
	MonitorInput      = "monitor_input"
	MonitorPower      = "monitor_power"
)

// IsMonitor reports whether name is a monitor command.
func IsMonitor(name string) bool {
	switch name {
	case MonitorBrightness, MonitorInput, MonitorPower:
		return true
	}
	return false
}

// MonitorArgs checks a command's value and returns the `monitor set` flag
// that carries it (without --id). value is the text form: "40", "hdmi1",
// "off".
func MonitorArgs(name, value string) ([]string, error) {
	value = strings.TrimSpace(value)
	switch name {
	case MonitorBrightness:
		n, err := strconv.Atoi(strings.TrimSuffix(value, "%"))
		if err != nil || n < 0 || n > 100 {
			return nil, &ValueError{"value must be between 0 and 100"}
		}
		return []string{"--brightness", strconv.Itoa(n)}, nil
	case MonitorInput:
		in := NormalizeMonitorInput(value)
		if _, ok := useraction.MonitorInputCode(in); !ok {
			return nil, &ValueError{"value must be an input name such as hdmi1, dp1 or vcp:0x1b"}
		}
		return []string{"--input", in}, nil
	case MonitorPower:
		switch v := strings.ToLower(value); v {
		case "on", "off":
			return []string{"--power", v}, nil
		}
		return nil, &ValueError{"value must be on or off"}
	}
	return nil, &ValueError{"not a monitor command"}
}

// NormalizeMonitorInput accepts what a person types for an input —
// "HDMI1", "hdmi 1", "DisplayPort-1", "VCP:0x1B" — and returns the wire
// name (hdmi1, dp1, vcp:0x1b). It does not check it.
func NormalizeMonitorInput(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "vcp:") {
		return strings.ReplaceAll(s, " ", "")
	}
	s = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)
	return strings.Replace(s, "displayport", "dp", 1)
}

// MonitorValueText reads a JSON command value: a number for the
// brightness, a string for the input and the power. Anything else is a
// ValueError, as is a missing value.
func MonitorValueText(name string, raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", &ValueError{name + " needs a value"}
	}
	if name == MonitorBrightness {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return "", &ValueError{"value must be between 0 and 100"}
		}
		return strconv.Itoa(n), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		if name == MonitorPower {
			return "", &ValueError{"value must be on or off"}
		}
		return "", &ValueError{"value must be an input name such as hdmi1, dp1 or vcp:0x1b"}
	}
	return s, nil
}

// ClassifyMonitor maps a monitor command's error to its answer. It is
// Classify with the monitor's own codes: monitor_disabled,
// monitor_unsupported (the monitor does not do DDC/CI, or not this) and
// monitor_failed (it was asked and refused), and no_user_session also when
// nobody is logged in at the console the monitors hang off.
func ClassifyMonitor(err error) Failure {
	var ue *session.ActionError
	switch {
	case errors.Is(err, ErrMonitorDisabled):
		return Failure{Status: http.StatusForbidden, Code: "monitor_disabled", Message: "monitor control is turned off in the app settings"}
	case errors.Is(err, ErrMonitorUnsupported):
		msg := "the primary monitor does not answer DDC/CI"
		return Failure{Status: http.StatusNotImplemented, Code: "monitor_unsupported", Message: msg, Detail: msg}
	case errors.Is(err, session.ErrNoConsoleSession):
		return Failure{Status: http.StatusConflict, Code: "no_user_session", Message: "nobody is logged in at this PC's screen"}
	case errors.As(err, &ue) && ue.Code == useraction.CodeUnsupported:
		return Failure{Status: http.StatusNotImplemented, Code: "monitor_unsupported", Message: ue.Message, Detail: ue.Message}
	case errors.As(err, &ue) && ue.Code == useraction.CodeFailed:
		return Failure{Status: http.StatusBadGateway, Code: "monitor_failed", Message: ue.Message, Detail: ue.Message}
	}
	f := Classify(err)
	if f.Code == useraction.CodeFailed {
		f.Code = "monitor_failed"
	}
	return f
}
