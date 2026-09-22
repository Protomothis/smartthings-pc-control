package service

// SmartThings Edge driver protocol, /st/v1 (docs/design/edge-driver.md §3,
// issue #67). The routes live on the main command port next to the legacy
// PCControl path, so the driver needs no second port:
//
//	GET    /st/v1/status    §3.2
//	POST   /st/v1/command   §3.3
//	DELETE /st/v1/schedule  §3.4
//
// Authentication is the X-PC-Secret header (§3.1) — never the URL — plus an
// optional hub allow-list and a per-source-IP rate limit (§3.1).
// /st/v1/subscribe (§3.5) lives in st_push.go; /st/v1/description and the
// SSDP responder (§3.6) live in st_ssdp.go.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

const (
	// stProtocol is the wire version the driver compares against (§3.2).
	stProtocol = 1
	// stRatePerSecond is the token-bucket refill rate per source IP (§3.1).
	stRatePerSecond = 10
	// stDriverAgent is the User-Agent prefix the Edge driver sends,
	// "smartthings-pc-control-edge/<driver version>".
	stDriverAgent = "smartthings-pc-control-edge"
	// stHubStale is 2× the longest poll interval the driver offers (5 min,
	// §7), after which the GUI calls the hub disconnected.
	stHubStale = 10 * time.Minute
	// stMaxBody caps a command body; the JSON is a handful of fields.
	stMaxBody = 8 << 10
	// stMaxMinutes matches /api/schedule and the Telegram bot.
	stMaxMinutes = 1440
)

// ---- rate limiting (§3.1) --------------------------------------------------

// stBucket is one source IP's token bucket: stRatePerSecond tokens per
// second, burst stRatePerSecond.
type stBucket struct {
	tokens float64
	last   time.Time
}

var (
	stBuckets   = map[string]*stBucket{}
	stBucketsMu sync.Mutex
	// stNow is time.Now, replaced by tests that drive the bucket.
	stNow = time.Now
)

// stAllow reports whether ip may make one more request now.
func stAllow(ip string) bool {
	now := stNow()
	stBucketsMu.Lock()
	defer stBucketsMu.Unlock()

	b, ok := stBuckets[ip]
	if !ok {
		// Keep the map from growing with every probing source: entries
		// idle for a minute are worthless (they are full again anyway).
		if len(stBuckets) > 256 {
			for k, v := range stBuckets {
				if now.Sub(v.last) > time.Minute {
					delete(stBuckets, k)
				}
			}
		}
		stBuckets[ip] = &stBucket{tokens: stRatePerSecond - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * stRatePerSecond
	if b.tokens > stRatePerSecond {
		b.tokens = stRatePerSecond
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// resetSTRateLimit drops every bucket (tests, and a config reload).
func resetSTRateLimit() {
	stBucketsMu.Lock()
	stBuckets = map[string]*stBucket{}
	stBucketsMu.Unlock()
}

// ---- hub last seen ---------------------------------------------------------

// hubSeen is the last authenticated /st/v1 caller. The GUI SmartThings
// section (#70) shows it as "hub 192.168.1.20 · driver v1.0.0 · 3s ago".
type hubSeen struct {
	IP            string
	DriverVersion string
	At            time.Time
}

var (
	hubLastSeen   hubSeen
	hubLastSeenMu sync.RWMutex
)

// noteHubSeen records one authenticated request from ip.
func noteHubSeen(ip, userAgent string) {
	hubLastSeenMu.Lock()
	hubLastSeen = hubSeen{IP: ip, DriverVersion: driverVersionOf(userAgent), At: time.Now()}
	hubLastSeenMu.Unlock()
}

// hubLastSeenInfo returns the last authenticated hub contact; ok is false
// before the first one.
func hubLastSeenInfo() (hubSeen, bool) {
	hubLastSeenMu.RLock()
	defer hubLastSeenMu.RUnlock()
	return hubLastSeen, !hubLastSeen.At.IsZero()
}

// driverVersionOf extracts "1.0.0" from
// "smartthings-pc-control-edge/1.0.0"; any other User-Agent is kept
// verbatim (truncated) so an unexpected client is still recognisable.
func driverVersionOf(userAgent string) string {
	ua := strings.TrimSpace(userAgent)
	if name, version, ok := strings.Cut(ua, "/"); ok && name == stDriverAgent {
		return truncate(version, 32)
	}
	return truncate(ua, 64)
}

// ---- auth (§3.1) -----------------------------------------------------------

// stAuth wraps a /st/v1 handler with the rate limit, the hub allow-list and
// the header secret check, and records the hub on success. The secret is
// never logged (§8).
func stAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := getConfig()
		from := remoteHost(r.RemoteAddr)

		if !stAllow(from) {
			w.Header().Set("Retry-After", "1")
			stError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		if hubs := cfg.SmartThings.AllowedHubs; len(hubs) > 0 && !stHubAllowed(hubs, from) {
			logMsg("ST API: %s %s from %s rejected (not in allowed_hubs)", r.Method, r.URL.Path, from)
			emit("security", "unauthorized", map[string]string{
				"from": from,
				"path": truncate(r.URL.Path, 64),
			})
			stError(w, http.StatusForbidden, "hub not allowed")
			return
		}
		if cfg.Secret != "" && r.Header.Get("X-PC-Secret") != cfg.Secret {
			// The attempted value is deliberately not logged or notified.
			logMsg("ST API: %s %s from %s (UNAUTHORIZED)", r.Method, r.URL.Path, from)
			emit("security", "unauthorized", map[string]string{
				"from": from,
				"path": truncate(r.URL.Path, 64),
			})
			stError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		noteHubSeen(from, r.Header.Get("User-Agent"))
		next(w, r)
	}
}

// stHubAllowed reports whether from matches one of the configured hub
// addresses. Entries are compared as IPs when both parse (so "192.168.1.20"
// matches "::ffff:192.168.1.20"), otherwise as plain strings.
func stHubAllowed(hubs []string, from string) bool {
	fromIP := net.ParseIP(from)
	for _, h := range hubs {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h == from {
			return true
		}
		if hIP := net.ParseIP(h); hIP != nil && fromIP != nil && hIP.Equal(fromIP) {
			return true
		}
	}
	return false
}

// stError writes the {"error": ...} body the driver shows in pcInfo.
func stError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---- status (§3.2) ---------------------------------------------------------

type stStatusResponse struct {
	Protocol          int            `json:"protocol"`
	ServiceVersion    string         `json:"service_version"`
	MachineID         string         `json:"machine_id"`
	Hostname          string         `json:"hostname"`
	Power             string         `json:"power"`
	UptimeSeconds     int64          `json:"uptime_seconds"`
	LastShutdownClean bool           `json:"last_shutdown_clean"`
	SecretSet         bool           `json:"secret_set"`
	Grace             stGrace        `json:"grace"`
	Schedule          map[string]any `json:"schedule"`
	LastCommand       *stLastCommand `json:"last_command"`
	Update            stUpdate       `json:"update"`
	WoL               stWoL          `json:"wol"`
	Display           string         `json:"display"`
	Session           stSession      `json:"session"`
}

type stGrace struct {
	Enabled bool `json:"enabled"`
	Seconds int  `json:"seconds"`
}

type stLastCommand struct {
	Command string `json:"command"`
	Origin  string `json:"origin"`
	At      string `json:"at"`
}

type stUpdate struct {
	Available bool   `json:"available"`
	Latest    string `json:"latest"`
}

type stWoL struct {
	Ready    bool           `json:"ready"`
	Adapters []stWoLAdapter `json:"adapters"`
}

type stWoLAdapter struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	WoLEnabled bool   `json:"wol_enabled"`
	WoLCapable bool   `json:"wol_capable"`
}

// stSession is the opt-in session block (§3.2). Everything but Exposed is
// omitted while smartthings.expose_session is off; Locked is null when the
// service cannot read the session state, and IdleSeconds is null unless the
// tray app posted a heartbeat within idleHeartbeatTTL (#77).
type stSession struct {
	Exposed     bool   `json:"exposed"`
	Locked      *bool  `json:"locked,omitempty"`
	IdleSeconds *int64 `json:"idle_seconds,omitempty"`
	User        string `json:"user,omitempty"`
}

// stWoLProvider is getWoLStatus, replaced in tests (the real one shells out
// to PowerShell and queries the public IP).
var stWoLProvider = getWoLStatus

// wolCache keeps the adapter scan for a short while: the driver polls the
// status every 10–30s and each scan runs a PowerShell query.
var (
	wolCached   WoLStatus
	wolCachedAt time.Time
	wolCacheMu  sync.Mutex
)

const wolCacheTTL = time.Minute

// stWoLStatus returns the (cached) adapter scan mapped to the §3.2 shape.
func stWoLStatus() stWoL {
	wolCacheMu.Lock()
	if wolCachedAt.IsZero() || time.Since(wolCachedAt) > wolCacheTTL {
		wolCached = stWoLProvider()
		wolCachedAt = time.Now()
	}
	status := wolCached
	wolCacheMu.Unlock()

	out := stWoL{Ready: status.Ready, Adapters: []stWoLAdapter{}}
	for _, a := range status.Adapters {
		out.Adapters = append(out.Adapters, stWoLAdapter{
			Name:       a.Name,
			MAC:        a.MacAddress,
			WoLEnabled: a.WoLEnabled,
			WoLCapable: a.WoLCapable,
		})
	}
	return out
}

// stScheduleView is getSchedule() under the §3.2 key names. The wire form
// of /api/schedule (executeAt/remainingSec) stays as it is for the GUI.
func stScheduleView() map[string]any {
	s := getSchedule()
	if s["active"] != true {
		return map[string]any{"active": false}
	}
	return map[string]any{
		"active":            true,
		"command":           s["command"],
		"origin":            s["origin"],
		"remaining_seconds": s["remainingSec"],
		"execute_at":        s["executeAt"], // RFC3339, local offset
	}
}

// stUpdateInfo reports the newest release this service knows about. The
// periodic checker caches every lookup (#68), so once it has run "latest"
// is the real newest tag even when it is not newer than us; before the
// first check the only thing on record is the tag state.json says was
// announced with system.update_available (#60).
func stUpdateInfo() stUpdate {
	if tag := latestReleaseTag(); tag != "" {
		return stUpdate{Available: release.IsNewer(Version, tag), Latest: tag}
	}
	stateMu.Lock()
	st := loadState(statePath())
	stateMu.Unlock()
	if tag := st.LastNotifiedTag; tag != "" && release.IsNewer(Version, tag) {
		return stUpdate{Available: true, Latest: tag}
	}
	return stUpdate{Available: false, Latest: ""}
}

// machineID is HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid, the stable
// per-install identifier the driver keys its device on. It never changes
// while Windows is installed, so it is read once.
var (
	machineIDOnce  sync.Once
	machineIDValue string
)

func machineID() string {
	machineIDOnce.Do(func() {
		guid, err := readMachineGUID()
		if err != nil || guid == "" {
			logMsg("ST API: MachineGuid unreadable (%v); falling back to the hostname", err)
			machineIDValue = hostname()
			return
		}
		machineIDValue = guid
	})
	return machineIDValue
}

// readMachineGUID reads the Cryptography\MachineGuid value. WOW64_64KEY
// keeps a 32-bit build looking at the same key as a 64-bit one.
func readMachineGUID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", err
	}
	return guid, nil
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "PC"
}

// stSessionInfo builds the session block for the live config. The lock
// state and the user name come from WTS; the idle time comes from the tray
// app's heartbeat (#77) and is independent of them, so a machine with no
// tray app still reports the lock state and one with WTS refusing still
// reports the idle time.
func stSessionInfo(cfg SmartThingsConfig) stSession {
	if !cfg.ExposeSession {
		return stSession{Exposed: false}
	}
	out := stSession{Exposed: true}
	if idle, ok := lastIdleSeconds(); ok {
		out.IdleSeconds = &idle
	}
	info, err := querySessionInfo()
	if err != nil {
		// Nobody is logged in, or WTS refused: locked stays null rather
		// than guessing.
		logMsg("ST API: session info unavailable: %v", err)
		return out
	}
	locked := info.Locked
	out.Locked = &locked
	if cfg.ExposeSessionUser {
		out.User = info.User
	}
	return out
}

// handleSTStatus serves GET /st/v1/status.
func handleSTStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, buildSTStatus(getConfig()))
}

// buildSTStatus assembles the §3.2 status document for cfg. Push bodies
// carry the very same object (§3.5), so the driver never needs a diff.
func buildSTStatus(cfg Config) stStatusResponse {
	resp := stStatusResponse{
		Protocol:       stProtocol,
		ServiceVersion: Version,
		MachineID:      machineID(),
		Hostname:       hostname(),
		// Answering at all proves the PC is awake (§3.2); sleep and
		// shutdown are the driver's job to infer.
		Power: "on",
		// Uptime describes the PC, not this process: the driver shows it
		// next to the power state.
		UptimeSeconds:     int64(windows.DurationSinceBoot() / time.Second),
		LastShutdownClean: lastShutdownClean.Load(),
		SecretSet:         cfg.Secret != "",
		Grace:             stGrace{Enabled: cfg.ShutdownGrace, Seconds: int(cfg.graceDuration() / time.Second)},
		Schedule:          stScheduleView(),
		Update:            stUpdateInfo(),
		WoL:               stWoLStatus(),
		Display:           getDisplayState(),
		Session:           stSessionInfo(cfg.SmartThings),
	}
	if lr := getLastRemote(); lr.Command != "" {
		resp.LastCommand = &stLastCommand{
			Command: lr.Command,
			Origin:  lr.Origin,
			At:      lr.At.Format(time.RFC3339),
		}
	}
	return resp
}

// ---- command (§3.3) --------------------------------------------------------

type stCommandRequest struct {
	Command string `json:"command"`
	Mode    string `json:"mode"`    // "default" | "immediate" | "grace"
	Minutes int    `json:"minutes"` // > 0 schedules instead of running
}

type stCommandResponse struct {
	Accepted bool           `json:"accepted"`
	Executed bool           `json:"executed"`
	Schedule map[string]any `json:"schedule"`
}

// handleSTCommand serves POST /st/v1/command. It goes through the same
// registry, grace rules and events as the legacy path so Telegram keeps
// reporting SmartThings commands.
func handleSTCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body stCommandRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, stMaxBody)).Decode(&body); err != nil {
		stError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from := remoteHost(r.RemoteAddr)
	name := strings.ToLower(strings.TrimSpace(body.Command))
	cmd, ok := Commands[name]
	if !ok {
		logMsg("ST API: unknown command from %s", from)
		emit("security", "unknown_command", map[string]string{"from": from, "command": truncate(name, 64)})
		stError(w, http.StatusBadRequest, "unknown command")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	switch mode {
	case "", "default", "immediate", "grace":
	default:
		stError(w, http.StatusBadRequest, "unknown mode")
		return
	}
	if body.Minutes < 0 || body.Minutes > stMaxMinutes {
		stError(w, http.StatusBadRequest, "minutes must be between 0 and 1440")
		return
	}
	if name != "ping" {
		// Shown by the Telegram /status command and as last_command above.
		noteRemoteCommandBy(name, from, "smartthings")
	}

	// An explicit delay is a schedule the user set from their phone, so it
	// carries its own origin and needs no tray toast (§3.3).
	if body.Minutes > 0 {
		delay := time.Duration(body.Minutes) * time.Minute
		if err := setSchedule(name, delay, originSmartThings); err != nil {
			logMsg("ST API: scheduling %s failed: %v", name, err)
			stError(w, http.StatusBadRequest, err.Error())
			return
		}
		logMsg("ST API: %s scheduled in %s (from %s)", name, formatDelay(delay), from)
		writeJSON(w, http.StatusOK, stCommandResponse{Accepted: true, Schedule: stScheduleView()})
		return
	}

	// No delay: run now, or defer by the configured grace period so the
	// user at the PC can cancel. forceshutdown is always immediate (§3.3).
	cfg := getConfig()
	graceWanted := mode != "immediate" && name != "forceshutdown" && graceCommands[name] &&
		(cfg.ShutdownGrace || mode == "grace")
	if graceWanted {
		grace := cfg.graceDuration()
		// originRemote, like the legacy path: this deferral exists so the
		// tray toast appears, and the GUI already words it as SmartThings.
		if err := setSchedule(name, grace, originRemote); err == nil {
			logMsg("ST API: %s deferred %s (grace period)", name, formatDelay(grace))
			emit("remote", "grace_scheduled", map[string]string{
				"command":    name,
				"from":       from,
				"delay":      formatDelay(grace),
				"execute_at": time.Now().Add(grace).Format("15:04:05"),
			}, graceActions()...)
			writeJSON(w, http.StatusOK, stCommandResponse{Accepted: true, Schedule: stScheduleView()})
			return
		}
		logMsg("WARNING: ST API grace scheduling failed for %s, executing immediately", name)
	}

	logMsg("ST API: %s from %s", name, from)
	switch {
	case name == "forceshutdown":
		emit("remote", "force", map[string]string{"from": from})
	case name != "ping": // ping is never notified
		emit("remote", "received", map[string]string{"command": name, "from": from})
	}
	if cmd.Execute != nil {
		go cmd.Execute()
	}
	writeJSON(w, http.StatusOK, stCommandResponse{
		Accepted: true,
		Executed: true,
		Schedule: map[string]any{"active": false},
	})
}

// ---- schedule (§3.4) -------------------------------------------------------

// handleSTSchedule serves DELETE /st/v1/schedule.
func handleSTSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cancelled := cancelScheduleBy("smartthings")
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": cancelled})
}

// ---- wiring ----------------------------------------------------------------

// stHandler is the authenticated /st/v1 tree; tests serve it directly.
func stHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/st/v1/status", stAuth(handleSTStatus))
	mux.HandleFunc("/st/v1/command", stAuth(handleSTCommand))
	mux.HandleFunc("/st/v1/schedule", stAuth(handleSTSchedule))
	registerSTDescriptionRoute(mux) // #69, unauthenticated (see st_ssdp.go)
	registerSTPushRoutes(mux)       // /st/v1/subscribe (§3.5, #68)
	// Anything else under /st/v1 is a 404 rather than falling through to
	// the legacy /{secret}/{command} handler.
	mux.HandleFunc("/st/v1/", func(w http.ResponseWriter, r *http.Request) {
		stError(w, http.StatusNotFound, "not found")
	})
	return mux
}

// registerSTRoutes mounts the /st/v1 tree on the command server's mux.
func registerSTRoutes(mux *http.ServeMux) {
	mux.Handle("/st/v1/", stHandler())
}
