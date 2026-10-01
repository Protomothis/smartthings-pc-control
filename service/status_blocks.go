package service

import "github.com/Protomothis/smartthings-pc-control/service/status"

// The status blocks (service/status) under the names the service code has
// always used.
type (
	stUpdate   = status.Update
	awakeView  = status.AwakeView
	stActivity = status.Activity
	stAudio    = status.Audio
	stMedia    = status.Media
)

// The adapter scan (service/status) under its old names.
type (
	WoLStatus  = status.WoLStatus
	WoLAdapter = status.NetAdapter
)
