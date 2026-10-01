package webui

// A Server on fixed fakes, for the tests of what the WebUI keeps itself:
// sessions, the local login, the Host check. The root service's tests
// drive every /api route against the real stores.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
	"github.com/Protomothis/smartthings-pc-control/useraction"

	"golang.org/x/sys/windows"
)

// testServer is a Server whose live config is cfg.
type testServer struct {
	*Server
	cfg     config.Config
	session func() (session.Info, error)
}

type fakeStatus struct{ ts *testServer }

func (fakeStatus) MachineID() string     { return "test-machine" }
func (fakeStatus) Hostname() string      { return "TEST-PC" }
func (fakeStatus) Update() status.Update { return status.Update{} }
func (fakeStatus) Display() string       { return "unknown" }
func (f fakeStatus) Session() (session.Info, error) {
	if f.ts.session != nil {
		return f.ts.session()
	}
	return session.Info{}, errors.New("no session in the tests")
}
func (fakeStatus) Battery() status.Battery      { return status.Battery{Percent: -1} }
func (fakeStatus) Schedule() map[string]any     { return map[string]any{"active": false} }
func (fakeStatus) WoLScan() status.WoLStatus    { return status.WoLStatus{} }
func (fakeStatus) Processes() ([]string, error) { return nil, nil }

type fakeHub struct{}

func (fakeHub) HubLastSeen() (status.HubSeen, bool) { return status.HubSeen{}, false }
func (fakeHub) WoLView(config.SmartThingsConfig) (status.WoL, *status.WoLSelected) {
	return status.WoL{Adapters: []status.WoLAdapter{}}, nil
}
func (fakeHub) SSDPRunning() bool                         { return false }
func (fakeHub) LastSSDPSearch() (status.SSDPSearch, bool) { return status.SSDPSearch{}, false }
func (fakeHub) SSDPFirewallRule() bool                    { return false }

type fakeCommands struct{}

func (fakeCommands) RunNow(string, string) bool           { return false }
func (fakeCommands) Schedule(string, time.Duration) error { return errors.New("no schedules") }
func (fakeCommands) CancelSchedule(string) bool           { return false }

type fakeAwake struct{}

func (fakeAwake) View() status.AwakeView                   { return status.AwakeView{} }
func (fakeAwake) TurnOn(int) (status.AwakeView, error)     { return status.AwakeView{On: true}, nil }
func (fakeAwake) TurnOff() (status.AwakeView, bool, error) { return status.AwakeView{}, false, nil }
func (fakeAwake) Now() time.Time                           { return time.Now() }

type fakeMedia struct{}

func (fakeMedia) Run(context.Context, string, *int) (session.Result, error) {
	return session.Result{}, action.ErrMediaDisabled
}
func (fakeMedia) AudioView(useraction.Audio) status.Audio { return status.Audio{} }
func (fakeMedia) SessionPresent() bool                    { return false }
func (fakeMedia) Audio(config.Config) status.Audio        { return status.Audio{} }
func (fakeMedia) Media(config.Config) status.Media        { return status.Media{Status: "none"} }

type fakePresets struct{}

func (fakePresets) Run(context.Context, config.Preset, string) error { return errors.New("no presets") }

type fakeNotifier struct{}

func (fakeNotifier) Send(context.Context, config.NotifyPCConfig, string, string, string) (action.NotifyResult, error) {
	return action.NotifyResult{}, &action.NotifyError{Code: "notify_disabled"}
}

type fakeTelegram struct{}

func (fakeTelegram) Client(token string) *telegram.Client { return telegram.NewClient(token) }
func (fakeTelegram) PCName(config.TelegramConfig) string  { return "TEST-PC" }
func (fakeTelegram) Polling() bool                        { return false }
func (fakeTelegram) Conflict() (bool, time.Time)          { return false, time.Time{} }

type fakeHeartbeat struct{}

func (fakeHeartbeat) Now() time.Time                         { return time.Now() }
func (fakeHeartbeat) Target() (uint32, error)                { return 0, errors.New("no session") }
func (fakeHeartbeat) Ignored(uint32, uint32)                 {}
func (fakeHeartbeat) Idle(int64)                             {}
func (fakeHeartbeat) Audio(useraction.Audio, time.Time)      {}
func (fakeHeartbeat) Media(useraction.NowPlaying, time.Time) {}

// newTestServer is a Server over cfg and the fakes above; set ts.cfg to
// change the live config.
func newTestServer(t *testing.T, cfg config.Config) *testServer {
	t.Helper()
	ts := &testServer{cfg: cfg}
	ts.Server = New(Deps{
		Config:     func() config.Config { return ts.cfg },
		SaveConfig: func(c config.Config) error { ts.cfg = c; return nil },
		Version:    func() string { return "v1.2.0-test" },
		Emit:       func(string, string, map[string]string) {},
		Restart:    func() {},
		Logins:     ratelimit.NewLockout(5, time.Minute, nil),
		UserToken: func(uint32) (windows.Token, error) {
			return 0, errors.New("WTSQueryUserToken needs LocalSystem")
		},
		Status:    fakeStatus{ts},
		Hub:       fakeHub{},
		Commands:  fakeCommands{},
		Awake:     fakeAwake{},
		Media:     fakeMedia{},
		Presets:   fakePresets{},
		Notify:    fakeNotifier{},
		Telegram:  fakeTelegram{},
		Heartbeat: fakeHeartbeat{},
	})
	return ts
}
