package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "RemoteShutdownService"
const serviceDisplayName = "Remote Shutdown Service"
const serviceDescription = "HTTP server for SmartThings PC shutdown control. Compatible with PCControl Edge driver."

type shutdownService struct {
	stop  chan struct{}
	power powerTracker // suspend/resume broadcasts → power.resumed (#60)
}

func (s *shutdownService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Initialize config before starting servers
	initLogger()
	cfg := loadConfig()
	setConfig(cfg)

	// Notification bus (#55) must exist before the servers emit. The live
	// Telegram sink (#63) follows getConfig().Telegram on every event, so
	// enabling Telegram later needs no restart; the grace-message hook
	// (#62) rides along so the message can be edited when the schedule ends.
	startLiveNotifier()
	// Inbound Telegram commands (#61): polls only while telegram.enabled
	// and control_enabled are both set; config saves reconcile it.
	startTelegramControl()
	// SSDP discovery (#69): answers M-SEARCH while smartthings.discovery
	// is on; config saves reconcile it.
	startSSDP()
	// The responder needs inbound UDP 1900; an install made before #69 has
	// no such rule, so re-check here (#76). Off the startup path: netsh
	// must never delay the service reaching Running.
	go ensureSSDPFirewallRuleAtStart()

	s.stop = make(chan struct{})
	go StartHTTPServer(s.stop)
	go StartWebUI(s.stop)
	startupHooks(s.stop) // system.updated, power.started, release checker (#60)

	// AcceptPowerEvent subscribes to SERVICE_CONTROL_POWEREVENT so sleep and
	// resume broadcasts reach us (power.resumed).
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPowerEvent}

	for {
		c := <-r
		switch c.Cmd {
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			reason := "stop"
			if c.Cmd == svc.Shutdown {
				reason = "shutdown"
			}
			logMsg("Service stopping (%s)", reason)
			// The next start reads this back as last_shutdown_clean; a
			// power cut never reaches here, so the flag stays false.
			markCleanShutdown(statePath())
			// The SmartThings push sink taps this event and delivers it
			// synchronously (up to 1.5s, edge-driver doc §4.5) so the hub
			// learns the PC is going away before it stops answering; the
			// stop continues right afterwards either way. reason is
			// shutdown/restart/suspend/hibernate/unknown (§6.2): the SCM
			// only distinguishes a system shutdown from a plain stop, so
			// the command this service just ran refines it.
			fallback := "unknown"
			if c.Cmd == svc.Shutdown {
				fallback = "shutdown"
			}
			emit("power", "stopping", map[string]string{"reason": stoppingReason(fallback)})
			close(s.stop)
			stopTelegramControl()
			stopSSDP()
			stopNotifier() // delivers what is queued (power.stopping, #60) before the logger goes
			closeLogger()
			return false, 0
		case svc.PowerEvent:
			// EventType carries the PBT_* broadcast; only suspend and
			// automatic resume matter, everything else is ignored.
			s.power.handle(c.EventType)
		case svc.Interrogate:
			changes <- c.CurrentStatus
		}
	}
}

// RunService runs as a Windows service. Interactive launches are routed to
// the native GUI by the main package before this is called.
func RunService() {
	svc.Run(serviceName, &shutdownService{})
}

// ShowInstallCompleteDialog shows a completion dialog with WebUI button.
func ShowInstallCompleteDialog() {
	modUser32 := syscall.NewLazyDLL("user32.dll")
	procMessageBoxW := modUser32.NewProc("MessageBoxW")

	title, _ := syscall.UTF16PtrFromString("SmartThings PC Control")
	msg, _ := syscall.UTF16PtrFromString("설치가 완료되었습니다!\nInstallation complete!\n\n관리는 exe를 더블클릭해 데스크톱 앱을 사용하세요.\nManage this PC with the desktop app (double-click the exe).")

	// MB_OK (0) | MB_ICONINFORMATION (64)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x40)
}

// RunConsole runs in console mode for debugging
func RunConsole() {
	fmt.Println("Running in console mode. Press Ctrl+C to stop.")
	startLiveNotifier() // live Telegram sink + grace-message hook; see Execute
	startTelegramControl()
	defer stopTelegramControl()
	startSSDP()
	defer stopSSDP()
	stop := make(chan struct{})
	go StartWebUI(stop)
	startupHooks(stop)
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
	// SSDP discovery (#69) answers M-SEARCH on UDP 1900; without this rule
	// the Edge driver never sees this PC (#76).
	if err := ensureSSDPFirewallRule(); err != nil {
		fmt.Printf("  WARNING: %v\n", err)
		fmt.Println("  SmartThings discovery may not find this PC until UDP 1900 is allowed.")
	} else {
		fmt.Println("  OK - Discovery firewall rule added (UDP 1900)")
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
	removeWebUIFirewallRule() // best-effort; only exists when webui_remote was enabled
	removeSSDPFirewallRule()  // best-effort; only exists on installs from #76 on

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

// addFirewallRule opens the command port. The delete comes first because
// the port may have changed since the rule was written; ensureFirewallRule
// only looks at the name (service/firewall.go, #76).
func addFirewallRule(port int) error {
	// Remove existing rule first (in case port changed)
	removeFirewallRule()
	return ensureFirewallRule(firewallRuleName, firewallProtoTCP, port)
}

func removeFirewallRule() error {
	return deleteFirewallRule(firewallRuleName)
}

const webUIFirewallRuleName = "SmartThings PC Control WebUI"

// addWebUIFirewallRule opens the WebUI port for LAN access. Managed by the
// service at startup (runs as SYSTEM) when webui_remote is enabled.
func addWebUIFirewallRule(port int) error {
	removeWebUIFirewallRule()
	return ensureFirewallRule(webUIFirewallRuleName, firewallProtoTCP, port)
}

func removeWebUIFirewallRule() error {
	return deleteFirewallRule(webUIFirewallRuleName)
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
