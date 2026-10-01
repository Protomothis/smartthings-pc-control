// Package systool starts the Windows tools the program still shells out
// to (netsh, sc, shutdown, wevtutil, PowerShell, cmd) by absolute path.
//
// The service runs as LocalSystem, and exec.Command("netsh") would search
// the current directory and PATH for it; a writable folder early on PATH
// would then run as SYSTEM. Every external tool therefore goes through
// this one helper, which only ever names files under the Windows
// directory the kernel reports (GetSystemWindowsDirectory, not the
// %SystemRoot% of an environment a caller may have built).
package systool

import (
	"context"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// The tools, relative to System32.
const (
	Cmd        = "cmd.exe"
	Netsh      = "netsh.exe"
	PowerShell = `WindowsPowerShell\v1.0\powershell.exe`
	SC         = "sc.exe"
	Shutdown   = "shutdown.exe"
	Timeout    = "timeout.exe"
	Wevtutil   = "wevtutil.exe"
)

// WindowsDir is the Windows directory (C:\Windows). It asks the kernel
// first; %SystemRoot% and then C:\Windows are the fallbacks for the
// unlikely case that the call fails.
func WindowsDir() string {
	if d, err := windows.GetSystemWindowsDirectory(); err == nil && d != "" {
		return d
	}
	if d := os.Getenv("SystemRoot"); d != "" {
		return d
	}
	return `C:\Windows`
}

// Path is the absolute path of a tool in System32, for instance
// Path(Netsh) or Path(PowerShell).
func Path(tool string) string {
	return WindowsDir() + `\System32\` + tool
}

// Command is exec.Command for a System32 tool, by absolute path.
func Command(tool string, args ...string) *exec.Cmd {
	return exec.Command(Path(tool), args...)
}

// CommandContext is exec.CommandContext for a System32 tool.
func CommandContext(ctx context.Context, tool string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, Path(tool), args...)
}
