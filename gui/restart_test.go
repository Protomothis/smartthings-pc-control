package gui

import (
	"strings"
	"testing"
)

// The command port's firewall rule line (#139).
func TestSTCommandPortLine(t *testing.T) {
	ko := &ui{lang: LangKo}
	if got := ko.stCommandPortLine(nil, 5001); got != "" {
		t.Errorf("a service without the field: %q, want the line hidden", got)
	}
	if got := ko.stCommandPortLine(&STCommandPort{}, 5001); got != T(LangKo, "st.cmdport.checking") {
		t.Errorf("before the check: %q", got)
	}
	cases := []struct {
		name  string
		state STCommandPort
		saved int
		ko    string
		en    string
	}{
		{"rule ok", STCommandPort{Port: 5001, FirewallRule: true, Checked: true}, 5001,
			"명령 포트 TCP 5001 · 방화벽 규칙 OK",
			"Command port TCP 5001 · firewall rule OK"},
		{"rule missing", STCommandPort{Port: 5001, Checked: true}, 0,
			"명령 포트 TCP 5001 · 방화벽 규칙 없음 (허브가 이 PC에 닿지 못할 수 있습니다, 로그 확인)",
			"Command port TCP 5001 · no firewall rule (the hub may not reach this PC, check the log)"},
		{"saved port not applied yet", STCommandPort{Port: 5001, FirewallRule: true, Checked: true}, 5005,
			"명령 포트 TCP 5001 · 방화벽 규칙 OK · 저장한 포트 5005는 서비스를 다시 시작하면 적용됩니다",
			"Command port TCP 5001 · firewall rule OK · the saved port 5005 applies after a service restart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ko.stCommandPortLine(&c.state, c.saved); got != c.ko {
				t.Errorf("ko = %q\nwant %q", got, c.ko)
			}
			if got := (&ui{lang: LangEn}).stCommandPortLine(&c.state, c.saved); got != c.en {
				t.Errorf("en = %q\nwant %q", got, c.en)
			}
		})
	}
}

// restartAPI is fakeAPI answering a save the way the service does for a
// port change, and counting restarts.
type restartAPI struct {
	*fakeAPI
	restarts int
}

func (r *restartAPI) SaveConfig(cfg Config) (SaveReply, error) {
	reply, err := r.fakeAPI.SaveConfig(cfg)
	reply.Message = "Settings saved. Restart service to apply port/remote-access changes."
	return reply, err
}

func (r *restartAPI) RestartService() error {
	r.restarts++
	return nil
}

// Saving a new port offers [지금 재시작] next to the "restart to apply"
// message (#139); taking it restarts the service through the same call
// as the settings tab's button, declining leaves the service alone.
func TestPortSaveOffersRestart(t *testing.T) {
	for _, accept := range []bool{true, false} {
		svc := &restartAPI{fakeAPI: &fakeAPI{cfg: storedConfig()}}
		u := newTestUI(t, LangKo, svc)
		u.initialLoad()
		var asked []string
		u.confirm = func(title, body, ok string, cb func(bool)) {
			asked = append(asked, body, ok)
			cb(accept)
		}
		u.portEntry.SetText("5005")
		u.saveTab(u.forms.tabAt(tabSettings))

		if len(asked) != 2 || !strings.Contains(asked[0], T(LangKo, "settings.saved.restart")) ||
			asked[1] != T(LangKo, "settings.restart.now") {
			t.Fatalf("accept=%v: asked %q, want the restart offer", accept, asked)
		}
		want := 0
		if accept {
			want = 1
		}
		if svc.restarts != want {
			t.Errorf("accept=%v: %d restarts, want %d", accept, svc.restarts, want)
		}
		if accept && u.statusText != T(LangKo, "settings.restarting") {
			t.Errorf("status after restarting = %q", u.statusText)
		}
	}
}

// A save that needs no restart only confirms; no offer is made.
func TestPlainSaveOffersNoRestart(t *testing.T) {
	u, _ := newFakeServiceUI(t)
	asked := false
	u.confirm = func(string, string, string, func(bool)) { asked = true }
	u.secretEntry.SetText("n3w-secret")
	u.saveTab(u.forms.tabAt(tabSettings))
	if asked {
		t.Error("a save the service applies at once offered a restart")
	}
}
