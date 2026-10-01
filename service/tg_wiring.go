package service

// The Telegram bot (service/tgcontrol) wired to the service.

import (
	"context"
	"slices"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/tgcontrol"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// tgCtl is the service's inbound Telegram control.
var tgCtl = tgcontrol.New(tgcontrol.Deps{
	Config:        getConfig,
	Version:       func() string { return Version },
	Emit:          func(cat, kind string, fields map[string]string) { emit(cat, kind, fields) },
	BaseURL:       func() string { return telegramBaseURL },
	Bus:           currentBus,
	StartedAt:     serviceStartedAt,
	ActionTimeout: func() time.Duration { return userActions.Timeout },
	Status:        sources,
	Commands:      tgCommands{},
	Awake:         awakeControl{},
	Media:         mediaControl{},
	Presets:       presetControl{origin: "telegram"},
	Notify:        pcNotifier{},
})

// tgCommands is the catalogue and the schedule slot on behalf of the bot:
// origin "telegram", at once — the bot asked for a confirmation (or a
// delay) already.
type tgCommands struct{}

func (tgCommands) Known(name string) bool {
	_, ok := Commands[name]
	return ok
}

func (tgCommands) GraceCommands() []string {
	names := make([]string, 0, len(graceCommands))
	for name := range graceCommands {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (tgCommands) RunNow(name string) bool {
	_, ok := dispatchCommand(name, "telegram", originTelegram, dispatchImmediate)
	return ok
}

func (tgCommands) Schedule(name string, delay time.Duration) error {
	return setSchedule(name, delay, originTelegram)
}

func (tgCommands) Take(by string) (string, bool)       { return takeSchedule(by) }
func (tgCommands) TakeForRun(by string) (string, bool) { return takeScheduleForRun(by) }

func (tgCommands) GraceSeq(command string) (uint64, bool) {
	return activeScheduleSeq(command, originRemote)
}

// LastRemote is the last remote command for the bot's /status.
func (*serviceSources) LastRemote() tgcontrol.Remote {
	lr := getLastRemote()
	return tgcontrol.Remote{Command: lr.Command, From: lr.From, At: lr.At}
}

// ReadAudio asks the user session for the playback device's state (/vol).
func (mediaControl) ReadAudio(ctx context.Context) (useraction.Audio, error) {
	return readAudioNow(ctx)
}

// ReadNowPlaying asks the user session for the media session (/np).
func (mediaControl) ReadNowPlaying(ctx context.Context) (useraction.NowPlaying, error) {
	return readNowPlayingNow(ctx)
}
