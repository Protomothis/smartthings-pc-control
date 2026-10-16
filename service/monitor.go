package service

// Monitor control over DDC/CI (#144): brightness, input and power of the
// primary monitor from SmartThings, Telegram and the app, and the
// "monitors" status block. Opt-in (monitor.enabled).
//
// DDC goes through the monitors' own I²C link, so it only works from the
// session on the physical console (an RDP session's displays are
// virtual): every reading and command is a `user-action monitor …` run
// there, like the screen commands. A reading costs tens to hundreds of
// milliseconds per value and a second or two for a capability string, so
// the status never waits for one: it serves the last reading and starts a
// new one in the background at most once a minute (monitorEvery), and a
// command's reply updates the store at once, with a full reading
// monitorAfterCommand later (an input switch or a power change takes the
// monitor a moment).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/devstate"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// monitorEvery is the least time between two background readings.
	monitorEvery = 60 * time.Second
	// monitorTimeout bounds one run: a `get` asks every monitor for its
	// capability string, which some take seconds to answer.
	monitorTimeout = 15 * time.Second
)

// monitorAfterCommand is how long after a command the full reading
// follows.
const monitorAfterCommand = 3 * time.Second

// monitorFollowUp schedules that reading; the tests replace it.
var monitorFollowUp = func() {
	time.AfterFunc(monitorAfterCommand, func() { requestMonitorReading(true) })
}

// monitorActions is the user-action runner with the monitor's longer
// timeout. A console run never touches the target bookkeeping.
var monitorActions = func() session.Runner {
	r := userActions
	r.Timeout = monitorTimeout
	return r
}()

// monitorRun runs `user-action monitor …` in the console session; the
// tests replace it.
var monitorRun userActionFunc = func(ctx context.Context, args ...string) (UserActionResult, error) {
	return monitorActions.Run(ctx, sessionConsole, args...)
}

// monitorStore is the newest reading.
type monitorStore struct {
	mu   sync.Mutex
	list []useraction.Monitor
	at   time.Time
	gate devstate.Refresh
	// failing is set after a failed reading, so a lasting failure is
	// logged once.
	failing bool
}

var monitors monitorStore

// last returns a copy of the newest reading; ok is false before the first.
func (s *monitorStore) last() ([]useraction.Monitor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at.IsZero() {
		return nil, false
	}
	out := make([]useraction.Monitor, len(s.list))
	for i, m := range s.list {
		out[i] = m.Clone()
	}
	return out, true
}

// set stores a full reading.
func (s *monitorStore) set(list []useraction.Monitor, at time.Time) {
	s.mu.Lock()
	s.list, s.at = list, at
	s.mu.Unlock()
}

// merge folds one monitor read back after a command into the reading. The
// reply has no capability string, so the input list of the stored entry
// is kept (plus the current input when it is new).
func (s *monitorStore) merge(m useraction.Monitor) useraction.Monitor {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.list {
		if old.ID != m.ID {
			continue
		}
		inputs := slices.Clone(old.Inputs)
		if m.Input != nil && !slices.Contains(inputs, *m.Input) {
			inputs = append(inputs, *m.Input)
		}
		m.Inputs = inputs
		m.Name, m.Primary = old.Name, old.Primary
		s.list[i] = m
		return m.Clone()
	}
	return m.Clone()
}

// reset forgets everything (tests).
func (s *monitorStore) reset() {
	s.mu.Lock()
	s.list, s.at, s.failing = nil, time.Time{}, false
	s.mu.Unlock()
	s.gate.Reset()
}

// noteFailure logs a failed reading once until one works again.
func (s *monitorStore) noteFailure(err error) {
	s.mu.Lock()
	first := !s.failing
	s.failing = true
	s.mu.Unlock()
	if first {
		logMsg("Monitor: reading failed: %v", err)
	}
}

func (s *monitorStore) noteSuccess() {
	s.mu.Lock()
	was := s.failing
	s.failing = false
	s.mu.Unlock()
	if was {
		logMsg("Monitor: reading works again")
	}
}

// monitorsFromReply reads the "monitors" of a `monitor get` reply,
// dropping any entry that fails Validate.
func monitorsFromReply(res UserActionResult) ([]useraction.Monitor, error) {
	raw, ok := res.Fields["monitors"]
	if !ok {
		return nil, fmt.Errorf("%w: monitor get without monitors", errUserActionOutput)
	}
	var list []useraction.Monitor
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%w: monitors: %v", errUserActionOutput, err)
	}
	out := make([]useraction.Monitor, 0, len(list))
	for _, m := range list {
		if err := m.Validate(); err != nil {
			logMsg("Monitor: ignoring %q in the reading: %v", m.ID, err)
			continue
		}
		out = append(out, m.Clone())
	}
	return out, nil
}

// readMonitors runs `monitor get` and stores the reading. Nobody at the
// console empties the store: the commands would answer no_user_session.
func readMonitors(ctx context.Context) ([]useraction.Monitor, error) {
	res, err := monitorRun(ctx, useraction.ActionMonitor, "get")
	if err == nil {
		var list []useraction.Monitor
		if list, err = monitorsFromReply(res); err == nil {
			monitors.set(list, time.Now())
			monitors.noteSuccess()
			return list, nil
		}
	}
	if errors.Is(err, errNoConsoleSession) || errors.Is(err, errNoUserSession) {
		monitors.set([]useraction.Monitor{}, time.Now())
	}
	return nil, err
}

// requestMonitorReading starts a background reading unless one runs or
// the last began less than monitorEvery ago; force skips the interval.
func requestMonitorReading(force bool) {
	if !monitors.gate.Begin(time.Now(), monitorEvery, force) {
		return
	}
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), monitorTimeout+time.Second)
			if _, err := readMonitors(ctx); err != nil {
				monitors.noteFailure(err)
			}
			cancel()
			if !monitors.gate.End(time.Now()) {
				return
			}
		}
	}()
}

// stMonitorsStatus is the status block: nil while monitor.enabled is off,
// the newest reading otherwise ([] before the first one). A status read
// keeps the reading fresh unless fromCache.
func stMonitorsStatus(cfg Config, fromCache bool) *[]status.Monitor {
	if !cfg.Monitor.Enabled {
		return nil
	}
	if !fromCache {
		requestMonitorReading(false)
	}
	list, _ := monitors.last()
	if list == nil {
		list = []useraction.Monitor{}
	}
	return &list
}

// primaryMonitor is the monitor the commands act on: the stored primary,
// or a fresh reading's when there is none stored yet.
func primaryMonitor(ctx context.Context) (useraction.Monitor, error) {
	list, ok := monitors.last()
	if !ok || len(list) == 0 {
		var err error
		if list, err = readMonitors(ctx); err != nil {
			return useraction.Monitor{}, err
		}
	}
	for _, m := range list {
		if m.Primary {
			return m, nil
		}
	}
	if len(list) > 0 {
		return list[0], nil
	}
	return useraction.Monitor{}, action.ErrMonitorUnsupported
}

// runMonitorCommand checks monitor.enabled and the value, sets it on the
// primary monitor and returns the monitor read back. err is
// action.ErrMonitorDisabled, *action.ValueError,
// action.ErrMonitorUnsupported, or a runner error.
func runMonitorCommand(ctx context.Context, name, value string) (useraction.Monitor, error) {
	if !getConfig().Monitor.Enabled {
		return useraction.Monitor{}, action.ErrMonitorDisabled
	}
	flag, err := action.MonitorArgs(name, value)
	if err != nil {
		return useraction.Monitor{}, err
	}
	primary, err := primaryMonitor(ctx)
	if err != nil {
		return useraction.Monitor{}, err
	}
	if !primary.DDC {
		return useraction.Monitor{}, action.ErrMonitorUnsupported
	}
	args := append([]string{useraction.ActionMonitor, "set", "--id", primary.ID}, flag...)
	res, err := monitorRun(ctx, args...)
	// Read everything again shortly, whatever happened: a refused power on
	// may still have woken the monitor, and a moved layout renumbers them.
	monitorFollowUp()
	if err != nil {
		return useraction.Monitor{}, err
	}
	var m useraction.Monitor
	if raw, ok := res.Fields["monitor"]; !ok || json.Unmarshal(raw, &m) != nil || m.Validate() != nil {
		return useraction.Monitor{}, fmt.Errorf("%w: monitor set without a valid monitor", errUserActionOutput)
	}
	return monitors.merge(m), nil
}

// monitorControl is the monitor control for /st/v1, the app and the bot.
type monitorControl struct{}

func (monitorControl) Monitors(cfg Config, fromCache bool) *[]status.Monitor {
	return stMonitorsStatus(cfg, fromCache)
}

func (monitorControl) Run(ctx context.Context, name, value string) (status.Monitor, error) {
	return runMonitorCommand(ctx, name, value)
}

// Read reads the monitors now (Telegram's /source without an argument).
func (monitorControl) Read(ctx context.Context) ([]status.Monitor, error) {
	if !getConfig().Monitor.Enabled {
		return nil, action.ErrMonitorDisabled
	}
	return readMonitors(ctx)
}
