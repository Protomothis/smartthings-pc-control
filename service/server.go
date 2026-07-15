package service

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var logger *log.Logger

func initLogger() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	logPath := filepath.Join(filepath.Dir(exePath), "service.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	logger = log.New(f, "", log.LstdFlags)
}

func logMsg(format string, args ...interface{}) {
	if logger != nil {
		logger.Printf(format, args...)
	}
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

	json.Unmarshal(data, &cfg)
	if cfg.Port == 0 {
		cfg.Port = 5001
	}
	return cfg
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
	return os.WriteFile(configPath, data, 0644)
}

// StartHTTPServer starts the HTTP server compatible with SmartThings Edge driver
func StartHTTPServer(stop chan struct{}) {
	initLogger()
	cfg := loadConfig()
	logMsg("Service starting on port %d", cfg.Port)

	mux := http.NewServeMux()

	// Route handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")
		logMsg("Request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

		var command string

		if cfg.Secret != "" {
			// With secret: /{secret}/{command}
			if len(parts) < 2 || parts[0] != cfg.Secret {
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

		switch strings.ToLower(command) {
		case "ping":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")
			logMsg("Command: ping - OK")

		case "shutdown":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Shutting down...")
			logMsg("Command: shutdown")
			go executeCommand("shutdown", "/s", "/t", "5")

		case "forceshutdown":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Force shutting down...")
			logMsg("Command: forceshutdown")
			go executeCommand("shutdown", "/s", "/f", "/t", "0")

		case "restart":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Restarting...")
			logMsg("Command: restart")
			go executeCommand("shutdown", "/r", "/t", "5")

		case "hibernate":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Hibernating...")
			logMsg("Command: hibernate")
			go executeCommand("shutdown", "/h")

		case "suspend":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Suspending...")
			logMsg("Command: suspend")
			go executePowerShell("Add-Type -Assembly System.Windows.Forms; [System.Windows.Forms.Application]::SetSuspendState('Suspend', $false, $false)")

		case "lock":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Locking...")
			logMsg("Command: lock")
			go lockAllSessions()

		case "turnscreenoff":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "Screen off...")
			logMsg("Command: turnscreenoff")
			go executePowerShell("(Add-Type '[DllImport(\"user32.dll\")] public static extern int SendMessage(int hWnd,int hMsg,int wParam,int lParam);' -Name a -Pas)::SendMessage(-1,0x0112,0xF170,2)")

		default:
			http.Error(w, "Unknown command: "+command, http.StatusBadRequest)
		}
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}

	go func() {
		<-stop
		server.Close()
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
		logMsg("executeCommand error [%s %v]: %v - output: %s", name, args, err, string(output))
	}
}

func executeCommandWithLog(label string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("executeCommandWithLog [%s] error: %v - output: %s", label, err, string(output))
	} else {
		logMsg("executeCommandWithLog [%s] success - output: %s", label, string(output))
	}
}

func executePowerShell(script string) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logMsg("executePowerShell error: %v - output: %s", err, string(output))
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
