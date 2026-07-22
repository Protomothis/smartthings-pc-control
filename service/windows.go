package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	isService, _ := svc.IsWindowsService()
	if !isService {
		// Interactive session (double-click, terminal) — show GUI manager
		ShowManagerGUI()
		return
	}
	svc.Run(serviceName, &shutdownService{})
}

// ShowManagerGUI shows a service management GUI using PowerShell WPF.
func ShowManagerGUI() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exePath, _ = filepath.Abs(exePath)

	script := `
Add-Type -AssemblyName PresentationFramework
Add-Type -AssemblyName PresentationCore

$exePath = "` + strings.ReplaceAll(exePath, `\`, `\\`) + `"
$version = "` + Version + `"
$serviceName = "RemoteShutdownService"

function Get-SvcStatus {
    $svc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if (-not $svc) { return "not_installed" }
    return $svc.Status.ToString().ToLower()
}

[xml]$xaml = @"
<Window xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
        xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"
        Title="SmartThings PC Control" Width="440" Height="340"
        WindowStartupLocation="CenterScreen" ResizeMode="NoResize"
        Background="#0f172a" FontFamily="Segoe UI">
    <Grid Margin="28,24">
        <Grid.RowDefinitions>
            <RowDefinition Height="Auto"/>
            <RowDefinition Height="Auto"/>
            <RowDefinition Height="*"/>
            <RowDefinition Height="Auto"/>
        </Grid.RowDefinitions>

        <!-- Header -->
        <StackPanel Grid.Row="0" Margin="0,0,0,20">
            <TextBlock FontSize="17" FontWeight="SemiBold" Foreground="#f8fafc">SmartThings PC Control</TextBlock>
            <TextBlock Name="txtVersion" FontSize="11" Foreground="#475569" Margin="0,2,0,0"/>
        </StackPanel>

        <!-- Status + Info -->
        <Border Grid.Row="1" Background="#1e293b" CornerRadius="8" Padding="16" Margin="0,0,0,20">
            <StackPanel>
                <TextBlock Name="txtStatus" FontSize="13" FontWeight="SemiBold" Margin="0,0,0,10"/>
                <Grid>
                    <Grid.ColumnDefinitions>
                        <ColumnDefinition Width="60"/>
                        <ColumnDefinition Width="*"/>
                    </Grid.ColumnDefinitions>
                    <Grid.RowDefinitions>
                        <RowDefinition/>
                        <RowDefinition/>
                        <RowDefinition/>
                    </Grid.RowDefinitions>
                    <TextBlock Grid.Row="0" Grid.Column="0" Text="Path" Foreground="#64748b" FontSize="11.5" Margin="0,0,0,4"/>
                    <TextBlock Grid.Row="0" Grid.Column="1" Name="txtPath" Foreground="#94a3b8" FontSize="11.5" TextWrapping="Wrap" Margin="0,0,0,4"/>
                    <TextBlock Grid.Row="1" Grid.Column="0" Text="Port" Foreground="#64748b" FontSize="11.5" Margin="0,0,0,4"/>
                    <TextBlock Grid.Row="1" Grid.Column="1" Name="txtPort" Foreground="#94a3b8" FontSize="11.5" Margin="0,0,0,4"/>
                    <TextBlock Grid.Row="2" Grid.Column="0" Text="WebUI" Foreground="#64748b" FontSize="11.5"/>
                    <TextBlock Grid.Row="2" Grid.Column="1" Name="txtWebUI" Foreground="#94a3b8" FontSize="11.5"/>
                </Grid>
            </StackPanel>
        </Border>

        <!-- Actions: single toggle button + WebUI -->
        <StackPanel Grid.Row="3" Orientation="Horizontal" HorizontalAlignment="Right">
            <Button Name="btnAction" Content="Install" Width="120" Height="36" Margin="0,0,8,0" FontSize="13" FontWeight="SemiBold" Foreground="White" Background="#334155" BorderThickness="0" Cursor="Hand">
                <Button.Template><ControlTemplate TargetType="Button"><Border Background="{TemplateBinding Background}" CornerRadius="6"><ContentPresenter HorizontalAlignment="Center" VerticalAlignment="Center"/></Border></ControlTemplate></Button.Template>
            </Button>
            <Button Name="btnWebUI" Content="Open WebUI" Width="110" Height="36" FontSize="13" FontWeight="SemiBold" Foreground="White" Background="#475569" BorderThickness="0" Cursor="Hand" Visibility="Collapsed">
                <Button.Template><ControlTemplate TargetType="Button"><Border Background="{TemplateBinding Background}" CornerRadius="6"><ContentPresenter HorizontalAlignment="Center" VerticalAlignment="Center"/></Border></ControlTemplate></Button.Template>
            </Button>
        </StackPanel>
    </Grid>
</Window>
"@

$reader = [System.Xml.XmlNodeReader]::new($xaml)
$window = [Windows.Markup.XamlReader]::Load($reader)

$txtVersion = $window.FindName("txtVersion")
$txtStatus = $window.FindName("txtStatus")
$txtPath = $window.FindName("txtPath")
$txtPort = $window.FindName("txtPort")
$txtWebUI = $window.FindName("txtWebUI")
$btnAction = $window.FindName("btnAction")
$btnWebUI = $window.FindName("btnWebUI")

$txtVersion.Text = $version
$txtPath.Text = $exePath

$configPath = [System.IO.Path]::Combine([System.IO.Path]::GetDirectoryName($exePath), "config.json")
$port = 5001
if (Test-Path $configPath) {
    try { $cfg = Get-Content $configPath | ConvertFrom-Json; $port = $cfg.port } catch {}
}
$txtPort.Text = "$port"
$txtWebUI.Text = "http://127.0.0.1:$($port+1)"

function Update-UI {
    $status = Get-SvcStatus
    switch ($status) {
        "running" {
            $txtStatus.Text = "● Running"
            $txtStatus.Foreground = [System.Windows.Media.BrushConverter]::new().ConvertFromString("#4ade80")
            $btnAction.Content = "Uninstall"
            $btnAction.Background = [System.Windows.Media.BrushConverter]::new().ConvertFromString("#334155")
            $btnWebUI.Visibility = "Visible"
        }
        "stopped" {
            $txtStatus.Text = "○ Stopped"
            $txtStatus.Foreground = [System.Windows.Media.BrushConverter]::new().ConvertFromString("#fbbf24")
            $btnAction.Content = "Start"
            $btnAction.Background = [System.Windows.Media.BrushConverter]::new().ConvertFromString("#334155")
            $btnWebUI.Visibility = "Collapsed"
        }
        default {
            $txtStatus.Text = "○ Not installed"
            $txtStatus.Foreground = [System.Windows.Media.BrushConverter]::new().ConvertFromString("#64748b")
            $btnAction.Content = "Install"
            $btnAction.Background = [System.Windows.Media.BrushConverter]::new().ConvertFromString("#334155")
            $btnWebUI.Visibility = "Collapsed"
        }
    }
}

Update-UI

$btnAction.Add_Click({
    $status = Get-SvcStatus
    $btnAction.IsEnabled = $false
    switch ($status) {
        "running" {
            $result = [System.Windows.MessageBox]::Show("Uninstall the service?", "Confirm", "YesNo", "Question")
            if ($result -eq "Yes") {
                $btnAction.Content = "Removing..."
                $window.Dispatcher.Invoke([action]{}, "Render")
                Start-Process -FilePath $exePath -ArgumentList "uninstall" -Verb RunAs -Wait
            }
        }
        "stopped" {
            $btnAction.Content = "Starting..."
            $window.Dispatcher.Invoke([action]{}, "Render")
            Start-Process -FilePath "sc.exe" -ArgumentList "start",$serviceName -Verb RunAs -Wait
        }
        default {
            $btnAction.Content = "Installing..."
            $window.Dispatcher.Invoke([action]{}, "Render")
            Start-Process -FilePath $exePath -ArgumentList "install" -Verb RunAs -Wait
        }
    }
    Start-Sleep 2
    $btnAction.IsEnabled = $true
    Update-UI
})

$btnWebUI.Add_Click({
    Start-Process "http://127.0.0.1:$($port+1)"
})

$window.ShowDialog() | Out-Null
`

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		logPath := filepath.Join(filepath.Dir(exePath), "gui.log")
		logContent := fmt.Sprintf("[%s] GUI error: %v\n%s\n", time.Now().Format("2006-01-02 15:04:05"), runErr, string(output))
		os.WriteFile(logPath, []byte(logContent), 0644)
	}
}

// ShowInstallCompleteDialog shows a completion dialog with WebUI button.
func ShowInstallCompleteDialog() {
	modUser32 := syscall.NewLazyDLL("user32.dll")
	procMessageBoxW := modUser32.NewProc("MessageBoxW")

	title, _ := syscall.UTF16PtrFromString("SmartThings PC Control")
	msg, _ := syscall.UTF16PtrFromString("설치가 완료되었습니다!\nInstallation complete!\n\nWebUI: http://127.0.0.1:5002\n\nWebUI를 여시겠습니까?\nOpen WebUI?")

	// MB_YESNO (4) | MB_ICONINFORMATION (64)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x44)

	if ret == 6 { // IDYES
		exec.Command("cmd", "/c", "start", "http://127.0.0.1:5002").Run()
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
