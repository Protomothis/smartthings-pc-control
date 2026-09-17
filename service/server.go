package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

const (
	// maskedTokenPrefix is what secret.Mask produces; a POSTed token that
	// starts with it is the GET placeholder echoed back, not a new token.
	maskedTokenPrefix = "****"
	// clearTokenSentinel in a POSTed bot_token removes the stored token.
	clearTokenSentinel = "-"
)

var (
	logger  *log.Logger
	logFile *os.File
	logPath string
	logMu   sync.Mutex
)

const maxLogSize = 512 * 1024 // 512KB
const maxLogBackups = 3

func initLogger() {
	logMu.Lock()
	defer logMu.Unlock()

	if logger != nil {
		return // Already initialized
	}

	exePath, err := os.Executable()
	if err != nil {
		return
	}
	logPath = filepath.Join(filepath.Dir(exePath), "service.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	logFile = f
	logger = log.New(f, "", log.LstdFlags)
}

func closeLogger() {
	logMu.Lock()
	defer logMu.Unlock()

	if logFile != nil {
		logFile.Close()
		logFile = nil
		logger = nil
	}
}

// rotateLog must be called with logMu held.
func rotateLog() {
	if logFile == nil || logPath == "" {
		return
	}
	info, err := logFile.Stat()
	if err != nil || info.Size() < maxLogSize {
		return
	}

	// Close current log
	logFile.Close()

	// Rotate: .3 삭제, .2→.3, .1→.2, current→.1
	for i := maxLogBackups; i >= 1; i-- {
		src := logPath
		if i > 1 {
			src = fmt.Sprintf("%s.%d", logPath, i-1)
		}
		dst := fmt.Sprintf("%s.%d", logPath, i)
		os.Remove(dst)
		os.Rename(src, dst)
	}

	// Open new log file
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logFile = nil
		logger = nil
		return
	}
	logFile = f
	logger = log.New(f, "", log.LstdFlags)
}

func logMsg(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()

	if logger != nil {
		logger.Printf(format, args...)
		rotateLog()
	}
}

func maskSecret(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) <= 4 {
		return "***"
	}
	return s[:2] + "***" + s[len(s)-2:]
}

// Config holds the service configuration
type Config struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
	// WebUIRemote exposes the WebUI on all interfaces (LAN) instead of
	// 127.0.0.1 only. Requires a secret; applied on service restart.
	WebUIRemote bool `json:"webui_remote"`
	// ShutdownGrace defers power commands from SmartThings (shutdown,
	// restart, suspend, hibernate) by GraceSeconds so the user can cancel
	// from the tray app. forceshutdown always runs immediately.
	ShutdownGrace bool `json:"shutdown_grace"`
	// GraceSeconds is the length of that grace period. 0 (key missing in
	// an older config.json, or omitted by a client) means the default.
	GraceSeconds int `json:"grace_seconds"`
	// Telegram is the notification channel (v1.0, design doc §10).
	Telegram TelegramConfig `json:"telegram"`
	// SmartThings holds the Edge driver settings (edge-driver doc §4.7).
	SmartThings SmartThingsConfig `json:"smartthings"`
	// Notify says which Category.Kind events are sent. Missing entries
	// mean the catalogue default; loadConfig/saveConfig store the full map.
	Notify notify.Config `json:"notify"`
}

// TelegramConfig is the "telegram" object in config.json (design doc §10).
type TelegramConfig struct {
	Enabled bool `json:"enabled"`
	// BotToken is stored as "dpapi:BASE64" (issue #65); plaintext is
	// accepted and re-encrypted on the next save. Consumers must call
	// secret.Unprotect (see liveBotToken) — never use this value directly.
	// /api/config POST: empty, omitted or the masked form keeps the current
	// token; "-" clears it (see normalizeConfig).
	BotToken       string            `json:"bot_token"`
	ChatID         string            `json:"chat_id"`
	ControlEnabled bool              `json:"control_enabled"`
	AllowedChatIDs []string          `json:"allowed_chat_ids"` // empty: only chat_id
	Detail         string            `json:"detail"`           // "simple" | "full"
	Lang           string            `json:"lang"`             // "ko" | "en"
	PCName         string            `json:"pc_name"`          // empty: hostname
	QuietHours     notify.QuietHours `json:"quiet_hours"`
}

// SmartThingsConfig is the "smartthings" object in config.json
// (edge-driver doc §4.7). Hot-reloaded: every /st/v1 request reads the
// live value, so a save takes effect without a restart.
type SmartThingsConfig struct {
	// Discovery answers SSDP M-SEARCH probes (#69). A missing key in an
	// older config.json keeps the default true, because loadConfig decodes
	// over defaultConfig.
	Discovery bool `json:"discovery"`
	// AllowedHubs restricts /st/v1/* to these source IPs. Empty (the
	// default) allows any source that knows the secret.
	AllowedHubs []string `json:"allowed_hubs"`
	// ExposeSession opts into the session block of GET /st/v1/status
	// (lock state and idle time); ExposeSessionUser additionally reveals
	// the user name. Both default to off.
	ExposeSession     bool `json:"expose_session"`
	ExposeSessionUser bool `json:"expose_session_user"`
}

// withDefaults normalises the slice field; Discovery cannot be defaulted
// here (false is a legitimate value) and relies on decoding over defaults.
func (s SmartThingsConfig) withDefaults() SmartThingsConfig {
	if s.AllowedHubs == nil {
		s.AllowedHubs = []string{}
	} else {
		s.AllowedHubs = slices.Clone(s.AllowedHubs)
	}
	return s
}

var defaultConfig = Config{
	Port:          5001,
	Secret:        "",
	WebUIRemote:   false,
	ShutdownGrace: true, // missing key in config.json keeps this default
	GraceSeconds:  defaultGraceSeconds,
	Telegram: TelegramConfig{
		AllowedChatIDs: []string{},
		Detail:         "full",
		Lang:           "ko",
		// Missing quiet_hours keys keep these because loadConfig decodes
		// over defaultConfig (security_bypass/digest default true).
		QuietHours: notify.QuietHours{Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true},
	},
	SmartThings: SmartThingsConfig{
		// A missing "smartthings" object (an older config.json) keeps SSDP
		// discovery on; everything else stays off/empty.
		Discovery:   true,
		AllowedHubs: []string{},
	},
	// Notify stays nil here (a nil map means "all defaults" and must not be
	// shared between copies); withDefaults materialises the catalogue.
}

// withDefaults fills the telegram/notify values a client or an older
// config.json may omit. It never aliases maps or slices of the receiver.
func (c Config) withDefaults() Config {
	c.Telegram = c.Telegram.withDefaults()
	c.SmartThings = c.SmartThings.withDefaults()
	c.Notify = c.Notify.WithDefaults()
	return c
}

// withDefaults normalises the string fields; the bools cannot be defaulted
// here (false is a legitimate value) and rely on decoding over defaults.
func (t TelegramConfig) withDefaults() TelegramConfig {
	def := defaultConfig.Telegram
	if t.Detail != "simple" && t.Detail != "full" {
		t.Detail = def.Detail
	}
	if t.Lang != "ko" && t.Lang != "en" {
		t.Lang = def.Lang
	}
	if t.AllowedChatIDs == nil {
		t.AllowedChatIDs = []string{}
	} else {
		t.AllowedChatIDs = slices.Clone(t.AllowedChatIDs)
	}
	if t.QuietHours.Start == "" {
		t.QuietHours.Start = def.QuietHours.Start
	}
	if t.QuietHours.End == "" {
		t.QuietHours.End = def.QuietHours.End
	}
	return t
}

// forUpdate returns a copy of the live config for a POST body to be
// decoded over, so keys the client omits (an older GUI knows nothing of
// telegram/notify) keep their current values. Reference fields are
// cleared first so decoding cannot write into maps/slices other
// goroutines are reading; normalizeConfig restores them when still nil.
func (c Config) forUpdate() Config {
	c.Notify = nil
	c.Telegram.AllowedChatIDs = nil
	c.SmartThings.AllowedHubs = nil
	return c
}

// graceDuration returns the configured grace period, falling back to the
// default when the value is missing or out of range.
func (c Config) graceDuration() time.Duration {
	if validateGraceSeconds(c.GraceSeconds) != "" {
		return time.Duration(defaultGraceSeconds) * time.Second
	}
	return time.Duration(c.GraceSeconds) * time.Second
}

// Global config with RWMutex for hot-reload support
var (
	currentConfig Config
	configMu      sync.RWMutex
)

// getConfig returns the current configuration (thread-safe).
func getConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return currentConfig
}

// setConfig updates the current configuration in memory (thread-safe).
func setConfig(cfg Config) {
	configMu.Lock()
	defer configMu.Unlock()
	currentConfig = cfg
}

func loadConfig() Config {
	cfg := defaultConfig

	exePath, err := os.Executable()
	if err != nil {
		return cfg.withDefaults()
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		// No config file, use defaults
		return cfg.withDefaults()
	}

	// Decoding over defaultConfig keeps the default for every missing key
	// (shutdown_grace, telegram.quiet_hours.security_bypass, ...).
	if err := json.Unmarshal(data, &cfg); err != nil {
		logMsg("WARNING: config.json 파싱 실패 (기본값 사용): %v", err)
		fmt.Fprintf(os.Stderr, "WARNING: config.json parse error (using defaults): %v\n", err)
		cfg = defaultConfig
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		logMsg("WARNING: invalid port %d, using default 5001", cfg.Port)
		cfg.Port = 5001
	}
	// A DPAPI-protected token that this machine cannot decrypt (config.json
	// copied from another PC) is unusable: blank it so the GUI shows "not
	// set" and the user re-enters it. The load itself still succeeds.
	if secret.IsProtected(cfg.Telegram.BotToken) {
		if _, err := secret.Unprotect(cfg.Telegram.BotToken); err != nil {
			logMsg("WARNING: telegram.bot_token cannot be decrypted on this machine (config copied from another PC?); token cleared, enter it again: %v", err)
			cfg.Telegram.BotToken = ""
		}
	}
	return cfg.withDefaults()
}

// validatePort checks if a port number is valid (1-65535).
// Returns an error message or empty string if valid.
func validatePort(port int) string {
	if port < 1 || port > 65535 {
		return fmt.Sprintf("port must be between 1 and 65535 (got %d)", port)
	}
	return ""
}

// validateGraceSeconds returns a message when the grace period is outside
// the accepted range. 0 is rejected here — callers that mean "default"
// normalise first (see normalizeConfig).
func validateGraceSeconds(sec int) string {
	if sec < minGraceSeconds || sec > maxGraceSeconds {
		return fmt.Sprintf("grace_seconds must be between %d and %d (got %d)", minGraceSeconds, maxGraceSeconds, sec)
	}
	return ""
}

// normalizeConfig fills in values a client may legitimately omit: a
// missing/zero grace_seconds keeps the current (or default) period so an
// older WebUI page or config.json does not silently reset it.
func normalizeConfig(cfg Config, current Config) Config {
	if cfg.GraceSeconds == 0 {
		cfg.GraceSeconds = current.GraceSeconds
		if cfg.GraceSeconds == 0 {
			cfg.GraceSeconds = defaultGraceSeconds
		}
	}
	// Bot token rules (design doc §10, issue #63): the GET side hands the
	// GUI a masked form ("****1234"), so an empty/omitted token or the
	// masked placeholder sent back means "keep the stored one". The literal
	// "-" clears it; anything else is a new (plaintext) token, which
	// saveConfig encrypts.
	switch tok := cfg.Telegram.BotToken; {
	case tok == "" || strings.HasPrefix(tok, maskedTokenPrefix):
		cfg.Telegram.BotToken = current.Telegram.BotToken
	case tok == clearTokenSentinel:
		cfg.Telegram.BotToken = ""
	}
	// nil here means the key was absent from the body (forUpdate cleared
	// it before decoding, and "[]"/"{}" decode to non-nil): keep current.
	if cfg.Telegram.AllowedChatIDs == nil {
		cfg.Telegram.AllowedChatIDs = current.Telegram.AllowedChatIDs
	}
	if cfg.SmartThings.AllowedHubs == nil {
		cfg.SmartThings.AllowedHubs = current.SmartThings.AllowedHubs
	}
	if cfg.Notify == nil {
		cfg.Notify = current.Notify
	}
	return cfg.withDefaults()
}

// configChangedKeys lists the security-relevant settings that differ
// between old and new, for the security.config_changed event. Values are
// never included — only key names.
func configChangedKeys(old, new Config) []string {
	var keys []string
	add := func(key string, changed bool) {
		if changed {
			keys = append(keys, key)
		}
	}
	add("secret", old.Secret != new.Secret)
	add("port", old.Port != new.Port)
	add("webui_remote", old.WebUIRemote != new.WebUIRemote)
	add("telegram.enabled", old.Telegram.Enabled != new.Telegram.Enabled)
	add("telegram.bot_token", old.Telegram.BotToken != new.Telegram.BotToken)
	add("telegram.chat_id", old.Telegram.ChatID != new.Telegram.ChatID)
	add("telegram.control_enabled", old.Telegram.ControlEnabled != new.Telegram.ControlEnabled)
	add("telegram.allowed_chat_ids", !slices.Equal(old.Telegram.AllowedChatIDs, new.Telegram.AllowedChatIDs))
	add("telegram.detail", old.Telegram.Detail != new.Telegram.Detail)
	add("telegram.lang", old.Telegram.Lang != new.Telegram.Lang)
	add("telegram.pc_name", old.Telegram.PCName != new.Telegram.PCName)
	add("telegram.quiet_hours", old.Telegram.QuietHours != new.Telegram.QuietHours)
	add("smartthings.discovery", old.SmartThings.Discovery != new.SmartThings.Discovery)
	add("smartthings.allowed_hubs", !slices.Equal(old.SmartThings.AllowedHubs, new.SmartThings.AllowedHubs))
	add("smartthings.expose_session", old.SmartThings.ExposeSession != new.SmartThings.ExposeSession)
	add("smartthings.expose_session_user", old.SmartThings.ExposeSessionUser != new.SmartThings.ExposeSessionUser)
	return keys
}

func saveConfig(cfg Config) error {
	// Always persist the full catalogue and defaults so config.json
	// documents every key.
	cfg = cfg.withDefaults()
	// Issue #65: never write the bot token in plaintext. A protection
	// failure is logged and the save proceeds with plaintext rather than
	// losing the user's other changes; the next save retries.
	if tok := cfg.Telegram.BotToken; tok != "" && !secret.IsProtected(tok) {
		if enc, err := secret.Protect(tok); err != nil {
			logMsg("WARNING: telegram.bot_token could not be DPAPI-protected, saving as plaintext: %v", err)
		} else {
			cfg.Telegram.BotToken = enc
		}
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return err
	}
	// Update in-memory config
	setConfig(cfg)
	// Telegram control follows the saved settings without a restart
	// (no-op unless the service has started it, see telegram_control.go).
	reconcileTelegramControl()
	return nil
}

// newCommandHandler returns the HTTP handler for processing SmartThings commands.
func newCommandHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		var command string

		from := remoteHost(r.RemoteAddr)

		if liveCfg.Secret != "" {
			// With secret: /{secret}/{command}
			if len(parts) < 2 || parts[0] != liveCfg.Secret {
				logMsg("Request: %s /***/%s from %s (UNAUTHORIZED)", r.Method, strings.Join(parts[1:], "/"), r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				// count/window are filled by the aggregation stage (#58).
				emit("security", "unauthorized", map[string]string{
					"from": from,
					"path": truncate("/***/"+strings.Join(parts[1:], "/"), 64),
				})
				return
			}
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
			grace := liveCfg.graceDuration()
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
	// The /st/v1 tree (edge-driver doc §4) shares the command port; a more
	// specific pattern wins over "/", so the legacy /{secret}/{command}
	// handler still sees everything else.
	registerSTRoutes(mux)
	mux.HandleFunc("/", newCommandHandler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
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

// executeCommand runs name with args for the catalogue command `command`
// (shutdown, restart, ...) and logs the outcome; a failure also raises
// system.exec_failed naming that command.
func executeCommand(command string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("exec [%s %v] error: %v - output: %s", name, args, err, string(output))
		reportExecFailure(command, err, output)
	} else {
		logMsg("exec [%s %v] ok", name, args)
	}
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

func executeCommandWithLog(label string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("exec [%s] error: %v - output: %s", label, err, string(output))
	} else {
		logMsg("exec [%s] ok - output: %s", label, string(output))
	}
}

// executePowerShell runs script for the catalogue command `command`; see
// executeCommand for the failure notification.
func executePowerShell(command string, script string) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("powershell error: %v - output: %s", err, string(output))
		reportExecFailure(command, err, output)
	} else {
		logMsg("powershell ok - output: %s", string(output))
	}
}

func lockAllSessions() {
	// Get active session IDs using WTS API via PowerShell and disconnect them
	// tsdiscon disconnects a session which forces lock screen
	script := "$ErrorActionPreference = 'Continue'; " +
		"$output = @(); " +
		"$procs = Get-Process -Name explorer -ErrorAction SilentlyContinue; " +
		"$output += \"Found explorer processes: $($procs.Count)\"; " +
		"foreach ($p in $procs) { " +
		"$sid = $p.SessionId; " +
		"$output += \"Disconnecting session $sid\"; " +
		"$r = tsdiscon $sid 2>&1; " +
		"$output += \"Result: $r\" }; " +
		"$output -join \"`n\""
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("lockAllSessions error: %v - output: %s", err, string(output))
	} else {
		logMsg("lockAllSessions success - output: %s", string(output))
	}
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
	wolScript := "Get-NetAdapterPowerManagement | Select-Object @{N='MAC';E={(Get-NetAdapter $_.Name).MacAddress}}, WakeOnMagicPacket | ConvertTo-Json -Compress"
	wolCmd := exec.Command("powershell", "-NoProfile", "-Command", wolScript)
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
	resp, err := client.Get("https://api.ipify.org")
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
	go func() {
		if err := trayAppLauncher(); err != nil {
			logMsg("Tray app wake failed for %s (grace toast may not appear): %v", command, err)
			emit("system", "tray_wake_failed", map[string]string{"command": command, "error": err.Error()})
			return
		}
		logMsg("Tray app launched in user session for %s grace toast", command)
	}()
}

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

// formatDelay renders a delay for log lines: whole minutes as "5 min",
// anything shorter (or not a whole minute) as seconds ("30 sec").
func formatDelay(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%d sec", int(d/time.Second))
}

// cancelSchedule cancels the current scheduled task on behalf of the local
// API (app/WebUI/toast all go through /api/schedule DELETE).
func cancelSchedule() bool {
	return cancelScheduleBy("api")
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
