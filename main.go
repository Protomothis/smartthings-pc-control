package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/Protomothis/smartthings-pc-control/gui"
	"github.com/Protomothis/smartthings-pc-control/service"
)

var Version = "dev"

// attachParentConsole reconnects stdout/stderr to the launching terminal.
// The binary is built with -H=windowsgui so no console window pops up on
// double-click or "gui"; without this, CLI commands would print nothing.
func attachParentConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	if ret, _, _ := proc.Call(attachParentProcess); ret == 0 {
		return // no parent console (double-click, service) — nothing to do
	}
	// Only rebind handles that are missing — when the shell pipes or
	// redirects output, the inherited handles must stay untouched.
	if os.Stdout.Fd() == 0 || int(os.Stdout.Fd()) == -1 {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stdout = f
		}
	}
	if os.Stderr.Fd() == 0 || int(os.Stderr.Fd()) == -1 {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stderr = f
		}
	}
}

func main() {
	attachParentConsole()
	service.Version = Version

	if len(os.Args) < 2 {
		// No arguments: Windows service context → run the service;
		// interactive (double-click) → open the native GUI with tray.
		if isSvc, _ := svc.IsWindowsService(); isSvc {
			service.RunService()
		} else {
			gui.Run(Version, false)
		}
		return
	}

	cmd := strings.ToLower(os.Args[1])
	switch cmd {
	case "install":
		if err := service.Install(); err != nil {
			fmt.Fprintf(os.Stderr, "Install failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Service installed and started successfully.")
		fmt.Println("  - Listening on port 5001")
		fmt.Println("  - Firewall rule added")
		fmt.Println("  - Service set to auto-start on boot")
		fmt.Println("  - Manage via the desktop app (double-click the exe)")
		fmt.Println("  - Browser WebUI is disabled by default; enable it in app settings")
		// Show completion dialog if launched from GUI installer
		if len(os.Args) > 2 && os.Args[2] == "--gui" {
			service.ShowInstallCompleteDialog()
		}

	case "uninstall":
		if err := service.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "Uninstall failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Service uninstalled successfully.")

	case "status":
		service.Status()

	case "version":
		fmt.Printf("SmartThings PC Control %s\n", Version)

	case "run":
		// Run in console mode (for debugging)
		service.RunConsole()

	case "gui":
		// Native GUI — talks to the running service via localhost API.
		// "--minimized" (login autostart) keeps the window hidden, tray only.
		minimized := len(os.Args) > 2 && os.Args[2] == "--minimized"
		gui.Run(Version, minimized)

	case "toast":
		// Invoked by toast notification action buttons (stpc:// protocol)
		if len(os.Args) > 2 {
			gui.HandleToastAction(os.Args[2])
		}

	case "update-apply":
		// Hidden: launched elevated by the GUI's self-updater as
		// `update-apply "<newExe>" <guiPid>`. Swaps the installed exe and
		// relaunches the GUI; see gui/selfupdate.go.
		newExe, pid, err := gui.ParseUpdateApplyArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := gui.ApplyUpdate(newExe, pid); err != nil {
			fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Printf("SmartThings PC Control %s\n", Version)
		fmt.Println("")
		fmt.Println("Usage:")
		fmt.Println("  install     Install and start the service")
		fmt.Println("  uninstall   Stop and remove the service")
		fmt.Println("  status      Show service status")
		fmt.Println("  version     Show version")
		fmt.Println("  run         Run in console mode (debug)")
		fmt.Println("  gui [--minimized]  Open the desktop app (tray only with --minimized)")
		fmt.Println("")
		fmt.Println("No arguments = run as Windows service")
	}
}
