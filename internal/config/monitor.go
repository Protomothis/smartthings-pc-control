package config

// MonitorConfig is the "monitor" object in config.json (#144): DDC/CI
// control of the monitors — brightness, input and power — from
// SmartThings, Telegram and the app's command tab. Opt-in, default off: a
// missing object keeps it off, and the one switch also decides whether the
// status carries the "monitors" block.
type MonitorConfig struct {
	Enabled bool `json:"enabled"`
}
