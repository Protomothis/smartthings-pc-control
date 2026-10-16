package tgcontrol

// The bot's side of the camera/microphone detection (#143): the /status
// line, and the guard's answer when a power command from the bot meets a
// device in use — refused under "block", run with a warning under "warn".

import (
	"strings"

	"github.com/Protomothis/smartthings-pc-control/service/privacy"
)

// privacyTexts join texts at start-up; they live here so the feature's
// wording stays in one file.
var privacyTexts = map[string][2]string{
	"st_privacy":            {"카메라·마이크", "Camera/mic"},
	"privacy_camera":        {"카메라", "camera"},
	"privacy_microphone":    {"마이크", "microphone"},
	"privacy_in_use":        {"사용 중", "in use"},
	"privacy_idle":          {"쉬는 중", "idle"},
	"privacy_unknown":       {"알 수 없음", "unknown"},
	"privacy_blocked":       {"⛔ %s 사용 중이라 %s 명령을 실행하지 않았습니다 (PC 앱 설정: 사용 중 종료 막기)", "⛔ %s in use — %s was not run (PC app setting: block while in use)"},
	"privacy_blocked_toast": {"사용 중이라 실행 안 함", "In use — not run"},
	"privacy_warn":          {"⚠️ %s 사용 중", "⚠️ %s in use"},
	"by_device_in_use":      {"카메라·마이크 사용 중", "camera/mic in use"},
}

func init() {
	for k, v := range privacyTexts {
		texts[k] = v
	}
}

// privacyDevices names the devices in the bot's language: "카메라·마이크".
func (c *Control) privacyDevices(devs []string) string {
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		names = append(names, c.text("privacy_"+d))
	}
	sep := "·"
	if c.d.Config().Telegram.Lang == "en" {
		sep = " and "
	}
	return strings.Join(names, sep)
}

// privacyLine is the /status line, "" while the option is off:
// "카메라·마이크: 카메라 사용 중 · 마이크 쉬는 중".
func (c *Control) privacyLine() string {
	if c.d.Privacy == nil {
		return ""
	}
	p := c.d.Privacy(c.d.Config())
	if p == nil {
		return ""
	}
	return c.text("st_privacy") + ": " + c.privacyState(privacy.Camera, p.Camera) + " · " + c.privacyState(privacy.Microphone, p.Microphone)
}

func (c *Control) privacyState(device, state string) string {
	word := "privacy_unknown"
	switch state {
	case privacy.InUse:
		word = "privacy_in_use"
	case privacy.Idle:
		word = "privacy_idle"
	}
	return c.text("privacy_"+device) + " " + c.text(word)
}

// privacyVerdict asks the guard about a power command from the bot.
func (c *Control) privacyVerdict(name string) privacy.Verdict {
	if c.d.Guard == nil {
		return privacy.Verdict{}
	}
	return c.d.Guard(name)
}

// privacyRefusal is the reply for a power command v refuses.
func (c *Control) privacyRefusal(name string, v privacy.Verdict) string {
	return c.text("privacy_blocked", c.privacyDevices(v.InUse), c.commandLabel(name))
}

// privacyWarning is the line appended to a power command's reply under
// "warn", "" when nothing is in use.
func (c *Control) privacyWarning(v privacy.Verdict) string {
	if !v.Warn() {
		return ""
	}
	return c.text("privacy_warn", c.privacyDevices(v.InUse))
}

// scheduledGuard judges the active schedule's command for /now and the
// runnow: button, before the schedule is taken so a refusal leaves it in
// place: the refusal text ("" when it may run) and, under "warn", the
// warning line for the reply.
func (c *Control) scheduledGuard() (refusal, warning string) {
	s := c.d.Status.Schedule()
	if s["active"] != true {
		return "", ""
	}
	name, _ := s["command"].(string)
	v := c.privacyVerdict(name)
	if v.Block {
		return c.privacyRefusal(name, v), ""
	}
	return "", c.privacyWarning(v)
}

// withWarning appends a warning line to a reply when there is one.
func withWarning(reply, warning string) string {
	if warning == "" {
		return reply
	}
	return reply + "\n" + warning
}
