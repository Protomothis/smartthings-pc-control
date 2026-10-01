package devstate

import "testing"

func TestDecodeBattery(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   PowerStatus
		want BatteryInfo
	}{
		{"desktop: no system battery", PowerStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255},
			BatteryInfo{Present: false, Percent: -1, AC: true}},
		{"no battery flag with other bits and a stray percent", PowerStatus{ACLineStatus: 1, BatteryFlag: 128 | 8, BatteryLifePercent: 100},
			BatteryInfo{Present: false, Percent: -1, AC: true}},
		{"high, on battery", PowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 80},
			BatteryInfo{Present: true, Percent: 80}},
		{"charging bit on AC", PowerStatus{ACLineStatus: 1, BatteryFlag: 8 | 1, BatteryLifePercent: 80},
			BatteryInfo{Present: true, Percent: 80, Charging: true, AC: true}},
		{"low and charging", PowerStatus{ACLineStatus: 1, BatteryFlag: 8 | 2, BatteryLifePercent: 12},
			BatteryInfo{Present: true, Percent: 12, Charging: true, AC: true}},
		{"critical", PowerStatus{ACLineStatus: 0, BatteryFlag: 4, BatteryLifePercent: 3},
			BatteryInfo{Present: true, Percent: 3}},
		{"full on AC, not charging", PowerStatus{ACLineStatus: 1, BatteryFlag: 1, BatteryLifePercent: 100},
			BatteryInfo{Present: true, Percent: 100, AC: true}},
		{"flag 0 (between high and low)", PowerStatus{ACLineStatus: 0, BatteryFlag: 0, BatteryLifePercent: 50},
			BatteryInfo{Present: true, Percent: 50}},
		{"battery with unknown percent", PowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 255},
			BatteryInfo{Present: true, Percent: -1}},
		{"flag unknown, percent known", PowerStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 64},
			BatteryInfo{Present: true, Percent: 64}},
		{"flag unknown, percent unknown (VM)", PowerStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 255},
			BatteryInfo{Present: false, Percent: -1}},
		{"AC unknown reads as false", PowerStatus{ACLineStatus: 255, BatteryFlag: 1, BatteryLifePercent: 90},
			BatteryInfo{Present: true, Percent: 90}},
	} {
		if got := DecodeBattery(tc.in); got != tc.want {
			t.Errorf("%s: DecodeBattery(%+v) = %+v, want %+v", tc.name, tc.in, got, tc.want)
		}
	}
}
