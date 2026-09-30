package gui

import "testing"

func TestBatteryText(t *testing.T) {
	ko := &ui{lang: LangKo}
	en := &ui{lang: LangEn}
	for _, tc := range []struct {
		u    *ui
		b    Battery
		want string
	}{
		{ko, Battery{Present: false, Percent: -1, AC: true}, ""},
		{ko, Battery{}, ""},
		{ko, Battery{Present: true, Percent: 80, Charging: true, AC: true}, "배터리 80% · 충전 중"},
		{ko, Battery{Present: true, Percent: 100, AC: true}, "배터리 100% · 전원 연결됨"},
		{ko, Battery{Present: true, Percent: 15}, "배터리 15%"},
		{ko, Battery{Present: true, Percent: -1}, "배터리 잔량 알 수 없음"},
		{en, Battery{Present: true, Percent: 42, Charging: true, AC: true}, "Battery 42% · charging"},
		{en, Battery{Present: true, Percent: 99, AC: true}, "Battery 99% · plugged in"},
	} {
		if got := tc.u.batteryText(tc.b); got != tc.want {
			t.Errorf("%s %+v = %q, want %q", tc.u.lang, tc.b, got, tc.want)
		}
	}
}
