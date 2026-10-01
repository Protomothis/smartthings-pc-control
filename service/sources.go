package service

// The service's state as the front ends read it (#127): service/stapi,
// service/webui and service/tgcontrol get these adapters at construction
// and never see the stores or the globals behind them.

import (
	"context"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// serviceSources is what the status blocks are built from. The func
// fields are the seams the tests replace; everything else reads the live
// stores on each call.
type serviceSources struct {
	// sessionQuery describes the target user session (querySessionInfo).
	// The contract tests replace it: the real one asks WTS about whatever
	// session the machine running the tests happens to have.
	sessionQuery func() (sessionInfo, error)
	// wolScan is the adapter scan (getWoLStatus), which shells out to
	// PowerShell and queries the public IP.
	wolScan func() WoLStatus
}

var sources = &serviceSources{
	sessionQuery: querySessionInfo,
	wolScan:      getWoLStatus,
}

func (*serviceSources) MachineID() string                { return machineID() }
func (*serviceSources) Hostname() string                 { return hostname() }
func (*serviceSources) LastShutdownClean() bool          { return lastShutdownClean.Load() }
func (*serviceSources) Update() status.Update            { return stUpdateInfo() }
func (*serviceSources) Schedule() map[string]any         { return getSchedule() }
func (*serviceSources) Display() string                  { return getDisplayState() }
func (s *serviceSources) Session() (sessionInfo, error)  { return s.sessionQuery() }
func (*serviceSources) IdleSeconds() (int64, bool)       { return lastIdleSeconds() }
func (*serviceSources) Battery() status.Battery          { return battery.Info() }
func (*serviceSources) Activity(cfg Config) stActivity   { return stActivityStatus(cfg) }
func (*serviceSources) Audio(cfg Config) stAudio         { return stAudioStatus(cfg) }
func (*serviceSources) Media(cfg Config) stMedia         { return stMediaStatus(cfg) }
func (s *serviceSources) WoLScan() WoLStatus             { return s.wolScan() }
func (*serviceSources) LastCommand() *status.LastCommand { return lastCommandBlock() }
func (*serviceSources) Processes() ([]string, error)     { return runningProcessNames() }

// lastCommandBlock is the last remote command as the status reports it,
// nil before one.
func lastCommandBlock() *status.LastCommand {
	lr := getLastRemote()
	if lr.Command == "" {
		return nil
	}
	out := &status.LastCommand{
		Command: lr.Command,
		Origin:  lr.Origin,
		At:      lr.At.Format(time.RFC3339),
	}
	if p := lr.Preset; p != nil {
		out.Preset = &status.PresetRef{Slot: p.Slot, Name: p.Name}
		out.Result = p.Result
	}
	return out
}

// awakeControl is the keep-awake controller of the moment (currentAwake:
// the tests swap it).
type awakeControl struct{}

func (awakeControl) View() status.AwakeView { return currentAwake().View() }
func (awakeControl) TurnOn(minutes int) (status.AwakeView, error) {
	return currentAwake().TurnOn(minutes)
}
func (awakeControl) TurnOff() (status.AwakeView, bool, error) { return currentAwake().TurnOff() }
func (awakeControl) Now() time.Time                           { return currentAwake().now() }

// mediaControl runs the volume, mute and media-key commands and reports
// the state the media card shows.
type mediaControl struct{}

func (mediaControl) Run(ctx context.Context, name string, value *int) (UserActionResult, error) {
	return runMediaCommand(ctx, name, value)
}

// AudioView stamps a command reply's own reading with the audio clock.
func (mediaControl) AudioView(a useraction.Audio) status.Audio {
	return stAudioView(audioSample{Audio: a, UpdatedAt: audioNow()})
}

func (mediaControl) SessionPresent() bool     { return audioSessionPresent() }
func (mediaControl) Audio(cfg Config) stAudio { return stAudioStatus(cfg) }
func (mediaControl) Media(cfg Config) stMedia { return stMediaStatus(cfg) }

// presetControl runs presets on behalf of one origin ("smartthings",
// "telegram"), which last_command records.
type presetControl struct{ origin string }

func (p presetControl) Run(ctx context.Context, preset Preset, by string) error {
	return runPreset(ctx, preset, by)
}
func (p presetControl) Record(preset Preset, from, result string) {
	notePresetCommand(preset, from, p.origin, result)
}

// pcNotifier shows PC notifications with the enabled switch checked.
type pcNotifier struct{}

func (pcNotifier) Send(ctx context.Context, cfg NotifyPCConfig, source, title, text string) (action.NotifyResult, error) {
	return sendPCNotify(ctx, cfg, true, source, title, text)
}
