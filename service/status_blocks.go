package service

import "github.com/Protomothis/smartthings-pc-control/service/status"

// The status blocks (service/status) under the names the service code has
// always used.
type (
	stGrace       = status.Grace
	stUpdate      = status.Update
	stWoLSelected = status.WoLSelected
	stWoLAdapter  = status.WoLAdapter
	stSession     = status.Session
	awakeView     = status.AwakeView
	stActivity    = status.Activity
	stActivityApp = status.ActivityApp
	stAudio       = status.Audio
	stMedia       = status.Media
)

// The adapter scan (service/status) under its old names.
type (
	WoLStatus  = status.WoLStatus
	WoLAdapter = status.NetAdapter
)
