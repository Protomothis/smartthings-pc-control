package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// This file holds the power/system notification hooks of issue #60:
// power.started, power.resumed, system.updated and system.update_available
// (power.stopping is emitted directly from the SCM handler in windows.go).

// stateFileName sits next to the exe, like config.json, and remembers what
// the service must know across restarts.
const stateFileName = "state.json"

// serviceState is the content of state.json.
type serviceState struct {
	// LastVersion is the Version of the previous run; a different current
	// Version means the binary was updated (system.updated).
	LastVersion string `json:"last_version"`
	// LastNotifiedTag is the newest release tag already announced with
	// system.update_available, so a 24h re-check stays silent.
	LastNotifiedTag string `json:"last_notified_tag,omitempty"`
}

// stateMu serialises state.json access between the startup hook and the
// update checker.
var stateMu sync.Mutex

// statePath returns the state.json path next to the exe, or "" when the exe
// path is unknown (then the state is simply not persisted).
func statePath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exePath), stateFileName)
}

// loadState reads path; a missing or unreadable file is an empty state.
func loadState(path string) serviceState {
	var st serviceState
	if path == "" {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		logMsg("WARNING: %s 파싱 실패 (무시): %v", stateFileName, err)
		return serviceState{}
	}
	return st
}

// saveState writes st to path (no-op for "").
func saveState(path string, st serviceState) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// isReleaseVersion reports whether v is a tagged build ("v1.2.3"); "dev"
// and "" never take part in version comparisons.
func isReleaseVersion(v string) bool {
	_, ok := release.ParseVersion(v)
	return ok
}

// recordVersion compares version with state.json's last_version, emits
// system.updated when a previous, different version is recorded, and then
// stores version. Non-release versions leave the file untouched. Reports
// whether an event was emitted.
func recordVersion(path, version string) bool {
	if !isReleaseVersion(version) {
		return false
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	st := loadState(path)
	updated := st.LastVersion != "" && st.LastVersion != version
	if updated {
		logMsg("Version changed: %s → %s", st.LastVersion, version)
		emit("system", "updated", map[string]string{
			"version":  version,
			"previous": st.LastVersion,
		})
	}
	if st.LastVersion != version {
		st.LastVersion = version
		if err := saveState(path, st); err != nil {
			logMsg("WARNING: %s 저장 실패: %v", stateFileName, err)
		}
	}
	return updated
}

// Release check schedule (design doc §3: "서비스가 24h마다 확인").
const (
	updateCheckFirst = 10 * time.Minute
	updateCheckEvery = 24 * time.Hour
)

// updateChecker polls GitHub Releases and emits system.update_available
// once per newly seen tag.
type updateChecker struct {
	statePath string
	version   string
	url       string
	client    *http.Client
	first     time.Duration
	every     time.Duration
	// after replaces time.After so tests drive the schedule.
	after func(time.Duration) <-chan time.Time
}

// newUpdateChecker returns the production checker for version.
func newUpdateChecker(statePath, version string) *updateChecker {
	return &updateChecker{
		statePath: statePath,
		version:   version,
		url:       release.API,
		client:    &http.Client{Timeout: 15 * time.Second},
		first:     updateCheckFirst,
		every:     updateCheckEvery,
		after:     time.After,
	}
}

// run checks first after c.first, then every c.every, until stop closes.
func (c *updateChecker) run(stop <-chan struct{}) {
	if !isReleaseVersion(c.version) {
		return
	}
	wait := c.first
	for {
		select {
		case <-stop:
			return
		case <-c.after(wait):
		}
		c.check(context.Background())
		wait = c.every
	}
}

// check performs one release lookup and emits system.update_available
// when the latest tag is newer than c.version and not announced before.
// Reports whether an event was emitted.
func (c *updateChecker) check(ctx context.Context) bool {
	if !isReleaseVersion(c.version) {
		return false
	}
	rel, err := release.Fetch(ctx, c.client, c.url)
	if err != nil {
		logMsg("Update check failed: %v", err)
		return false
	}
	if !release.IsNewer(c.version, rel.TagName) {
		return false
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	st := loadState(c.statePath)
	if st.LastNotifiedTag == rel.TagName {
		return false
	}
	url := rel.HTMLURL
	if url == "" {
		url = release.Page
	}
	logMsg("Update available: %s (current %s)", rel.TagName, c.version)
	emit("system", "update_available", map[string]string{
		"version": rel.TagName,
		"current": c.version,
		"url":     url,
	})
	st.LastNotifiedTag = rel.TagName
	if err := saveState(c.statePath, st); err != nil {
		logMsg("WARNING: %s 저장 실패: %v", stateFileName, err)
	}
	return true
}

// startupHooks runs the once-per-start notifications after the servers
// have been launched: system.updated, power.started (in the background,
// so the external-IP lookup never delays startup) and the release checker.
func startupHooks(stop <-chan struct{}) {
	path := statePath()
	recordVersion(path, Version)
	go emitStarted()
	go newUpdateChecker(path, Version).run(stop)
}

// emitStarted emits power.started with the boot time and the public IP
// ("-" when the lookup fails). It blocks on the lookup (3s timeout) and is
// therefore run in its own goroutine.
func emitStarted() {
	ip := getExternalIP()
	if ip == "" {
		ip = "-"
	}
	version := Version
	if version == "" {
		version = "dev"
	}
	emit("power", "started", map[string]string{
		"version":     version,
		"boot_time":   bootTime().Format("2006-01-02 15:04:05"),
		"external_ip": ip,
	})
}

// bootTime derives the system boot time from the uptime counter
// (GetTickCount64, which keeps counting through sleep).
func bootTime() time.Time {
	return time.Now().Add(-windows.DurationSinceBoot())
}

// Power broadcast event types delivered with SERVICE_CONTROL_POWEREVENT
// (WinUser.h PBT_* values).
const (
	pbtAPMSuspend         = 0x0004 // PBT_APMSUSPEND
	pbtAPMResumeAutomatic = 0x0012 // PBT_APMRESUMEAUTOMATIC
)

// powerTracker turns the suspend/resume broadcasts into power.resumed with
// the time spent asleep.
type powerTracker struct {
	mu          sync.Mutex
	suspendedAt time.Time
	// now replaces time.Now in tests.
	now func() time.Time
}

// handle processes one power broadcast; every event type other than
// suspend and automatic resume is ignored. Reports whether power.resumed
// was emitted.
func (p *powerTracker) handle(eventType uint32) bool {
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch eventType {
	case pbtAPMSuspend:
		p.suspendedAt = now()
		logMsg("Power: suspending")
		return false
	case pbtAPMResumeAutomatic:
		since := "-"
		if !p.suspendedAt.IsZero() {
			since = formatSpan(now().Sub(p.suspendedAt))
			p.suspendedAt = time.Time{}
		}
		logMsg("Power: resumed from sleep (asleep %s)", since)
		emit("power", "resumed", map[string]string{"since": since})
		return true
	}
	return false
}

// formatSpan renders a sleep duration for people: "45 sec", "12 min",
// "3h 05m".
func formatSpan(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d sec", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d/time.Minute))
	default:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh %02dm", h, m)
	}
}
