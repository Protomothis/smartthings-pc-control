package service

// The last remote command (status last_command, Telegram /status) and the
// notification bus the bot pauses and resumes.

import (
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// serviceStartedAt approximates process start for the /status uptime line.
var serviceStartedAt = time.Now()

// remoteRecord is the last SmartThings command, for /status.
type remoteRecord struct {
	Command string
	From    string
	// Origin is the path it arrived on: "remote" for the legacy
	// /{secret}/{command} URL, "smartthings" for /st/v1/command (#67).
	Origin string
	At     time.Time
	// Preset is set for a "preset" command (#109): which slot ran, under
	// which name, and the outcome ("started" or an error code).
	Preset *presetRecord
}

// presetRecord is the preset part of a remoteRecord.
type presetRecord struct {
	Slot   int
	Name   string
	Result string
}

// noteRemoteCommandBy records the last command and the path it arrived on
// (recordCommand in dispatch.go decides which commands count).
func noteRemoteCommandBy(command, from, origin string) {
	lastRemoteMu.Lock()
	lastRemote = remoteRecord{Command: command, From: from, Origin: origin, At: time.Now()}
	lastRemoteMu.Unlock()
}

// notePresetCommand records a preset run as the last remote command.
func notePresetCommand(p Preset, from, origin, result string) {
	lastRemoteMu.Lock()
	lastRemote = remoteRecord{Command: "preset", From: from, Origin: origin, At: time.Now(),
		Preset: &presetRecord{Slot: p.Slot, Name: p.Name, Result: result}}
	lastRemoteMu.Unlock()
}

func getLastRemote() remoteRecord {
	lastRemoteMu.Lock()
	defer lastRemoteMu.Unlock()
	return lastRemote
}

// currentBus returns the notification bus or nil (before startNotifier).
func currentBus() *notify.Bus {
	busMu.RLock()
	defer busMu.RUnlock()
	return bus
}

var (
	lastRemote   remoteRecord
	lastRemoteMu sync.Mutex
)
