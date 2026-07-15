package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/service"
)

func main() {
	if len(os.Args) < 2 {
		// No arguments - run as Windows service
		service.RunService()
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
		fmt.Println("  - WebUI: http://127.0.0.1:5002")

	case "uninstall":
		if err := service.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "Uninstall failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Service uninstalled successfully.")

	case "status":
		service.Status()

	case "run":
		// Run in console mode (for debugging)
		service.RunConsole()

	default:
		fmt.Println("Remote Shutdown Service - SmartThings Edge Driver Compatible")
		fmt.Println("")
		fmt.Println("Usage:")
		fmt.Println("  install     Install and start the service")
		fmt.Println("  uninstall   Stop and remove the service")
		fmt.Println("  status      Show service status")
		fmt.Println("  run         Run in console mode (debug)")
		fmt.Println("")
		fmt.Println("No arguments = run as Windows service")
	}
}
