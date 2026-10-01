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

		// The reply is the same whether the command runs now or waits out
		// the grace period (the user can cancel from the tray app), and it
		// goes out first, as it always has for the Edge driver.
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, cmd.Response)
		dispatchCommand(name, from, originRemote, dispatchDefault)
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
