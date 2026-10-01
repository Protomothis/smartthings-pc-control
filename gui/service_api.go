package gui

// serviceAPI is everything the window asks of the service. *Client is the
// real one (the WebUI API on localhost); tests drive the window with fakes
// that embed the interface and implement only what they need.
type serviceAPI interface {
	// SetPort moves the client to another WebUI port (#121).
	SetPort(webPort int)
	Login(secret string) error

	GetConfig() (Config, error)
	SaveConfig(cfg Config) (string, error)
	Logs() ([]string, error)
	RestartService() error

	TestCommand(name string) (string, error)
	GetSchedule() (Schedule, error)
	SetSchedule(command string, minutes int) error
	CancelSchedule(by string) error
	GetAwake() (Awake, error)
	SetAwake(minutes int) (Awake, error)
	AwakeOff() (Awake, error)
	GetBattery() (Battery, error)
	GetMedia() (MediaState, error)
	MediaCommand(command string, value *int) (MediaState, error)
	SessionHeartbeat(body Heartbeat) (ignored bool, err error)

	GetWoLStatus() (WoLStatus, error)
	GetSTHub() (STHub, error)
	RunningProcesses() ([]string, error)

	TestTelegram(token, chatID string) error
	TelegramMe() (username, name string, err error)
	TelegramState() (TelegramState, error)
	TelegramChats(token string) ([]TelegramChat, error)

	TestNotify() (NotifyResult, error)
	RunPreset(slot int) error
	TestPreset(p Preset) error
}

var _ serviceAPI = (*Client)(nil)
