package service

// Tests for the battery report (#112, docs/design/media-notify.md §13).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeBattery(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   systemPowerStatus
		want batteryInfo
	}{
		{"desktop: no system battery", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255},
			batteryInfo{Present: false, Percent: -1, AC: true}},
		{"no battery flag with other bits and a stray percent", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 128 | 8, BatteryLifePercent: 100},
			batteryInfo{Present: false, Percent: -1, AC: true}},
		{"high, on battery", systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 80},
			batteryInfo{Present: true, Percent: 80}},
		{"charging bit on AC", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 8 | 1, BatteryLifePercent: 80},
			batteryInfo{Present: true, Percent: 80, Charging: true, AC: true}},
		{"low and charging", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 8 | 2, BatteryLifePercent: 12},
			batteryInfo{Present: true, Percent: 12, Charging: true, AC: true}},
		{"critical", systemPowerStatus{ACLineStatus: 0, BatteryFlag: 4, BatteryLifePercent: 3},
			batteryInfo{Present: true, Percent: 3}},
		{"full on AC, not charging", systemPowerStatus{ACLineStatus: 1, BatteryFlag: 1, BatteryLifePercent: 100},
			batteryInfo{Present: true, Percent: 100, AC: true}},
		{"flag 0 (between high and low)", systemPowerStatus{ACLineStatus: 0, BatteryFlag: 0, BatteryLifePercent: 50},
			batteryInfo{Present: true, Percent: 50}},
		{"battery with unknown percent", systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 255},
			batteryInfo{Present: true, Percent: -1}},
		{"flag unknown, percent known", systemPowerStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 64},
			batteryInfo{Present: true, Percent: 64}},
		{"flag unknown, percent unknown (VM)", systemPowerStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 255},
			batteryInfo{Present: false, Percent: -1}},
		{"AC unknown reads as false", systemPowerStatus{ACLineStatus: 255, BatteryFlag: 1, BatteryLifePercent: 90},
			batteryInfo{Present: true, Percent: 90}},
	} {
		if got := decodeBattery(tc.in); got != tc.want {
			t.Errorf("%s: decodeBattery(%+v) = %+v, want %+v", tc.name, tc.in, got, tc.want)
		}
	}
}

// fakeBattery is a monitor fed from a slice, recording the changes it
// reports.
type fakeBattery struct {
	readings []systemPowerStatus
	err      error
	changes  []batteryInfo
}

func (f *fakeBattery) monitor() *batteryMonitor {
	return &batteryMonitor{
		last: unknownBattery,
		read: func() (systemPowerStatus, error) {
			if f.err != nil {
				return systemPowerStatus{}, f.err
			}
			s := f.readings[0]
			if len(f.readings) > 1 {
				f.readings = f.readings[1:]
			}
			return s, nil
		},
		onChange: func(b batteryInfo) { f.changes = append(f.changes, b) },
	}
}

// stubBattery installs m as the service's monitor for one test.
func stubBattery(t *testing.T, m *batteryMonitor) {
	t.Helper()
	orig := battery
	battery = m
	t.Cleanup(func() { battery = orig })
}

func TestBatteryChangeDetection(t *testing.T) {
	initLogger()
	laptop := func(percent byte, charging bool) systemPowerStatus {
		s := systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: percent}
		if charging {
			s.ACLineStatus, s.BatteryFlag = 1, 1|8
		}
		return s
	}
	f := &fakeBattery{readings: []systemPowerStatus{
		laptop(80, false), // first reading: fills the cache, no event
		laptop(80, false), // same: no event
		laptop(79, false), // percent moved
		laptop(79, true),  // plugged in
		laptop(79, true),
	}}
	m := f.monitor()
	if got := m.info(); got != unknownBattery {
		t.Errorf("before the first reading = %+v", got)
	}
	var changed []bool
	for i := 0; i < 5; i++ {
		changed = append(changed, m.poll())
	}
	want := []bool{false, false, true, true, false}
	for i := range want {
		if changed[i] != want[i] {
			t.Errorf("poll %d changed = %v, want %v", i, changed[i], want[i])
		}
	}
	if len(f.changes) != 2 || f.changes[0].Percent != 79 || f.changes[0].Charging || !f.changes[1].Charging {
		t.Errorf("changes = %+v", f.changes)
	}
	if got := m.info(); got != (batteryInfo{Present: true, Percent: 79, Charging: true, AC: true}) {
		t.Errorf("info = %+v", got)
	}

	// A failed read keeps the last value and is not a change.
	f.err = errors.New("nope")
	if m.poll() {
		t.Error("a failed read reported a change")
	}
	if m.info().Percent != 79 {
		t.Errorf("a failed read lost the last value: %+v", m.info())
	}
}

// A desktop never changes, so it never pushes.
func TestBatteryDesktopNeverChanges(t *testing.T) {
	initLogger()
	f := &fakeBattery{readings: []systemPowerStatus{{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}}}
	m := f.monitor()
	for i := 0; i < 3; i++ {
		m.poll()
	}
	if len(f.changes) != 0 {
		t.Errorf("desktop changes = %+v", f.changes)
	}
}

func TestSTStatusBatteryAndFeatures(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	stubAwake(t)

	// Desktop: the block is there, "battery" is not in features.
	f := &fakeBattery{readings: []systemPowerStatus{{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255}}}
	m := f.monitor()
	m.poll()
	stubBattery(t, m)
	got := stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	bat, ok := got["battery"].(map[string]any)
	if !ok || bat["present"] != false || bat["percent"] != float64(-1) || bat["charging"] != false || bat["ac"] != true {
		t.Errorf("desktop battery = %v", got["battery"])
	}
	if features := fmt.Sprint(got["features"]); features != "[awake notify presets]" {
		t.Errorf("desktop features = %v, want [awake notify presets]", features)
	}

	// Laptop: "battery" joins features.
	f = &fakeBattery{readings: []systemPowerStatus{{ACLineStatus: 1, BatteryFlag: 1 | 8, BatteryLifePercent: 80}}}
	m = f.monitor()
	m.poll()
	stubBattery(t, m)
	got = stJSON(t, stDo(t, "GET", "/st/v1/status", "192.168.1.20", "", ""))
	bat = got["battery"].(map[string]any)
	if bat["present"] != true || bat["percent"] != float64(80) || bat["charging"] != true || bat["ac"] != true {
		t.Errorf("laptop battery = %v", bat)
	}
	if features := fmt.Sprint(got["features"]); features != "[awake battery notify presets]" {
		t.Errorf("laptop features = %v, want [awake battery notify presets]", features)
	}
}

func TestBatteryChangedIsPushed(t *testing.T) {
	stPushSetup(t, Config{Port: 5001})
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	f := &fakeBattery{readings: []systemPowerStatus{
		{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 21},
		{ACLineStatus: 0, BatteryFlag: 2, BatteryLifePercent: 20},
	}}
	m := f.monitor()
	m.onChange = emitBatteryChanged
	stubBattery(t, m)
	cb := newCallbackServer(t)
	subscribeTo(t, cb, 600)

	m.poll()
	m.poll()
	got := cb.wait(t)
	if got["type"] != "battery.changed" {
		t.Fatalf("type = %v", got["type"])
	}
	data, _ := got["data"].(map[string]any)
	if data["percent"] != "20" || data["present"] != "true" || data["charging"] != "false" || data["ac"] != "false" {
		t.Errorf("data = %v", data)
	}
	status, _ := got["status"].(map[string]any)
	if bat, _ := status["battery"].(map[string]any); bat["percent"] != float64(20) {
		t.Errorf("status.battery = %v", status["battery"])
	}
}

func TestTelegramStatusBatteryLine(t *testing.T) {
	initLogger()
	prev := getConfig()
	setConfig(Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko"}})
	t.Cleanup(func() { setConfig(prev) })
	stubAwake(t)
	var h telegramControl

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
		m.poll()
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
	m.poll()
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
	m.poll()
	stubBattery(t, m)

	w := httptest.NewRecorder()
	handleBatteryAPI(w, httptest.NewRequest("GET", "/api/battery", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"present":true`) || !strings.Contains(w.Body.String(), `"percent":55`) {
		t.Errorf("GET /api/battery = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handleBatteryAPI(w, httptest.NewRequest("POST", "/api/battery", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", w.Code)
	}
}
