package ui

// What the window shows, as plain values: the poller (poll.go) fills a
// snapshot from the service, the screens read a copy of it each frame, and
// the screenshot test fills one by hand.

import (
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// API is everything the window asks of the service. *gui.Client is the real
// one; tests use fakes.
type API interface {
	SetPort(webPort int)
	Login(secret string) error
	LocalLogin() error

	GetConfig() (gui.Config, error)
	SaveConfig(cfg gui.Config) (gui.SaveReply, error)
	Logs() ([]string, error)

	TestCommand(name string) (string, error)
	GetSchedule() (gui.Schedule, error)
	SetSchedule(command string, minutes int) error
	CancelSchedule(by string) error
	GetAwake() (gui.Awake, error)
	SetAwake(minutes int) (gui.Awake, error)
	AwakeOff() (gui.Awake, error)
	GetBattery() (gui.Battery, error)
	GetMedia() (gui.MediaState, error)
	MediaCommand(command string, value *int) (gui.MediaState, error)
	GetSTHub() (gui.STHub, error)
	TelegramState() (gui.TelegramState, error)
	RunPreset(slot int) error
	TestPreset(p gui.Preset) ([]gui.PresetWarning, error)
	GetWoLStatus() (gui.WoLStatus, error)
	RunningProcesses() ([]string, error)
	TestTelegram(token, chatID string) error
	TelegramMe() (username, name string, err error)
	TelegramChats(token string) ([]gui.TelegramChat, error)
	TestNotify() (gui.NotifyResult, error)
	RestartService() error
	SessionHeartbeat(hb gui.Heartbeat) (ignored bool, err error)
}

var _ API = (*gui.Client)(nil)

// connState is the connection light.
type connState int

const (
	connPending connState = iota // connecting
	connOK
	connLost
	connLogin // the service wants the secret
)

// snapshot is one consistent picture of the service.
type snapshot struct {
	conn connState
	port int // the API port the client talks to

	cfg    gui.Config
	cfgSet bool
	cfgGen uint64 // bumped by every config read; the forms adopt each once

	battery gui.Battery

	awake    gui.Awake
	awakeSet bool
	awakeErr error
	sched    gui.Schedule
	schedAt  time.Time // when sched was read; the countdown runs on from it
	media    gui.MediaState
	mediaErr error
	mediaSet bool
	hub      gui.STHub
	hubSet   bool
	tg       gui.TelegramState
	logs     []string
	logsSet  bool
	logsErr  error

	// updateTag is a newer release found by the update check, "" for
	// none; updateCheck is what the check learned (the update page uses it).
	updateTag   string
	updateCheck gui.UpdateCheck
}

// remaining is the schedule's time left at now.
func (s snapshot) remaining(now time.Time) time.Duration {
	if !s.sched.Active {
		return 0
	}
	left := time.Duration(s.sched.RemainingSec)*time.Second - now.Sub(s.schedAt)
	return max(left, 0)
}

// shareCount is how many kinds of information the PC shares with
// SmartThings and Telegram (the 공유 card's line).
func (s snapshot) shareCount() int {
	n := 0
	for _, on := range []bool{s.cfg.SmartThings.ExposeSession, s.cfg.Media.NowPlaying, s.cfg.Activity.Enabled} {
		if on {
			n++
		}
	}
	return n
}
