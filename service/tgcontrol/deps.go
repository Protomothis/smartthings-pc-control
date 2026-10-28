// Package tgcontrol is inbound Telegram control (design doc §9): the slash
// commands and inline buttons of the bot, the poller's lifecycle, and the
// grace-schedule message that is edited when its schedule ends (#62).
//
// The package reads the service through the interfaces below and never
// imports the root service (#127): the root builds one Control with New,
// starts it at service start and reconciles it after every config save.
// Outbound notifications go through service/telegram's Sink, wired by the
// root.
package tgcontrol

import (
	"context"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/privacy"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Deps is everything the bot reads and drives. Every func and method is
// called per message, so a saved setting takes effect at once.
type Deps struct {
	// Config is the live configuration.
	Config func() config.Config
	// Version is this build's version (/status).
	Version func() string
	// Emit raises a notification (security.unknown_chat).
	Emit func(category, kind string, fields map[string]string)
	// BaseURL is where the Bot API clients point (telegram.DefaultBaseURL;
	// the tests use a fake server).
	BaseURL func() string
	// Bus is the notification bus, nil while it is not running (/quiet).
	// That is only while the service starts or stops.
	Bus func() *notify.Bus
	// Uptime is how long the PC has been up (/status), the same reading
	// the SmartThings status and the WebUI status card show (#147).
	Uptime func() time.Duration
	// ActionTimeout is how long a user-session action may take (/run, /say
	// wait a second more).
	ActionTimeout func() time.Duration
	// ModernStandby reports whether the PC sleeps in S0 low power idle
	// (#161, #162): the sleep and screen replies then say what that
	// means. Nil is false.
	ModernStandby func() bool

	Status   Status
	Commands Commands
	Awake    Awake
	Media    Media
	Presets  Presets
	Notify   Notifier

	// Privacy is the camera/microphone block (#143) for /status, nil
	// while privacy.enabled is off; a nil func reads the same.
	Privacy func(cfg config.Config) *status.Privacy
	// Guard judges a power command from the bot against the
	// camera/microphone guard (#143) before it runs; nil means no guard.
	Guard func(name string) privacy.Verdict
}

// Remote is the last remote command as /status shows it.
type Remote struct {
	Command string
	From    string
	At      time.Time
}

// Status is what /status reports.
type Status interface {
	Hostname() string
	// Schedule is the schedule slot in the /api/schedule wire form.
	Schedule() map[string]any
	// LastRemote is the last remote command; Command is "" before one.
	LastRemote() Remote
	Battery() status.Battery
	Activity(cfg config.Config) status.Activity
	Media(cfg config.Config) status.Media
	// Audio is the stored volume/mute sample (unavailable while
	// media.enabled is off or nobody is logged in).
	Audio(cfg config.Config) status.Audio
	// Session describes the target user session (WTS); IdleSeconds is the
	// tray app's idle reading, ok only while its heartbeat is fresh.
	Session() (session.Info, error)
	IdleSeconds() (int64, bool)
}

// Commands is the command catalogue and the schedule slot on behalf of
// Telegram (origin "telegram").
type Commands interface {
	// Known reports whether name is a catalogue command.
	Known(name string) bool
	// GraceCommands are the power commands the grace period defers.
	GraceCommands() []string
	// RunNow runs name at once; false for an unknown name.
	RunNow(name string) bool
	// Schedule arms name to run after delay.
	Schedule(name string, delay time.Duration) error
	// Take ends the schedule on behalf of by and returns its command.
	Take(by string) (string, bool)
	// TakeForRun ends it because by is about to run its command now.
	TakeForRun(by string) (string, bool)
	// GraceSeq is the seq of the active remote grace schedule for command.
	GraceSeq(command string) (uint64, bool)
}

// Awake is the keep-awake controller (#111).
type Awake interface {
	View() status.AwakeView
	TurnOn(minutes int) (status.AwakeView, error)
	TurnOff() (v status.AwakeView, wasOn bool, err error)
	// Now is the controller's clock.
	Now() time.Time
}

// Media runs the volume, mute and media commands in the user session
// (#104, #105, #117).
type Media interface {
	Run(ctx context.Context, name string, value *int) (session.Result, error)
	// ReadAudio asks the user session for the playback device's state.
	ReadAudio(ctx context.Context) (useraction.Audio, error)
	// ReadNowPlaying asks it for the media session.
	ReadNowPlaying(ctx context.Context) (useraction.NowPlaying, error)
}

// Presets runs a preset and records it as the last command (#109).
type Presets interface {
	Run(ctx context.Context, p config.Preset, by string) error
	Record(p config.Preset, from, result string)
}

// Notifier shows a PC notification (#106).
type Notifier interface {
	Send(ctx context.Context, cfg config.NotifyPCConfig, source, title, text string) (action.NotifyResult, error)
}

// Control is the bot: telegram.CommandHandler (and EditKeyboarder) on top
// of the service, the poller it runs, and the grace message.
type Control struct {
	d        Deps
	runner   runner
	conflict conflict
	grace    graceStore
}

var _ telegram.CommandHandler = (*Control)(nil)

// New builds the bot on d. Nothing polls until Start.
func New(d Deps) *Control {
	return &Control{d: d}
}
