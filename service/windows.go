package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "RemoteShutdownService"
const serviceDisplayName = "Remote Shutdown Service"
const serviceDescription = "HTTP server for SmartThings PC shutdown control. Compatible with PCControl Edge driver."

type shutdownService struct {
	stop chan struct{}
}

func (s *shutdownService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Initialize config before starting servers
	initLogger()
	cfg := loadConfig()
	setConfig(cfg)

	s.stop = make(chan struct{})
	go StartHTTPServer(s.stop)
	go StartWebUI(s.stop)

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		c := <-r
		switch c.Cmd {
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			close(s.stop)
			closeLogger()
			return false, 0
		case svc.Interrogate:
			changes <- c.CurrentStatus
		}
	}
}

// RunService runs as a Windows service
func RunService() {
	err := svc.Run(serviceName, &shutdownService{})
	if err != nil {
		// If not running as service, print help
		fmt.Println("Failed to run as service. Use 'run' for console mode.")
		fmt.Println("Use 'install' to install as a Windows service.")
		os.Exit(1)
	}
}

// RunConsole runs in console mode for debugging
func RunConsole() {
	fmt.Println("Running in console mode. Press Ctrl+C to stop.")
	stop := make(chan struct{})
	go StartWebUI(stop)
	StartHTTPServer(stop)
}

// Install installs the service, adds firewall rule, and starts it
func Install() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Create default config if not exists
	cfg := loadConfig()
	saveConfig(cfg)

	// Install Windows service
	fmt.Println("[1/3] Installing Windows service...")
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Check if already installed
	s, err := m.OpenService(serviceName)
	if err == nil {
		// Already exists, stop and delete
		fmt.Println("  Existing service found, removing...")
		status, err := s.Query()
		if err == nil && status.State != svc.Stopped {
			s.Control(svc.Stop)
			// Poll until stopped or timeout
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				status, err = s.Query()
				if err != nil || status.State == svc.Stopped {
					break
				}
			}
		}
		s.Delete()
		s.Close()
		// Wait for SCM to fully release the service name
		time.Sleep(1 * time.Second)
	}

	s, err = m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName: serviceDisplayName,
		Description: serviceDescription,
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}
	defer s.Close()

	// Set recovery actions (restart on failure)
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400) // reset after 1 day

	fmt.Println("  OK - Service registered")

	// Add firewall rule
	fmt.Println("[2/3] Adding firewall rule...")
	if err := addFirewallRule(cfg.Port); err != nil {
		fmt.Printf("  WARNING: %v\n", err)
		fmt.Println("  Service will still work, but you may need to allow port manually.")
	} else {
		fmt.Println("  OK - Firewall rule added")
	}

	// Start service
	fmt.Println("[3/3] Starting service...")
	err = s.Start()
	if err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}
	fmt.Println("  OK - Service started")

	if cfg.Secret == "" {
		fmt.Println("")
		fmt.Println("  ⚠ WARNING: No secret configured.")
		fmt.Println("    Anyone on your network can control this PC.")
		fmt.Println("    Set a secret via WebUI (http://127.0.0.1:5002) or config.json")
	}

	return nil
}

// Uninstall removes the service and firewall rule
func Uninstall() error {
	fmt.Println("[1/3] Stopping service...")
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service not found: %w", err)
	}
	defer s.Close()

	s.Control(svc.Stop)
	time.Sleep(2 * time.Second)
	fmt.Println("  OK - Service stopped")

	fmt.Println("[2/3] Removing service...")
	err = s.Delete()
	if err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}
	fmt.Println("  OK - Service removed")

	fmt.Println("[3/3] Removing firewall rule...")
	if err := removeFirewallRule(); err != nil {
		fmt.Printf("  WARNING: %v\n", err)
		fmt.Println("  You may need to remove the firewall rule manually.")
	} else {
		fmt.Println("  OK - Firewall rule removed")
	}

	return nil
}

// Status shows the current service status
func Status() {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Println("Status: Unable to connect to service manager")
		return
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		fmt.Println("Status: Not installed")
		return
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		fmt.Println("Status: Unable to query")
		return
	}

	cfg := loadConfig()

	stateStr := "Unknown"
	switch status.State {
	case svc.Running:
		stateStr = "Running"
	case svc.Stopped:
		stateStr = "Stopped"
	case svc.StartPending:
		stateStr = "Starting..."
	case svc.StopPending:
		stateStr = "Stopping..."
	}

	fmt.Printf("Service: %s\n", serviceDisplayName)
	fmt.Printf("State:   %s\n", stateStr)
	fmt.Printf("Port:    %d\n", cfg.Port)
	if cfg.Secret != "" {
		fmt.Printf("Secret:  %s\n", cfg.Secret)
	} else {
		fmt.Printf("Secret:  (none)\n")
	}
}

const firewallRuleName = "SmartThings PC Control"

func addFirewallRule(port int) error {
	// Remove existing rule first (in case port changed)
	removeFirewallRule()

	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+firewallRuleName,
		"dir=in", "action=allow", "protocol=tcp",
		"localport="+fmt.Sprintf("%d", port))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh add rule failed: %v - output: %s", err, string(output))
	}
	return nil
}

func removeFirewallRule() error {
	cmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+firewallRuleName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh delete rule failed: %v - output: %s", err, string(output))
	}
	return nil
}

// restartSelf restarts the service using sc.exe.
// Falls back to os.Exit(1) if sc.exe fails (e.g., in console mode).
func restartSelf() {
	// Try sc.exe stop + start (works when running as a service)
	stop := exec.Command("sc", "stop", serviceName)
	if err := stop.Run(); err != nil {
		logMsg("sc stop failed (console mode?): %v, falling back to os.Exit(1)", err)
		os.Exit(1)
	}
	// The service will be stopped; SCM will not auto-start it.
	// We need a separate process to start it after stop completes.
	// Use cmd /c with a delay to start the service after this process exits.
	start := exec.Command("cmd", "/c", "timeout", "/t", "2", "/nobreak", ">nul", "&&", "sc", "start", serviceName)
	start.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008, // DETACHED_PROCESS
	}
	if err := start.Start(); err != nil {
		logMsg("sc start scheduling failed: %v", err)
	}
}
