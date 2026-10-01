package service

import (
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// The tests here run real notify buses against a fake Bot API, and the
// production one-second send gap turned every multi-message test into a
// multi-second wait (and, against 3-second poll deadlines, a flaky one:
// TestGraceCallbackEditsThroughPollerOnly waits for four sends). The gap
// itself is covered by the notify package's own tests with a fake clock.
// An init function runs before any test starts a bus, so this write cannot
// race with a bus worker reading the value.
func init() { notify.MinSendGap = time.Millisecond }
