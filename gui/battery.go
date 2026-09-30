package gui

// Battery label in the status bar (#112). Only a PC with a battery shows
// it; on a desktop the label stays hidden and costs one request a minute.

import (
	"fmt"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
)

// batteryPollInterval matches the service, which reads the battery once a
// minute.
const batteryPollInterval = 60 * time.Second

// batteryText is the label: "배터리 80% · 충전 중", "배터리 100% · 전원
// 연결됨", "배터리 15%". "" means no battery, and no label.
func (u *ui) batteryText(b Battery) string {
	if !b.Present {
		return ""
	}
	level := u.t("battery.unknown")
	if b.Percent >= 0 {
		level = strconv.Itoa(b.Percent) + "%"
	}
	text := fmt.Sprintf(u.t("battery.label"), level)
	switch {
	case b.Charging:
		text += " · " + u.t("battery.charging")
	case b.AC:
		text += " · " + u.t("battery.ac")
	}
	return text
}

// loadBattery polls the service. Runs off the UI thread; any error just
// hides the label (the connection state has its own indicator).
func (u *ui) loadBattery() {
	b, err := u.client.GetBattery()
	if err != nil {
		b = Battery{}
	}
	fyne.Do(func() { u.applyBattery(b) })
}

// applyBattery shows or hides the label. Must be called on the UI thread.
func (u *ui) applyBattery(b Battery) {
	u.lastBattery = b
	if u.batteryLabel == nil {
		return
	}
	text := u.batteryText(b)
	if text == "" {
		u.batteryLabel.Hide()
		return
	}
	u.batteryLabel.SetText(text)
	u.batteryLabel.Show()
}
