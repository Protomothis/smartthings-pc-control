package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMonitorReason(t *testing.T) {
	ko := &ui{lang: LangKo}
	b := 40
	dell := MonitorInfo{ID: "m1", Primary: true, DDC: true, Brightness: &b, Inputs: []string{}}
	tv := MonitorInfo{ID: "m1", Primary: true, Inputs: []string{}}
	for _, tc := range []struct {
		s    MonitorState
		err  error
		want string
	}{
		{MonitorState{}, errMonitorUnsupported, "서비스 v1.3.0"},
		{MonitorState{Enabled: false, Monitors: []MonitorInfo{dell}}, nil, "원격 모니터 제어 허용"},
		{MonitorState{Enabled: true}, nil, "읽는 중"},
		{MonitorState{Enabled: true, Monitors: []MonitorInfo{tv}}, nil, "DDC/CI에 응답하지"},
		{MonitorState{Enabled: true, Monitors: []MonitorInfo{dell}}, nil, ""},
		{MonitorState{}, errors.New("network"), ""},
	} {
		got := ko.monitorReason(tc.s, tc.err)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("monitorReason(%+v, %v) = %q, want %q", tc.s, tc.err, got, tc.want)
		}
	}
}

func TestMonitorErrorText(t *testing.T) {
	ko := &ui{lang: LangKo}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&actionError{Code: "monitor_disabled"}, "원격 모니터 제어 허용"},
		{&actionError{Code: "no_user_session"}, "로그인한 사용자가 없어"},
		{&actionError{Code: "timeout"}, "제때 답하지"},
		{&actionError{Code: "monitor_unsupported", Message: "m1"}, "DDC/CI로 할 수 없습니다: m1"},
		{&actionError{Code: "monitor_failed", Message: "refused"}, "실패: refused"},
		{&actionError{Code: "value must be on or off"}, "실패: value must be on or off"},
	} {
		if got := ko.monitorErrorText(tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("%v: %q", tc.err, got)
		}
	}
}

func TestMonitorClient(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor" {
			http.NotFound(w, r)
			return
		}
		if r.Method == "POST" {
			json.NewDecoder(r.Body).Decode(&got)
			if got["value"] == "off" {
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte(`{"error":"monitor_failed","message":"refused"}`))
				return
			}
		}
		w.Write([]byte(`{"enabled":true,"monitors":[{"id":"m2","primary":false,"inputs":[]},{"id":"m1","primary":true,"ddc":true,"brightness":40,"input":"hdmi1","inputs":["dp1","hdmi1"],"power":"on"}]}`))
	}))
	defer srv.Close()
	c := &Client{base: srv.URL, http: srv.Client()}

	s, err := c.GetMonitors()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := s.Primary(); !ok || m.ID != "m1" || *m.Brightness != 40 || *m.Input != "hdmi1" {
		t.Errorf("primary = %+v", m)
	}
	if _, err := c.MonitorCommand("monitor_brightness", 70); err != nil || got["command"] != "monitor_brightness" || got["value"] != float64(70) {
		t.Errorf("brightness: %v %v", got, err)
	}
	var ae *actionError
	if _, err := c.MonitorCommand("monitor_power", "off"); !errors.As(err, &ae) || ae.Code != "monitor_failed" || ae.Message != "refused" {
		t.Errorf("power off: %v", err)
	}

	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if _, err := (&Client{base: old.URL, http: old.Client()}).GetMonitors(); !errors.Is(err, errMonitorUnsupported) {
		t.Errorf("older service: %v", err)
	}
}
