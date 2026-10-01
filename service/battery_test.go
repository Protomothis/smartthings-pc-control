package service

// Tests for the battery report (#112, docs/design/media-notify.md §13).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/devstate"
)

// fakeBattery is a monitor fed from a slice, recording the changes it
// reports.
type fakeBattery struct {
	readings []systemPowerStatus
	err      error
	changes  []batteryInfo
}

func (f *fakeBattery) monitor() *batteryMonitor {
	return &batteryMonitor{
		Last: devstate.UnknownBattery,
		Read: func() (systemPowerStatus, error) {
			if f.err != nil {
				return systemPowerStatus{}, f.err
			}
			s := f.readings[0]
			if len(f.readings) > 1 {
				f.readings = f.readings[1:]
			}
			return s, nil
		},
		OnChange: func(b batteryInfo) { f.changes = append(f.changes, b) },
	}
}

// stubBattery installs m as the service's monitor for one test.
func stubBattery(t *testing.T, m *batteryMonitor) {
	t.Helper()
	orig := battery
	battery = m
	t.Cleanup(func() { battery = orig })
}

func TestTelegramStatusBatteryLine(t *testing.T) {
	initLogger()
	prev := getConfig()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"}})
	t.Cleanup(func() { setConfig(prev) })
	stubAwake(t)
	h := tgCtl

	for _, tc := range []struct {
		raw  systemPowerStatus
		want string // "" = no battery line
	}{
		{systemPowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}, ""},
		{systemPowerStatus{ACLineStatus: 1, BatteryFlag: 1 | 8, BatteryLifePercent: 80}, "배터리: 80% · 충전 중"},
		{systemPowerStatus{ACLineStatus: 1, BatteryFlag: 1, BatteryLifePercent: 100}, "배터리: 100% · 전원 연결됨"},
		{systemPowerStatus{ACLineStatus: 0, BatteryFlag: 2, BatteryLifePercent: 15}, "배터리: 15%"},
		{systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 255}, "배터리: 잔량 알 수 없음"},
	} {
		f := &fakeBattery{readings: []systemPowerStatus{tc.raw}}
		m := f.monitor()
		m.Poll()
		stubBattery(t, m)
		status, _, _ := h.HandleCommand(context.Background(), "42", "status", nil)
		if tc.want == "" {
			if strings.Contains(status, "배터리") {
				t.Errorf("desktop /status has a battery line:\n%s", status)
			}
			continue
		}
		if !strings.Contains(status, tc.want) {
			t.Errorf("/status lacks %q:\n%s", tc.want, status)
		}
	}

	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "en"}})
	f := &fakeBattery{readings: []systemPowerStatus{{ACLineStatus: 1, BatteryFlag: 8, BatteryLifePercent: 42}}}
	m := f.monitor()
	m.Poll()
	stubBattery(t, m)
	status, _, _ := h.HandleCommand(context.Background(), "42", "status", nil)
	if !strings.Contains(status, "Battery: 42% · charging") {
		t.Errorf("en /status:\n%s", status)
	}
}

func TestBatteryAPI(t *testing.T) {
	prev := getConfig()
	setConfig(Config{Port: 5001})
	t.Cleanup(func() { setConfig(prev) })
	f := &fakeBattery{readings: []systemPowerStatus{{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 55}}}
	m := f.monitor()
	m.Poll()
	stubBattery(t, m)

	w := httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("GET", "/api/battery", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"present":true`) || !strings.Contains(w.Body.String(), `"percent":55`) {
		t.Errorf("GET /api/battery = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("POST", "/api/battery", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", w.Code)
	}
}
