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
	"strings"
	"sync"
	"time"
)

var (
	logger     *log.Logger
	logFile    *os.File
	logPath    string
	logMu      sync.Mutex
)

const maxLogSize = 1 * 1024 * 1024 // 1MB
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
}

var defaultConfig = Config{
	Port:   5001,
	Secret: "",
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
		return cfg
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		// No config file, use defaults
		return cfg
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		logMsg("WARNING: config.json 파싱 실패 (기본값 사용): %v", err)
		fmt.Fprintf(os.Stderr, "WARNING: config.json parse error (using defaults): %v\n", err)
		cfg = defaultConfig
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		logMsg("WARNING: invalid port %d, using default 5001", cfg.Port)
		cfg.Port = 5001
	}
	return cfg
}

// validatePort checks if a port number is valid (1-65535).
// Returns an error message or empty string if valid.
func validatePort(port int) string {
	if port < 1 || port > 65535 {
		return fmt.Sprintf("port must be between 1 and 65535 (got %d)", port)
	}
	return ""
}

func saveConfig(cfg Config) error {
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
	return nil
}

// newCommandHandler returns the HTTP handler for processing SmartThings commands.
func newCommandHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		var command string

		if liveCfg.Secret != "" {
			// With secret: /{secret}/{command}
			if len(parts) < 2 || parts[0] != liveCfg.Secret {
				logMsg("Request: %s /***/%s from %s (UNAUTHORIZED)", r.Method, strings.Join(parts[1:], "/"), r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

		cmd, ok := Commands[strings.ToLower(command)]
		if !ok {
			http.Error(w, "Unknown command: "+command, http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, cmd.Response)
		logMsg("Command: %s", strings.ToLower(command))
		if cmd.Execute != nil {
			go cmd.Execute()
		}
	}
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

func executeCommand(name string, args ...string) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("exec [%s %v] error: %v - output: %s", name, args, err, string(output))
	} else {
		logMsg("exec [%s %v] ok", name, args)
	}
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

func executePowerShell(script string) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("powershell error: %v - output: %s", err, string(output))
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
	Ready      bool         `json:"ready"` // true if at least one active adapter has WoL enabled
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
	wolOutput, _ := wolCmd.CombinedOutput()

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

// getExternalIP queries an external service for the public IP
func getExternalIP() string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	ip := strings.TrimSpace(string(body[:n]))
	if net.ParseIP(ip) != nil {
		return ip
	}
	return ""
}


// Schedule support
type ScheduledTask struct {
	Command   string    `json:"command"`
	ExecuteAt time.Time `json:"executeAt"`
	timer     *time.Timer
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
	return map[string]interface{}{
		"active":     true,
		"command":    scheduledTask.Command,
		"executeAt":  scheduledTask.ExecuteAt.Format(time.RFC3339),
		"remainingSec": int(remaining),
	}
}

// setSchedule creates a new scheduled task
func setSchedule(command string, delayMinutes int) error {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()

	// Cancel existing schedule
	if scheduledTask != nil && scheduledTask.timer != nil {
		scheduledTask.timer.Stop()
		scheduledTask = nil
	}

	cmd, ok := Commands[command]
	if !ok {
		return fmt.Errorf("unknown command: %s", command)
	}

	delay := time.Duration(delayMinutes) * time.Minute
	executeAt := time.Now().Add(delay)

	timer := time.AfterFunc(delay, func() {
		logMsg("Scheduled command executing: %s", command)
		if cmd.Execute != nil {
			cmd.Execute()
		}
		scheduleMu.Lock()
		scheduledTask = nil
		scheduleMu.Unlock()
	})

	scheduledTask = &ScheduledTask{
		Command:   command,
		ExecuteAt: executeAt,
		timer:     timer,
	}

	logMsg("Scheduled: %s in %d minutes (at %s)", command, delayMinutes, executeAt.Format("15:04:05"))
	return nil
}

// cancelSchedule cancels the current scheduled task
func cancelSchedule() bool {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()

	if scheduledTask == nil {
		return false
	}
	scheduledTask.timer.Stop()
	logMsg("Schedule cancelled: %s", scheduledTask.Command)
	scheduledTask = nil
	return true
}
