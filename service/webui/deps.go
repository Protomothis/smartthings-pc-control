package webui

import (
	"context"
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

// Deps is everything the WebUI and /api read and drive. The root service
// fills it once; every func and method is called per request, so a saved
// setting takes effect without a restart.
type Deps struct {
	// Config is the live configuration; SaveConfig stores one and makes it
	// live (config.Save plus what follows a save).
	Config     func() config.Config
	SaveConfig func(config.Config) error
	// Version is this build's version.
	Version func() string
	// Emit raises a notification (security.config_changed).
	Emit func(category, kind string, fields map[string]string)
	// Restart restarts the service (POST /api/restart-service).
	Restart func()
	// Logins is the failed-login lockout the WebUI login shares with the
	// legacy /{secret}/{command} URL (#120).
	Logins *ratelimit.Lockout
	// UserToken is WTSQueryUserToken: who is logged on to a session, for
	// the local login's "is it that session's own user" check (#131).
	UserToken func(session uint32) (windows.Token, error)

	Status    Status
	Hub       Hub
	Commands  Commands
	Awake     Awake
	Media     Media
	Presets   Presets
	Notify    Notifier
	Telegram  Telegram
	Heartbeat Heartbeat
}

// Status is what the pages and /api report about this PC.
type Status interface {
	MachineID() string
	Hostname() string
	Update() status.Update
	Display() string
	// Session describes the target user session (WTS).
	Session() (session.Info, error)
	Battery() status.Battery
	// Schedule is the schedule slot in the /api/schedule wire form.
	Schedule() map[string]any
	// WoLScan is the adapter scan behind /api/wol-status.
	WoLScan() status.WoLStatus
	// Processes is the picker list of running programs (#110).
	Processes() ([]string, error)
}

// Hub is the SmartThings side the app's SmartThings section shows (#67,
// #95, #96): the driver's last contact, the discovery responder and the
// WoL adapter choice.
type Hub interface {
	HubLastSeen() (status.HubSeen, bool)
	WoLView(cfg config.SmartThingsConfig) (status.WoL, *status.WoLSelected)
	SSDPRunning() bool
	LastSSDPSearch() (status.SSDPSearch, bool)
	// SSDPFirewallRule reports whether the inbound UDP 1900 rule was found.
	SSDPFirewallRule() bool
}

// Commands is the command catalogue and the schedule slot on behalf of
// the UIs on this PC (origin "ui").
type Commands interface {
	// RunNow runs a catalogue command at once for by (app, tray, toast,
	// webui); false for a name the catalogue does not have.
	RunNow(name, by string) bool
	// Schedule arms command to run after delay.
	Schedule(command string, delay time.Duration) error
	// CancelSchedule ends the schedule on behalf of by; false when there
	// was none.
	CancelSchedule(by string) bool
}

// Awake is the keep-awake controller (#111).
type Awake interface {
	View() status.AwakeView
	TurnOn(minutes int) (status.AwakeView, error)
	TurnOff() (v status.AwakeView, wasOn bool, err error)
	// Now is the controller's clock.
	Now() time.Time
}

// Media is the volume, mute and media-key commands and the state the
// command tab's media card shows (#104, #117).
type Media interface {
	Run(ctx context.Context, name string, value *int) (session.Result, error)
	// AudioView is the audio block of a command reply's own reading.
	AudioView(a useraction.Audio) status.Audio
	// SessionPresent reports whether someone is logged in.
	SessionPresent() bool
	Audio(cfg config.Config) status.Audio
	Media(cfg config.Config) status.Media
}

// Presets runs a preset in the user session (#109).
type Presets interface {
	Run(ctx context.Context, p config.Preset, by string) error
}

// Notifier shows a PC notification (#106).
type Notifier interface {
	Send(ctx context.Context, cfg config.NotifyPCConfig, source, title, text string) (action.NotifyResult, error)
}

// Telegram is what the notify tab's helpers need (#63, #75).
type Telegram interface {
	// Client builds a Bot API client for a plaintext token.
	Client(token string) *telegram.Client
	// PCName is the message footer name (telegram.pc_name or the hostname).
	PCName(tg config.TelegramConfig) string
	// Polling reports whether the control poller runs.
	Polling() bool
	// Conflict reports whether getUpdates keeps answering 409 because
	// another PC polls the same bot, and since when.
	Conflict() (bool, time.Time)
}

// Heartbeat stores what the tray app reports (#77, #103, #117).
type Heartbeat interface {
	// Now is the clock the audio and media samples are stamped with.
	Now() time.Time
	// Target is the session the commands act on; only its tray app's
	// heartbeats are stored.
	Target() (uint32, error)
	// Ignored notes a heartbeat from another session.
	Ignored(from, target uint32)
	Idle(seconds int64)
	Audio(a useraction.Audio, at time.Time)
	Media(np useraction.NowPlaying, at time.Time)
}
