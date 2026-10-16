package tgcontrol

// Telegram monitor commands over DDC/CI (#144):
//
//	/brightness        the primary monitor's brightness (the last reading)
//	/brightness 40     set it (0–100)
//	/input             the inputs, read now: "HDMI1 (현재) · DP1 · HDMI2"
//	/input hdmi1       switch to it
//	/monitor on|off    turn the monitor on or off (DPM)
//
// They follow monitor.enabled, act on the primary monitor and raise no
// notification. /status gets a "모니터: 밝기 40% · HDMI1" line.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Monitor is the DDC/CI monitor control (#144).
type Monitor interface {
	// Monitors is the stored reading, nil while monitor.enabled is off.
	Monitors(cfg config.Config, fromCache bool) *[]status.Monitor
	// Run sets one value on the primary monitor (action.MonitorArgs).
	Run(ctx context.Context, name, value string) (status.Monitor, error)
	// Read reads the monitors now.
	Read(ctx context.Context) ([]status.Monitor, error)
}

var monitorTexts = map[string][2]string{
	"mon_disabled":     {"모니터 제어가 꺼져 있습니다. PC 앱 설정 탭(미디어·알림)에서 '원격 모니터 제어 허용'을 켜세요", "Monitor control is off. Turn on 'Allow remote monitor control' in the PC app's Settings tab (Media &amp; notifications)"},
	"mon_no_user":      {"PC 화면(콘솔)에 로그인한 사용자가 없어 모니터를 제어할 수 없습니다", "Nobody is logged in at this PC's screen, so the monitor cannot be controlled"},
	"mon_unsupported":  {"이 모니터는 DDC/CI로 제어할 수 없습니다 (모니터 메뉴에서 DDC/CI를 켜 보세요): %s", "This monitor cannot be controlled over DDC/CI (try turning DDC/CI on in its menu): %s"},
	"mon_failed":       {"모니터 제어 실패: %s", "Monitor control failed: %s"},
	"mon_timeout":      {"PC가 제때 답하지 않았습니다 (모니터가 DDC/CI에 응답하지 않음)", "The PC did not answer in time (the monitor did not answer DDC/CI)"},
	"mon_none":         {"모니터 정보를 아직 읽지 못했습니다. 잠시 후 다시 보내 주세요", "The monitor has not been read yet. Try again in a moment"},
	"mon_brightness":   {"🔆 밝기 %d%%", "🔆 Brightness %d%%"},
	"mon_input_set":    {"🖥 입력: %s", "🖥 Input: %s"},
	"mon_inputs":       {"🖥 입력: %s", "🖥 Inputs: %s"},
	"mon_current":      {"(현재)", "(current)"},
	"mon_input_hint":   {"<code>/input 이름</code>으로 바꿉니다", "Switch with <code>/input name</code>"},
	"mon_on":           {"💡 모니터를 켰습니다", "💡 Monitor turned on"},
	"mon_off":          {"🌑 모니터를 껐습니다 — 기종에 따라 DDC로는 다시 켜지지 않을 수 있습니다", "🌑 Monitor turned off — some monitors cannot be turned back on over DDC"},
	"mon_standby":      {"대기", "standby"},
	"st_monitor":       {"모니터", "Monitor"},
	"st_brightness":    {"밝기", "brightness"},
	"brightness_usage": {"사용법: <code>/brightness</code> (현재 밝기), <code>/brightness 40</code> (0–100)", "Usage: <code>/brightness</code> (current brightness), <code>/brightness 40</code> (0–100)"},
	"input_usage":      {"사용법: <code>/input</code> (입력 목록), <code>/input hdmi1</code> (hdmi1, dp1, vcp:0x1b …)", "Usage: <code>/input</code> (the inputs), <code>/input hdmi1</code> (hdmi1, dp1, vcp:0x1b …)"},
	"monitor_usage":    {"사용법: <code>/monitor on</code> 또는 <code>/monitor off</code>", "Usage: <code>/monitor on</code> or <code>/monitor off</code>"},
}

func init() {
	for k, v := range monitorTexts {
		if _, dup := texts[k]; dup {
			panic("tgcontrol: monitor text " + k + " already defined")
		}
		texts[k] = v
	}
}

// primaryOf picks the primary monitor of a reading.
func primaryOf(list []status.Monitor) (status.Monitor, bool) {
	for _, m := range list {
		if m.Primary {
			return m, true
		}
	}
	if len(list) > 0 {
		return list[0], true
	}
	return status.Monitor{}, false
}

// storedPrimary is the primary of the stored reading.
func (c *Control) storedPrimary() (status.Monitor, bool) {
	if c.d.Monitor == nil {
		return status.Monitor{}, false
	}
	list := c.d.Monitor.Monitors(c.d.Config(), false)
	if list == nil {
		return status.Monitor{}, false
	}
	return primaryOf(*list)
}

// monitorName is " · DELL U2720Q", or "" for a monitor without a name.
func monitorName(m status.Monitor) string {
	if m.Name == "" {
		return ""
	}
	return " · " + html.EscapeString(m.Name)
}

// brightness handles /brightness [0-100].
func (c *Control) brightness(ctx context.Context, args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) == 0 {
		if !c.d.Config().Monitor.Enabled || c.d.Monitor == nil {
			return c.text("mon_disabled"), nil, nil
		}
		m, ok := c.storedPrimary()
		if !ok || m.Brightness == nil {
			return c.text("mon_none") + "\n" + c.text("brightness_usage"), nil, nil
		}
		return c.text("mon_brightness", *m.Brightness) + monitorName(m), nil, nil
	}
	m, err := c.runMonitor(ctx, action.MonitorBrightness, args[0])
	if err != nil {
		return c.monitorError(action.MonitorBrightness, err)
	}
	if m.Brightness == nil {
		return c.text("executed", c.text("st_monitor")), nil, nil
	}
	return c.text("mon_brightness", *m.Brightness) + monitorName(m), nil, nil
}

// input handles /input [name]: without one, the inputs read now.
func (c *Control) input(ctx context.Context, args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) == 0 {
		if !c.d.Config().Monitor.Enabled || c.d.Monitor == nil {
			return c.text("mon_disabled"), nil, nil
		}
		list, err := c.d.Monitor.Read(ctx)
		if err != nil {
			return c.monitorError(action.MonitorInput, err)
		}
		m, ok := primaryOf(list)
		if !ok {
			return c.monitorError(action.MonitorInput, action.ErrMonitorUnsupported)
		}
		return c.inputList(m), nil, nil
	}
	m, err := c.runMonitor(ctx, action.MonitorInput, strings.Join(args, " "))
	if err != nil {
		return c.monitorError(action.MonitorInput, err)
	}
	in := ""
	if m.Input != nil {
		in = useraction.MonitorInputLabel(*m.Input)
	}
	return c.text("mon_input_set", html.EscapeString(in)) + monitorName(m), nil, nil
}

// inputList is "🖥 입력: HDMI1 (현재) · DP1 · HDMI2" and how to switch.
func (c *Control) inputList(m status.Monitor) string {
	if len(m.Inputs) == 0 {
		return c.text("mon_none") + "\n" + c.text("input_usage")
	}
	parts := make([]string, 0, len(m.Inputs))
	for _, in := range m.Inputs {
		p := "<code>" + html.EscapeString(in) + "</code>"
		if m.Input != nil && *m.Input == in {
			p += " " + c.text("mon_current")
		}
		parts = append(parts, p)
	}
	return c.text("mon_inputs", strings.Join(parts, " · ")) + monitorName(m) + "\n" + c.text("mon_input_hint")
}

// monitorPower handles /monitor on|off.
func (c *Control) monitorPower(ctx context.Context, args []string) (string, *telegram.InlineKeyboard, error) {
	if len(args) != 1 {
		return c.text("monitor_usage"), nil, nil
	}
	arg := strings.ToLower(args[0])
	if arg != "on" && arg != "off" {
		return c.text("monitor_usage"), nil, nil
	}
	if _, err := c.runMonitor(ctx, action.MonitorPower, arg); err != nil {
		return c.monitorError(action.MonitorPower, err)
	}
	return c.text("mon_" + arg), nil, nil
}

// runMonitor runs one monitor command, or says monitor control is off.
func (c *Control) runMonitor(ctx context.Context, name, value string) (status.Monitor, error) {
	if c.d.Monitor == nil {
		return status.Monitor{}, action.ErrMonitorDisabled
	}
	m, err := c.d.Monitor.Run(ctx, name, value)
	if err == nil {
		logx.Printf("Telegram: %s", name)
	}
	return m, err
}

// monitorError words a failed monitor command. Like mediaError, only real
// failures are returned as errors.
func (c *Control) monitorError(name string, err error) (string, *telegram.InlineKeyboard, error) {
	var ve *action.ValueError
	if errors.As(err, &ve) {
		return c.text(strings.TrimPrefix(name, "monitor_") + "_usage"), nil, nil
	}
	f := action.ClassifyMonitor(err)
	detail := html.EscapeString(f.Message)
	switch f.Code {
	case "monitor_disabled":
		return c.text("mon_disabled"), nil, nil
	case "no_user_session":
		return c.text("mon_no_user"), nil, nil
	case "timeout":
		return c.text("mon_timeout"), nil, err
	case "monitor_unsupported":
		return c.text("mon_unsupported", detail), nil, err
	}
	return c.text("mon_failed", detail), nil, err
}

// monitorLine is the /status "모니터: 밝기 40% · HDMI1" line of the
// primary monitor's last reading, "" while monitor control is off or
// nothing is known.
func (c *Control) monitorLine(cfg config.Config) string {
	if !cfg.Monitor.Enabled || c.d.Monitor == nil {
		return ""
	}
	list := c.d.Monitor.Monitors(cfg, false)
	if list == nil {
		return ""
	}
	m, ok := primaryOf(*list)
	if !ok || !m.DDC {
		return ""
	}
	var parts []string
	if m.Power == useraction.MonitorPowerStandby {
		parts = append(parts, c.text("mon_standby"))
	}
	if m.Brightness != nil {
		parts = append(parts, fmt.Sprintf("%s %d%%", c.text("st_brightness"), *m.Brightness))
	}
	if m.Input != nil {
		parts = append(parts, html.EscapeString(useraction.MonitorInputLabel(*m.Input)))
	}
	if len(parts) == 0 {
		return ""
	}
	return c.text("st_monitor") + ": " + strings.Join(parts, " · ")
}
