package service

// What the service asks of Windows and of the user session, and the clocks
// its device stores stamp samples with, gathered in three values whose
// fields the tests replace (#127, refactor-plan §3.2: one struct each
// instead of a package variable per seam). The surfaces' own seams are
// fields of stSrv, webSrv, sources and heartbeat.

import (
	"context"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// clock is the time of the device stores.
var clock = struct {
	// audio stamps the audio and media samples and judges their freshness.
	audio func() time.Time
	// idle stamps the idle heartbeat.
	idle func() time.Time
}{
	audio: time.Now,
	idle:  time.Now,
}

// userActionFunc is one `user-action` run in a user session.
type userActionFunc func(ctx context.Context, args ...string) (UserActionResult, error)

// userRun is the user-action run behind each feature (runUserAction, in
// the session the commands act on), so a test can fake one feature's
// replies without the others.
var userRun = struct {
	media  userActionFunc // volume, mute, media keys, media info
	notify userActionFunc // the PC notification (#106)
	preset userActionFunc // presets (#109)
	// screen runs in the session on the physical console (setScreen).
	screen userActionFunc
}{
	media:  runUserAction,
	notify: runUserAction,
	preset: runUserAction,
	screen: consoleUserAction,
}

// sys is the rest of the service's reach into Windows; the tests replace
// the parts that must never run for real (suspend, netsh, wevtutil).
var sys = struct {
	// tool runs a System32 tool (runSystemTool).
	tool func(tool string, args ...string) ([]byte, error)
	// netsh runs netsh advfirewall (firewall.go).
	netsh netshRunner
	// suspend is power.SetSuspendState.
	suspend func(hibernate bool) error
	// disconnect is session.Disconnect (the lock command).
	disconnect func(session uint32) error
	// shutdownLog returns the newest User32/1074 record (shutdown_reason.go).
	shutdownLog func(ctx context.Context) ([]byte, error)
	// trayLaunch starts the tray app in the user's session (grace toasts).
	trayLaunch func() error
	// processes lists the running processes' file names (Toolhelp).
	processes func() ([]string, error)
	// sessionPresent reports whether someone is logged in.
	sessionPresent func() bool
	// mediaRefresh reads the media session again shortly after a media
	// command.
	mediaRefresh func()
}{
	tool:           runSystemTool,
	netsh:          runNetshCommand,
	suspend:        power.SetSuspendState,
	disconnect:     session.Disconnect,
	shutdownLog:    runLocalShutdownQuery,
	trayLaunch:     launchTrayApp,
	processes:      toolhelpProcessNames,
	sessionPresent: userSessionPresent,
	mediaRefresh:   refreshMediaSoon,
}
