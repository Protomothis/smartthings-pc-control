package service

import "github.com/Protomothis/smartthings-pc-control/service/status"

// The status blocks (service/status) under the names the service code has
// always used.
type (
	stGrace       = status.Grace
	stLastCommand = status.LastCommand
	stUpdate      = status.Update
	stWoL         = status.WoL
	stWoLSelected = status.WoLSelected
	stWoLAdapter  = status.WoLAdapter
	stSession     = status.Session
	awakeView     = status.AwakeView
	stAwake       = status.Awake
	stActivity    = status.Activity
	stActivityApp = status.ActivityApp
	stAudio       = status.Audio
	stMedia       = status.Media
	stPresetRef   = status.PresetRef
)
