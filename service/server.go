package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// httpReadHeaderTimeout is how long the command and WebUI servers wait
// for a client to finish sending its request headers.
const httpReadHeaderTimeout = 10 * time.Second

// initLogger opens service.log next to the exe (internal/logx); a second
// call while it is open does nothing.
func initLogger() { logx.Init(installDir()) }

// closeLogger closes it at service stop.
func closeLogger() { logx.Close() }

// logMsg writes one line to service.log.
func logMsg(format string, args ...any) { logx.Printf(format, args...) }

// newCommandHandler returns the HTTP handler for processing SmartThings commands.
func newCommandHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		var command string

		from := remoteHost(r.RemoteAddr)

		if liveCfg.Secret != "" {
			// With secret: /{secret}/{command}. A wrong secret counts like
			// a failed WebUI login (#120): five in a row lock the address
			// out for a minute, so the secret cannot be guessed at line rate.
			if !checkRateLimit(r.RemoteAddr) {
				logMsg("Request: %s /***/... from %s (RATE LIMITED)", r.Method, r.RemoteAddr)
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockDuration/time.Second)))
				http.Error(w, "Too many attempts", http.StatusTooManyRequests)
				return
			}
			if len(parts) < 2 || !secretEqual(parts[0], liveCfg.Secret) {
				recordLoginFailure(r.RemoteAddr)
				logMsg("Request: %s /***/%s from %s (UNAUTHORIZED)", r.Method, strings.Join(parts[1:], "/"), r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				// count/window are filled by the aggregation stage (#58).
				emit("security", "unauthorized", map[string]string{
					"from": from,
					"path": truncate("/***/"+strings.Join(parts[1:], "/"), 64),
				})
				return
			}
			resetLoginAttempts(r.RemoteAddr)
			command = parts[1]
		} else {
			// No secret: /{command}
			if len(parts) < 1 {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			command = parts[0]
		}

		logMsg("Request: %s /%s from %s", r.Method, command, r.RemoteAddr)

		name := strings.ToLower(command)
		cmd, ok := Commands[name]
		if !ok {
			http.Error(w, "Unknown command: "+command, http.StatusBadRequest)
			emit("security", "unknown_command", map[string]string{"from": from, "command": truncate(command, 64)})
			return
		}
		if name != "ping" {
			noteRemoteCommand(name, from) // shown by the Telegram /status command
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, cmd.Response)

		// Grace period: defer disruptive commands so the user can cancel
		// from the tray app. The HTTP response stays immediate for Edge
		// driver compatibility.
		if liveCfg.ShutdownGrace && graceCommands[name] {
			grace := liveCfg.GraceDuration()
			if err := setSchedule(name, grace, originRemote); err == nil {
				logMsg("Command: %s deferred %s (grace period — cancel from the app or tray)", name, formatDelay(grace))
				emit("remote", "grace_scheduled", map[string]string{
					"command":    name,
					"from":       from,
					"delay":      formatDelay(grace),
					"execute_at": time.Now().Add(grace).Format("15:04:05"),
				}, graceActions()...)
				return
			}
			logMsg("WARNING: grace scheduling failed for %s, executing immediately", name)
		}

		logMsg("Command: %s", name)
		switch {
		case name == "forceshutdown":
			emit("remote", "force", map[string]string{"from": from})
		case name != "ping": // ping is never notified
			emit("remote", "received", map[string]string{"command": name, "from": from})
		}
		if cmd.Execute != nil {
			go cmd.Execute()
		}
	}
}

// remoteHost strips the port from an http.Request.RemoteAddr for the
// "from" field of notifications.
func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// truncate shortens attacker-controlled strings (paths, command names) to
// max runes before they go into a notification.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// StartHTTPServer starts the HTTP server compatible with SmartThings Edge driver
func StartHTTPServer(stop chan struct{}) {
	// Initialize if not already done (e.g., console mode)
	initLogger()
	cfg := getConfig()
	if cfg.Port == 0 {
		cfg = loadConfig()
		setConfig(cfg)
	}
	logMsg("Service starting on port %d", cfg.Port)

	if cfg.Secret == "" {
		logMsg("WARNING: No secret configured. Anyone on your network can control this PC.")
	}

	mux := http.NewServeMux()
	// The /st/v1 tree (edge-driver doc §3) shares the command port; a more
	// specific pattern wins over "/", so the legacy /{secret}/{command}
	// handler still sees everything else.
	registerSTRoutes(mux)
	mux.HandleFunc("/", newCommandHandler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
		// Bounds only the request headers (gosec G112, Slowloris): a LAN
		// client that opens a connection and never finishes its headers no
		// longer holds it forever. Bodies and responses are not limited.
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	log.Printf("Listening on port %d", cfg.Port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Printf("HTTP server error: %v", err)
	}
}

// executeCommand runs the System32 tool with args for the catalogue
// command `command` (shutdown, restart, ...) and logs the outcome; a
// failure also raises system.exec_failed naming that command.
func executeCommand(command string, tool string, args ...string) {
	notePowerCommand(command) // hint for power.stopping's reason (§3.5)
	output, err := runTool(tool, args...)
	if err != nil {
		logMsg("exec [%s %v] error: %v - output: %s", tool, args, err, string(output))
		reportExecFailure(command, err, output)
	} else {
		logMsg("exec [%s %v] ok", tool, args)
	}
}

// runTool runs a System32 tool by absolute path (internal/systool) and
// returns its combined output. Replaced by the tests.
var runTool = func(tool string, args ...string) ([]byte, error) {
	return systool.Command(tool, args...).CombinedOutput()
}

// reportExecFailure emits system.exec_failed. The first line of output is
// appended when it is readable text (shutdown.exe writes in the console
// code page, which may not be UTF-8).
func reportExecFailure(command string, err error, output []byte) {
	msg := err.Error()
	if line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n"); line != "" && utf8.ValidString(line) {
		msg += ": " + truncate(strings.TrimSpace(line), 200)
	}
	emit("system", "exec_failed", map[string]string{"command": command, "error": msg})
}

// WoLAdapter represents a physical network adapter's WoL status
type WoLAdapter struct {
	Name       string   `json:"name"`
	MacAddress string   `json:"mac"`
	IPs        []string `json:"ips"`
	Status     string   `json:"status"` // "Up" or "Down"
	WoLEnabled bool     `json:"wolEnabled"`
	WoLCapable bool     `json:"wolCapable"`
}

// WoLStatus is the response for /api/wol-status
type WoLStatus struct {
	Adapters   []WoLAdapter `json:"adapters"`
	ExternalIP string       `json:"externalIP,omitempty"`
	Ready      bool         `json:"ready"`             // true if at least one active adapter has WoL enabled
	Warning    string       `json:"warning,omitempty"` // non-fatal warning (e.g., WoL query failed)
	Error      string       `json:"error,omitempty"`
}

// getWoLStatus queries all network adapters for WoL capability using Go net + PowerShell for WoL only
func getWoLStatus() WoLStatus {
	result := WoLStatus{}

	// Get network interfaces using Go standard library (no encoding issues)
	ifaces, err := net.Interfaces()
	if err != nil {
		return WoLStatus{Error: fmt.Sprintf("failed to list interfaces: %v", err)}
	}

	// Filter to real adapters (has MAC, not loopback, not point-to-point)
	type ifaceInfo struct {
		Name string
		MAC  string
		IPs  []string
		Up   bool
	}
	var realAdapters []ifaceInfo

	for _, iface := range ifaces {
		// Skip loopback, no-MAC (virtual), and point-to-point interfaces
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac == "" {
			continue
		}

		info := ifaceInfo{
			Name: iface.Name,
			MAC:  formatMAC(mac),
			Up:   iface.Flags&net.FlagUp != 0,
		}

		// Get IP addresses
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, _, _ := net.ParseCIDR(addr.String())
			if ip != nil && !ip.IsLinkLocalUnicast() {
				info.IPs = append(info.IPs, ip.String())
			}
		}

		// Only include adapters that have IPs or are up (skip internal virtual ones)
		if len(info.IPs) > 0 || info.Up {
			realAdapters = append(realAdapters, info)
		}
	}

	if len(realAdapters) == 0 {
		return WoLStatus{Error: "no network adapters found"}
	}

	// Get WoL status via PowerShell (by MAC matching)
	// WakeOnMagicPacket: 0=Unsupported, 1=Disabled, 2=Enabled
	//
	// Per adapter, not one pipeline: Get-NetAdapterPowerManagement throws
	// "A device attached to the system is not functioning" on some Realtek
	// drivers, which used to fail the whole query and report WoL as off even
	// though the adapter's advanced property "*WakeOnMagicPacket" is enabled.
	// That property is the fallback (registry value 1 = enabled).
	wolScript := `Get-NetAdapter -Physical | ForEach-Object {
  $n = $_.Name; $v = 0
  try { $pm = Get-NetAdapterPowerManagement -Name $n -ErrorAction Stop; $v = [int]$pm.WakeOnMagicPacket } catch { $v = -1 }
  if ($v -ne 2) {
    $p = Get-NetAdapterAdvancedProperty -Name $n -RegistryKeyword '*WakeOnMagicPacket' -ErrorAction SilentlyContinue
    if ($p) { if ([int]($p.RegistryValue | Select-Object -First 1) -eq 1) { $v = 2 } elseif ($v -lt 1) { $v = 1 } }
    elseif ($v -lt 0) { $v = 0 }
  }
  [pscustomobject]@{ MAC = $_.MacAddress; WakeOnMagicPacket = $v }
} | ConvertTo-Json -Compress`
	wolCmd := systool.Command(systool.PowerShell, "-NoProfile", "-Command", wolScript)
	wolOutput, wolErr := wolCmd.CombinedOutput()
	if wolErr != nil {
		logMsg("WoL PowerShell query failed: %v", wolErr)
		result.Warning = "WoL status unavailable (requires admin privileges)"
	}

	type wolInfo struct {
		MAC               string `json:"MAC"`
		WakeOnMagicPacket int    `json:"WakeOnMagicPacket"`
	}
	wolMap := make(map[string]wolInfo)
	wolTrimmed := strings.TrimSpace(string(wolOutput))
	if len(wolTrimmed) > 0 {
		var wolList []wolInfo
		if wolTrimmed[0] == '[' {
			json.Unmarshal([]byte(wolTrimmed), &wolList)
		} else {
			var single wolInfo
			if json.Unmarshal([]byte(wolTrimmed), &single) == nil {
				wolList = []wolInfo{single}
			}
		}
		for _, w := range wolList {
			wolMap[strings.ToUpper(w.MAC)] = w
		}
	}

	for _, a := range realAdapters {
		wa := WoLAdapter{
			Name:       a.Name,
			MacAddress: a.MAC,
			IPs:        a.IPs,
			Status:     "Down",
		}
		if a.Up {
			wa.Status = "Up"
		}

		// Match by MAC (normalize to XX-XX-XX format)
		macKey := strings.ToUpper(strings.ReplaceAll(a.MAC, ":", "-"))
		if wol, ok := wolMap[macKey]; ok {
			wa.WoLCapable = wol.WakeOnMagicPacket != 0
			wa.WoLEnabled = wol.WakeOnMagicPacket == 2
		}
		result.Adapters = append(result.Adapters, wa)

		if a.Up && wa.WoLEnabled {
			result.Ready = true
		}
	}

	// Get external IP (best-effort, non-blocking with timeout)
	result.ExternalIP = getExternalIP()

	return result
}

// formatMAC converts "aa:bb:cc:dd:ee:ff" to "AA-BB-CC-DD-EE-FF"
func formatMAC(mac string) string {
	return strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
}

// External IP cache
var (
	cachedExternalIP   string
	externalIPCachedAt time.Time
	externalIPMu       sync.Mutex
)

const externalIPCacheTTL = 10 * time.Minute

// getExternalIP queries an external service for the public IP (cached 10min)
func getExternalIP() string {
	externalIPMu.Lock()
	defer externalIPMu.Unlock()

	if cachedExternalIP != "" && time.Since(externalIPCachedAt) < externalIPCacheTTL {
		return cachedExternalIP
	}

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return cachedExternalIP
	}
	resp, err := client.Do(req)
	if err != nil {
		// Return stale cache if available
		return cachedExternalIP
	}
	defer resp.Body.Close()
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	ip := strings.TrimSpace(string(body[:n]))
	if net.ParseIP(ip) != nil {
		cachedExternalIP = ip
		externalIPCachedAt = time.Now()
		return ip
	}
	return cachedExternalIP
}

// Schedule support
type ScheduledTask struct {
	Command   string    `json:"command"`
	ExecuteAt time.Time `json:"executeAt"`
	// Origin says who created the schedule (app/WebUI vs. a remote grace
	// deferral); the GUI labels the countdown with it (#54).
	Origin scheduleOrigin
	// Replaced describes the schedule this one displaced, if any, so the
	// UI can tell the user their own timer was overridden.
	Replaced *replacedSchedule
	timer    *time.Timer
	// seq identifies this schedule for the lifetime of the process, so the
	// Telegram grace message remembered for it (#62) is never edited on
	// behalf of a later schedule.
	seq uint64
}

// scheduleSeq is the last seq handed out; guarded by scheduleMu.
var scheduleSeq uint64

// replacedSchedule is the summary of a schedule that a newer one cancelled.
type replacedSchedule struct {
	Command string `json:"command"`
	Origin  string `json:"origin"`
}

var (
	scheduledTask *ScheduledTask
	scheduleMu    sync.Mutex
)

// getSchedule returns the current scheduled task info (or nil)
func getSchedule() map[string]interface{} {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()

	if scheduledTask == nil {
		return map[string]interface{}{"active": false}
	}
	remaining := time.Until(scheduledTask.ExecuteAt).Seconds()
	if remaining < 0 {
		remaining = 0
	}
	info := map[string]interface{}{
		"active":       true,
		"command":      scheduledTask.Command,
		"origin":       scheduledTask.Origin.String(),
		"executeAt":    scheduledTask.ExecuteAt.Format(time.RFC3339),
		"remainingSec": int(remaining),
	}
	if scheduledTask.Replaced != nil {
		info["replaced"] = scheduledTask.Replaced
	}
	return info
}

// scheduleOrigin records who asked for a schedule. It decides whether the
// service must wake the tray app: a remote (SmartThings) grace schedule
// needs a toast the user can see, while schedules from the app or WebUI
// already come from a UI the user is looking at.
type scheduleOrigin int

const (
	// originUI: created from the desktop app or the browser WebUI.
	originUI scheduleOrigin = iota
	// originRemote: a SmartThings command deferred by the grace period.
	originRemote
	// originTelegram: "/shutdown 30" and friends from the Telegram bot (#61).
	// The user asked from their phone, so no tray toast is needed.
	originTelegram
	// originSmartThings: an explicit schedule from the Edge driver
	// (POST /st/v1/command with minutes > 0, #67). Like Telegram, the user
	// is acting from their phone, so no tray toast is needed — a grace
	// deferral of an immediate SmartThings command stays originRemote,
	// because there the toast is the point.
	originSmartThings
)

// wakesTrayApp reports whether a schedule from this origin should launch
// the tray app so the [Run now]/[Cancel] toast appears.
func (o scheduleOrigin) wakesTrayApp() bool {
	return o == originRemote
}

// String is the wire form used by /api/schedule ("ui", "remote",
// "telegram" or "smartthings").
func (o scheduleOrigin) String() string {
	switch o {
	case originRemote:
		return "remote"
	case originTelegram:
		return "telegram"
	case originSmartThings:
		return "smartthings"
	}
	return "ui"
}

// trayAppLauncher starts the tray app in the user's session. A package
// variable so tests can stub it out instead of spawning processes.
var trayAppLauncher = launchTrayApp

// wakeTrayApp launches the tray app in the background and logs the outcome.
// It never affects the scheduled command: a failure (no user logged in,
// token error, ...) only means no toast is shown.
func wakeTrayApp(command string) {
	launch := trayAppLauncher // read before the goroutine: tests swap it back
	go func() {
		if err := launch(); err != nil {
			logMsg("Tray app wake failed for %s (grace toast may not appear): %v", command, err)
			emit("system", "tray_wake_failed", map[string]string{"command": command, "error": err.Error()})
			return
		}
		logMsg("Tray app launched in user session for %s grace toast", command)
	}()
}

const (
	// maxScheduleMinutes is the longest delay a schedule may carry, in
	// minutes: three days (#89). Every front end shares it — the SmartThings
	// driver's `schedule(minutes)` definition, /api/schedule, the WebUI form,
	// the Telegram `/shutdown N` argument and the app's schedule tab — so a
	// delay one of them offers is a delay the others can show and cancel.
	maxScheduleMinutes = 4320
	// maxScheduleDelay is the same ceiling as a duration.
	maxScheduleDelay = maxScheduleMinutes * time.Minute
)

// setSchedule creates a new scheduled task. origin says who requested it;
// remote grace schedules additionally wake the tray app (see wakeTrayApp).
func setSchedule(command string, delay time.Duration, origin scheduleOrigin) error {
	if err := scheduleTask(command, delay, origin); err != nil {
		return err
	}
	if origin.wakesTrayApp() {
		wakeTrayApp(command)
	}
	return nil
}

// scheduleTask arms the timer for command. There is a single schedule slot:
// an existing schedule is cancelled and remembered as Replaced on the new
// one, so the UI can say what was overridden (#54).
func scheduleTask(command string, delay time.Duration, origin scheduleOrigin) error {
	if delay <= 0 {
		return fmt.Errorf("invalid delay: %s", delay)
	}
	// #89: the ceiling every front end shares. The Edge driver, /api/schedule
	// and the Telegram bot all check it before they get here; this is the last
	// guard, so no caller can arm a timer the others could never show.
	if delay > maxScheduleDelay {
		return fmt.Errorf("delay too long: %s (at most %d minutes)", delay, maxScheduleMinutes)
	}
	scheduleMu.Lock()
	defer scheduleMu.Unlock()

	cmd, ok := Commands[command]
	if !ok {
		return fmt.Errorf("unknown command: %s", command)
	}

	// Cancel existing schedule
	var replaced *replacedSchedule
	if scheduledTask != nil && scheduledTask.timer != nil {
		scheduledTask.timer.Stop()
		replaced = &replacedSchedule{Command: scheduledTask.Command, Origin: scheduledTask.Origin.String()}
		logMsg("Schedule replaced: %s (%s) -> %s (%s)", scheduledTask.Command, scheduledTask.Origin, command, origin)
		emit("schedule", "replaced", map[string]string{
			"command": command, "origin": origin.String(),
			"old_command": replaced.Command, "old_origin": replaced.Origin,
		})
		finishGraceMessage(scheduledTask.seq, graceReplaced, "")
		scheduledTask = nil
	}

	executeAt := time.Now().Add(delay)
	scheduleSeq++
	seq := scheduleSeq

	timer := time.AfterFunc(delay, func() {
		logMsg("Scheduled command executing: %s", command)
		if origin == originRemote {
			emit("remote", "executed", map[string]string{"command": command})
		} else {
			emit("schedule", "executed", map[string]string{"command": command, "origin": origin.String()})
		}
		finishGraceMessage(seq, graceExecuted, "timer")
		if cmd.Execute != nil {
			cmd.Execute()
		}
		scheduleMu.Lock()
		if scheduledTask != nil && scheduledTask.seq == seq {
			scheduledTask = nil
		}
		scheduleMu.Unlock()
	})

	scheduledTask = &ScheduledTask{
		Command:   command,
		ExecuteAt: executeAt,
		Origin:    origin,
		Replaced:  replaced,
		timer:     timer,
		seq:       seq,
	}

	logMsg("Scheduled: %s in %s (at %s, origin %s)", command, formatDelay(delay), executeAt.Format("15:04:05"), origin)
	// A remote grace deferral is reported as remote.grace_scheduled by the
	// command handler (it knows the caller and carries the cancel buttons).
	if origin != originRemote {
		emit("schedule", "created", map[string]string{
			"command": command, "origin": origin.String(),
			"delay": formatDelay(delay), "execute_at": executeAt.Format("15:04:05"),
		})
	}
	return nil
}

// formatDelay renders a delay for log lines and notifications: whole minutes
// as "5 min", anything shorter (or not a whole minute) as seconds ("30 sec").
// #89: schedules now reach three days, and "4320 min" is not a number anyone
// reads as three days, so from an hour on it climbs the units — "2 h",
// "1 h 30 min", "1 d", "1 d 3 h".
func formatDelay(d time.Duration) string {
	if d < time.Minute || d%time.Minute != 0 {
		return fmt.Sprintf("%d sec", int(d/time.Second))
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	case minutes < 1440:
		if rest := minutes % 60; rest != 0 {
			return fmt.Sprintf("%d h %d min", minutes/60, rest)
		}
		return fmt.Sprintf("%d h", minutes/60)
	default:
		// The odd minutes are noise at a day's distance.
		if hours := (minutes % 1440) / 60; hours != 0 {
			return fmt.Sprintf("%d d %d h", minutes/1440, hours)
		}
		return fmt.Sprintf("%d d", minutes/1440)
	}
}

// cancelScheduleBy cancels the current scheduled task. by says who asked
// (api/webui/app/toast/tray/telegram) and is reported in the notification:
// remote.grace_cancelled for a grace deferral, schedule.cancelled otherwise.
func cancelScheduleBy(by string) bool {
	_, ok := takeSchedule(by)
	return ok
}

// takeSchedule cancels the current scheduled task on behalf of by and
// returns its command.
func takeSchedule(by string) (string, bool) {
	return endSchedule(by, false)
}

// takeScheduleForRun ends the current scheduled task because by is about
// to execute its command right away (/now, the runnow: button). The
// notifications are the same as a cancel; only the Telegram grace message
// is stamped "executed" instead of "cancelled". Taking the schedule under
// the lock means the caller never races a concurrent cancel or replacement.
func takeScheduleForRun(by string) (string, bool) {
	return endSchedule(by, true)
}

// endSchedule stops the timer, reports remote.grace_cancelled or
// schedule.cancelled, updates the Telegram grace message and clears the
// slot. runNow says whether the caller executes the command itself.
func endSchedule(by string, runNow bool) (string, bool) {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()

	if scheduledTask == nil {
		return "", false
	}
	scheduledTask.timer.Stop()
	command := scheduledTask.Command
	logMsg("Schedule cancelled: %s", command)
	if scheduledTask.Origin == originRemote {
		emit("remote", "grace_cancelled", map[string]string{"command": command, "by": by})
	} else {
		emit("schedule", "cancelled", map[string]string{
			"command": command, "origin": scheduledTask.Origin.String(), "by": by,
		})
	}
	result := graceCancelled
	if runNow {
		result = graceExecuted
	}
	finishGraceMessage(scheduledTask.seq, result, by)
	scheduledTask = nil
	return command, true
}

// activeScheduleSeq returns the seq of the current schedule when it runs
// command on behalf of origin; the grace-message hook uses it to tie a
// sent Telegram message to the schedule it announced (#62).
func activeScheduleSeq(command string, origin scheduleOrigin) (uint64, bool) {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()
	if scheduledTask == nil || scheduledTask.Command != command || scheduledTask.Origin != origin {
		return 0, false
	}
	return scheduledTask.seq, true
}
