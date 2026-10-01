package tgcontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// testControl is a Control whose live config is cfg, on the fakes below.
type testControl struct {
	*Control
	cfg config.Config
}

func (c *testControl) setConfig(cfg config.Config) { c.cfg = cfg }

type fakeStatus struct{}

func (fakeStatus) Hostname() string         { return "TEST-PC" }
func (fakeStatus) Schedule() map[string]any { return map[string]any{"active": false} }
func (fakeStatus) LastRemote() Remote       { return Remote{} }
func (fakeStatus) Battery() status.Battery  { return status.Battery{Percent: -1} }
func (fakeStatus) Activity(config.Config) status.Activity {
	return status.Activity{Apps: []status.ActivityApp{}}
}
func (fakeStatus) Media(config.Config) status.Media { return status.Media{Status: "none"} }

type fakeCommands struct{}

func (fakeCommands) Known(string) bool { return false }
func (fakeCommands) GraceCommands() []string {
	return []string{"hibernate", "restart", "shutdown", "suspend"}
}
func (fakeCommands) RunNow(string) bool                   { return false }
func (fakeCommands) Schedule(string, time.Duration) error { return errors.New("no schedules") }
func (fakeCommands) Take(string) (string, bool)           { return "", false }
func (fakeCommands) TakeForRun(string) (string, bool)     { return "", false }
func (fakeCommands) GraceSeq(string) (uint64, bool)       { return 0, false }

type fakeAwake struct{}

func (fakeAwake) View() status.AwakeView                   { return status.AwakeView{} }
func (fakeAwake) TurnOn(int) (status.AwakeView, error)     { return status.AwakeView{On: true}, nil }
func (fakeAwake) TurnOff() (status.AwakeView, bool, error) { return status.AwakeView{}, false, nil }
func (fakeAwake) Now() time.Time                           { return time.Now() }

type fakeMedia struct{}

func (fakeMedia) Run(context.Context, string, *int) (session.Result, error) {
	return session.Result{}, action.ErrMediaDisabled
}
func (fakeMedia) ReadAudio(context.Context) (useraction.Audio, error) {
	return useraction.Audio{}, action.ErrMediaDisabled
}
func (fakeMedia) ReadNowPlaying(context.Context) (useraction.NowPlaying, error) {
	return useraction.NowPlaying{}, action.ErrMediaDisabled
}

type fakePresets struct{}

func (fakePresets) Run(context.Context, config.Preset, string) error { return errors.New("no presets") }
func (fakePresets) Record(config.Preset, string, string)             {}

type fakeNotifier struct{}

func (fakeNotifier) Send(context.Context, config.NotifyPCConfig, string, string, string) (action.NotifyResult, error) {
	return action.NotifyResult{}, &action.NotifyError{Code: "notify_disabled"}
}

// newTestControl is a Control over cfg and the fakes above; c.setConfig
// changes the live config.
func newTestControl(t *testing.T, cfg config.Config) *testControl {
	t.Helper()
	c := &testControl{cfg: cfg}
	c.Control = New(Deps{
		Config:        func() config.Config { return c.cfg },
		Version:       func() string { return "v1.2.0-test" },
		Emit:          func(string, string, map[string]string) {},
		BaseURL:       func() string { return "http://127.0.0.1:1" },
		Bus:           func() *notify.Bus { return nil },
		StartedAt:     time.Now(),
		ActionTimeout: func() time.Duration { return time.Second },
		Status:        fakeStatus{},
		Commands:      fakeCommands{},
		Awake:         fakeAwake{},
		Media:         fakeMedia{},
		Presets:       fakePresets{},
		Notify:        fakeNotifier{},
	})
	return c
}
