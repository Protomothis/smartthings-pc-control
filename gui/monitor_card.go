package gui

// The command tab's monitor card (#144):
//
//	모니터
//	밝기  ─────────●──────────  40%
//	입력  [HDMI1 ▼]
//	전원  [켜기] [끄기]
//	      DELL U2720Q
//	      설정 탭에서 '원격 모니터 제어 허용'을 켜면 …   (only when unusable)
//
// Everything goes through the service (/api/monitor), like SmartThings and
// Telegram: the same monitor.enabled switch, the same primary monitor, the
// same stored reading. The card polls every awakePollInterval while the
// window is on screen; the service itself reads the monitors at most once
// a minute, so the poll is cheap.

import (
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// monitorView is the card and the last state it showed; the state survives
// a rebuild (language change) so the card comes back at once.
type monitorView struct {
	card    *monitorCard
	last    MonitorState
	lastErr error
	loaded  bool
}

// monitorCard is the card's widgets.
type monitorCard struct {
	slider *widget.Slider
	level  *widget.Label
	input  *widget.Select
	on     *widget.Button
	off    *widget.Button
	name   *widget.Label
	reason *widget.Label
	// inputs are the wire names behind the select's options.
	inputs []string

	state MonitorState
	known bool
	// syncing is set while the widgets follow the service; dragging while
	// the slider is held, so a poll does not yank it away.
	syncing  bool
	dragging bool
	cmdErr   string
	cmdErrAt time.Time
}

// monitorPollBusy keeps a slow poll from piling up behind the ticker.
var monitorPollBusy atomic.Bool

// monitorLevelText is the brightness next to the slider, "—" while unknown.
func monitorLevelText(m MonitorInfo) string {
	if m.Brightness == nil {
		return "—"
	}
	return strconv.Itoa(*m.Brightness) + "%"
}

// monitorReason is the one line that says why the card cannot be used, or
// "".
func (u *ui) monitorReason(s MonitorState, err error) string {
	switch {
	case errors.Is(err, errMonitorUnsupported):
		return u.t("monitor.reason.old")
	case err != nil:
		return ""
	case !s.Enabled:
		return u.t("monitor.reason.disabled")
	}
	m, ok := s.Primary()
	switch {
	case !ok:
		return u.t("monitor.reason.none")
	case !m.DDC:
		return u.t("monitor.reason.noddc")
	}
	return ""
}

// monitorErrorText words a failed command for the card.
func (u *ui) monitorErrorText(err error) string {
	var ae *actionError
	if !errors.As(err, &ae) {
		return fmt.Sprintf(u.t("monitor.err.failed"), err)
	}
	switch ae.Code {
	case "monitor_disabled":
		return u.t("monitor.reason.disabled")
	case "no_user_session":
		return u.t("monitor.reason.nouser")
	case "timeout":
		return u.t("monitor.err.timeout")
	case "monitor_unsupported":
		return fmt.Sprintf(u.t("monitor.err.unsupported"), ae.Message)
	case "monitor_failed":
		return fmt.Sprintf(u.t("monitor.err.failed"), ae.Message)
	}
	if ae.Message != "" {
		return fmt.Sprintf(u.t("monitor.err.failed"), ae.Message)
	}
	return fmt.Sprintf(u.t("monitor.err.failed"), ae.Code)
}

// monitorNameText is "DELL U2720Q" or "DELL U2720Q · 대기".
func (u *ui) monitorNameText(m MonitorInfo) string {
	if m.Power == "standby" {
		if m.Name == "" {
			return u.t("monitor.standby")
		}
		return m.Name + " · " + u.t("monitor.standby")
	}
	return m.Name
}

// buildMonitorCard creates the card for the command tab.
func (u *ui) buildMonitorCard() fyne.CanvasObject {
	c := &monitorCard{}
	u.mon.card = c

	c.slider = widget.NewSlider(0, 100)
	c.slider.Step = 1
	c.slider.OnChanged = func(v float64) {
		if c.syncing {
			return
		}
		c.dragging = true
		c.level.SetText(strconv.Itoa(int(v)) + "%")
	}
	// Only the release is sent: one DDC write per gesture.
	c.slider.OnChangeEnded = func(v float64) {
		if c.syncing {
			return
		}
		c.dragging = false
		u.sendMonitorCommand("monitor_brightness", int(v))
	}
	c.level = widget.NewLabel("—")
	c.level.Alignment = fyne.TextAlignTrailing

	c.input = widget.NewSelect(nil, func(string) {
		if c.syncing {
			return
		}
		if i := c.input.SelectedIndex(); i >= 0 && i < len(c.inputs) {
			u.sendMonitorCommand("monitor_input", c.inputs[i])
		}
	})
	c.on = widget.NewButtonWithIcon(u.t("monitor.on"), theme.VisibilityIcon(), func() {
		u.sendMonitorCommand("monitor_power", "on")
	})
	c.off = widget.NewButtonWithIcon(u.t("monitor.off"), theme.VisibilityOffIcon(), func() {
		dialog.ShowConfirm(u.t("cmd.confirm.title"), u.t("monitor.off.confirm"), func(ok bool) {
			if ok {
				u.sendMonitorCommand("monitor_power", "off")
			}
		}, u.win)
	})
	c.name = smallLabel()
	c.name.Truncation = fyne.TextTruncateEllipsis
	c.reason = hint("")
	c.reason.Hide()

	levelBox := container.NewGridWrap(fyne.NewSize(56, c.slider.MinSize().Height), c.level)
	form := widget.NewForm(
		widget.NewFormItem(u.t("monitor.brightness"), container.NewBorder(nil, nil, nil, levelBox, c.slider)),
		widget.NewFormItem(u.t("monitor.input"), container.NewHBox(c.input)),
		widget.NewFormItem(u.t("monitor.power"), container.NewHBox(c.on, c.off)),
	)

	if u.mon.loaded {
		u.applyMonitor(u.mon.last, u.mon.lastErr)
	} else {
		for _, w := range []fyne.Disableable{c.slider, c.input, c.on, c.off} {
			w.Disable()
		}
	}
	return section(u.t("monitor.title"), container.NewVBox(form, c.name, c.reason))
}

// sendMonitorCommand runs one command off the UI thread and shows the
// state the service answers with.
func (u *ui) sendMonitorCommand(command string, value any) {
	c := u.mon.card
	var busy func(bool)
	if c != nil {
		busy = busyControls(c.slider, c.input, c.on, c.off)
	}
	runAsync(busy, func() (MonitorState, error) { return u.client.MonitorCommand(command, value) },
		func(s MonitorState, err error) {
			c := u.mon.card
			if c == nil {
				return
			}
			if err != nil {
				c.cmdErr, c.cmdErrAt = u.monitorErrorText(err), time.Now()
				u.applyMonitor(c.state, nil)
				return
			}
			c.cmdErr = ""
			u.applyMonitor(s, nil)
		})
}

// loadMonitor polls the service. Runs off the UI thread.
func (u *ui) loadMonitor() {
	if !monitorPollBusy.CompareAndSwap(false, true) {
		return
	}
	defer monitorPollBusy.Store(false)
	s, err := u.client.GetMonitors()
	if err != nil && !errors.Is(err, errMonitorUnsupported) {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() { u.applyMonitor(s, err) })
}

// applyMonitor mirrors the state to the card. UI thread only; err is
// errMonitorUnsupported or nil.
func (u *ui) applyMonitor(s MonitorState, err error) {
	u.mon.last, u.mon.lastErr, u.mon.loaded = s, err, true
	c := u.mon.card
	if c == nil {
		return
	}
	c.syncing = true
	defer func() { c.syncing = false }()
	c.state, c.known = s, err == nil

	m, have := s.Primary()
	if !c.dragging {
		c.level.SetText(monitorLevelText(m))
		if m.Brightness != nil {
			c.slider.SetValue(float64(*m.Brightness))
		}
	}
	c.inputs = append([]string(nil), m.Inputs...)
	labels := make([]string, len(c.inputs))
	selected := -1
	for i, in := range c.inputs {
		labels[i] = useraction.MonitorInputLabel(in)
		if m.Input != nil && *m.Input == in {
			selected = i
		}
	}
	c.input.SetOptions(labels)
	if selected >= 0 {
		c.input.SetSelectedIndex(selected)
	} else {
		c.input.ClearSelected()
	}
	c.name.SetText(u.monitorNameText(m))

	reason := u.monitorReason(s, err)
	if reason == "" && c.cmdErr != "" && time.Since(c.cmdErrAt) < mediaErrorShown {
		reason = c.cmdErr
	}
	if reason != "" {
		c.reason.SetText(reason)
		c.reason.Show()
	} else {
		c.reason.Hide()
	}

	usable := c.known && s.Enabled && have && m.DDC
	setEnabled(c.slider, usable && m.Brightness != nil)
	setEnabled(c.input, usable && len(c.inputs) > 0)
	setEnabled(c.on, usable)
	setEnabled(c.off, usable)
}
