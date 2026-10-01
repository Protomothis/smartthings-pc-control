package stapi

import (
	"context"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Deps is everything the /st/v1 surface reads and drives. The root service
// fills it once; every func and method is called on each request, so a
// saved setting or a new reading takes effect without a restart.
type Deps struct {
	// Config is the live configuration.
	Config func() config.Config
	// Version is this build's version (service_version, the SSDP SERVER
	// header, the push User-Agent).
	Version func() string
	// Emit raises a notification: security.unauthorized and
	// security.unknown_command, remote.received for a preset.
	Emit func(category, kind string, fields map[string]string)

	Status   StatusSource
	Commands Commands
	Awake    Awake
	Media    Media
	Presets  Presets
	Notify   Notifier
}

// StatusSource is what the status document (§3.2) reads that /st/v1 does
// not keep itself. The hub's own state — last contact, the interface it
// used, subscriptions — lives in the Server.
type StatusSource interface {
	// MachineID is the stable per-install id the driver keys its device on.
	MachineID() string
	Hostname() string
	// LastShutdownClean is what state.json said at this start.
	LastShutdownClean() bool
	// Update is the newest release this service knows about.
	Update() status.Update
	// Schedule is the schedule slot in the /api/schedule wire form
	// (active, command, origin, executeAt, remainingSec).
	Schedule() map[string]any
	// LastCommand is the last remote command, nil before one.
	LastCommand() *status.LastCommand
	// Display is the last screen command's effect: on, off or unknown.
	Display() string
	// Session describes the target user session (WTS); an error when
	// nobody is logged in or WTS refused.
	Session() (session.Info, error)
	// IdleSeconds is the tray heartbeat's idle time while it is fresh.
	IdleSeconds() (int64, bool)
	Battery() status.Battery
	Activity(cfg config.Config) status.Activity
	Audio(cfg config.Config) status.Audio
	Media(cfg config.Config) status.Media
	// WoLScan is the adapter scan (PowerShell); the Server caches it.
	WoLScan() status.WoLStatus
}

// Mode says whether a command may wait out the grace period (§3.3).
type Mode int

const (
	// ModeDefault defers a grace command while shutdown_grace is on.
	ModeDefault Mode = iota
	// ModeGrace defers a grace command even with shutdown_grace off.
	ModeGrace
	// ModeImmediate never defers.
	ModeImmediate
)

// Commands is the command catalogue and the schedule slot, on behalf of
// SmartThings (origin "smartthings").
type Commands interface {
	// Known reports whether name is a catalogue command.
	Known(name string) bool
	// Dispatch runs name for from, or defers it by the grace period;
	// deferred is that period, 0 when it runs now.
	Dispatch(name, from string, mode Mode) (deferred time.Duration)
	// Record makes name the last command.
	Record(name, from string)
	// Schedule arms name to run after delay.
	Schedule(name string, delay time.Duration) error
	// Cancel ends the schedule on behalf of by; false when there was none.
	Cancel(by string) bool
}

// Awake is the keep-awake controller (#111, §12).
type Awake interface {
	View() status.AwakeView
	// TurnOn keeps the PC awake for minutes (0: until turned off).
	TurnOn(minutes int) (status.AwakeView, error)
	// TurnOff lets it sleep again; wasOn is false when it already could.
	TurnOff() (v status.AwakeView, wasOn bool, err error)
}

// Media runs the volume, mute and media-key commands (#104, #105).
type Media interface {
	// Run checks media.enabled and runs one command in the user session.
	Run(ctx context.Context, name string, value *int) (session.Result, error)
	// AudioView is the audio block of a command reply's own reading,
	// stamped now.
	AudioView(a useraction.Audio) status.Audio
}

// Presets runs a registered preset (#109, §10).
type Presets interface {
	// Run starts p in the user session; nil means it was started.
	Run(ctx context.Context, p config.Preset, by string) error
	// Record makes the attempt the last command, with its result
	// ("started" or the error code).
	Record(p config.Preset, from, result string)
}

// Notifier shows a PC notification (#106): the text rules, the per-source
// rate limit and the user-action run.
type Notifier interface {
	Send(ctx context.Context, cfg config.NotifyPCConfig, source, title, text string) (action.NotifyResult, error)
}
