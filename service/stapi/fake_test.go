package stapi

// A Server on fixed fakes, for the tests of what /st/v1 keeps itself: the
// SSDP responder, the WoL choice, the push mapping. The root service's
// tests drive the whole surface against the real stores (and the
// contract fixtures in testdata/st-v1).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	testVersion   = "v1.1.0-test"
	testMachineID = "4c4c4544-0042-3510-8052-b4c04f4a3732"
)

// fakeSource answers the status with fixed values.
type fakeSource struct{ wol status.WoLStatus }

func (fakeSource) MachineID() string                { return testMachineID }
func (fakeSource) Hostname() string                 { return "TEST-PC" }
func (fakeSource) LastShutdownClean() bool          { return true }
func (fakeSource) Update() status.Update            { return status.Update{} }
func (fakeSource) Schedule() map[string]any         { return map[string]any{"active": false} }
func (fakeSource) LastCommand() *status.LastCommand { return nil }
func (fakeSource) Display() string                  { return "unknown" }
func (fakeSource) Session() (session.Info, error) {
	return session.Info{}, errors.New("no session in the tests")
}
func (fakeSource) IdleSeconds() (int64, bool) { return 0, false }
func (fakeSource) Battery() status.Battery    { return status.Battery{Percent: -1} }
func (fakeSource) Activity(config.Config) status.Activity {
	return status.Activity{Apps: []status.ActivityApp{}}
}
func (fakeSource) Audio(config.Config) status.Audio { return status.Audio{} }
func (fakeSource) Media(config.Config) status.Media { return status.Media{Status: "none"} }
func (f fakeSource) WoLScan() status.WoLStatus      { return f.wol }

// fakeCommands knows no command and schedules nothing.
type fakeCommands struct{}

func (fakeCommands) Known(string) bool                           { return false }
func (fakeCommands) Dispatch(string, string, Mode) time.Duration { return 0 }
func (fakeCommands) Record(string, string)                       {}
func (fakeCommands) Schedule(string, time.Duration) error {
	return errors.New("no schedules in the tests")
}
func (fakeCommands) Cancel(string) bool { return false }

type fakeAwake struct{}

func (fakeAwake) View() status.AwakeView                   { return status.AwakeView{} }
func (fakeAwake) TurnOn(int) (status.AwakeView, error)     { return status.AwakeView{On: true}, nil }
func (fakeAwake) TurnOff() (status.AwakeView, bool, error) { return status.AwakeView{}, false, nil }

type fakeMedia struct{}

func (fakeMedia) Run(context.Context, string, *int) (session.Result, error) {
	return session.Result{}, action.ErrMediaDisabled
}
func (fakeMedia) AudioView(useraction.Audio) status.Audio { return status.Audio{} }

type fakePresets struct{}

func (fakePresets) Run(context.Context, config.Preset, string) error { return errors.New("no presets") }
func (fakePresets) Record(config.Preset, string, string)             {}

type fakeNotifier struct{}

func (fakeNotifier) Send(context.Context, config.NotifyPCConfig, string, string, string) (action.NotifyResult, error) {
	return action.NotifyResult{}, &action.NotifyError{Code: "notify_disabled"}
}

// newTestServer is a Server over cfg and the fakes above; emitted events
// are dropped.
func newTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	return New(Deps{
		Config:   func() config.Config { return cfg },
		Version:  func() string { return testVersion },
		Emit:     func(string, string, map[string]string) {},
		Status:   fakeSource{},
		Commands: fakeCommands{},
		Awake:    fakeAwake{},
		Media:    fakeMedia{},
		Presets:  fakePresets{},
		Notify:   fakeNotifier{},
	})
}
