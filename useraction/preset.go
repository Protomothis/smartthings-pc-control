package useraction

// Preset execution (#109, docs/design/media-notify.md §10). The service only
// ever sends a slot number from the network; the type, path and arguments
// come from config.json and reach this child as a fixed argument vector.
//
//	program  CreateProcess(path, argv) — no shell, working directory = the exe's folder
//	url      ShellExecute("open", url) — http/https only (the parser checks)
//	script   .ps1 → powershell.exe -NoProfile -ExecutionPolicy Bypass -File <path> args…
//	         .bat/.cmd → cmd.exe /d /v:off /s /c ""<path>" "arg"…"
//
// Interpreters are started by absolute path under the Windows directory
// (internal/systool). Every argument is quoted with syscall.EscapeArg (the
// MSVCRT rules CommandLineToArgvW and PowerShell follow); cmd.exe has rules
// of its own, so for batch files every argument is double-quoted and the
// characters quoting cannot neutralise there (" and %) are refused. The
// launched program is not waited for.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// programExts are what a "program" preset may start. A .bat or .cmd given
// to CreateProcess runs through cmd.exe with its own parsing, so batch
// files must use the "script" type and its explicit quoting.
var programExts = map[string]bool{".exe": true, ".com": true}

// cmdUnsafe are the characters cmd.exe acts on even inside double quotes.
const cmdUnsafe = `"%`

func init() {
	Register(ActionPreset, handlePreset)
}

// presetLaunch is what CreateProcess gets for a program or script preset.
type presetLaunch struct {
	Exe     string // absolute path of the image to start
	CmdLine string // the complete command line, argv[0] included
	Dir     string // working directory
	// Target is the preset path itself (the script, for an interpreter).
	Target string
}

// cmdLineOf joins argv the way CommandLineToArgvW splits it back.
func cmdLineOf(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = syscall.EscapeArg(a)
	}
	return strings.Join(parts, " ")
}

// cmdQuote wraps one batch-file argument in double quotes, refusing the
// characters that cmd.exe would still interpret inside them.
func cmdQuote(s string) (string, bool) {
	if strings.ContainsAny(s, cmdUnsafe) {
		return "", false
	}
	return `"` + s + `"`, true
}

// ValidatePresetLaunch checks what the parser leaves to the preset rules:
// program and script paths must be absolute, a program must be an .exe or
// .com, and a batch file's path and arguments must be free of " and %.
// The service applies it to config.json too, so an entry that could never
// run is rejected when it is saved rather than when it is used.
func ValidatePresetLaunch(typ, path string, args []string) error {
	switch typ {
	case "program", "script":
		if !isAbsWindowsPath(path) {
			return badArgs("preset: %s path must be absolute (C:\\… or \\\\server\\…)", typ)
		}
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case typ == "program" && !programExts[ext]:
		return badArgs("preset: a program must be an .exe (use the script type for .bat, .cmd and .ps1)")
	case typ == "script" && (ext == ".bat" || ext == ".cmd"):
		if strings.ContainsAny(path, cmdUnsafe) {
			return badArgs(`preset: a batch file path must not contain " or %%`)
		}
		for _, a := range args {
			if strings.ContainsAny(a, cmdUnsafe) {
				return badArgs(`preset: batch file arguments must not contain " or %%`)
			}
		}
	}
	return nil
}

// isAbsWindowsPath accepts "C:\dir\x.exe" and "\\server\share\x.exe" —
// filepath.IsAbs on Windows, spelled out so the rule does not depend on the
// build's GOOS.
func isAbsWindowsPath(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return strings.HasPrefix(p, `\\`) && len(p) > 2 && p[2] != '\\' && p[2] != '?' && p[2] != '.'
}

// buildPresetLaunch turns a program or script request into the process to
// start. systemRoot is the Windows directory (C:\Windows,
// systool.WindowsDir), where the interpreters live.
func buildPresetLaunch(req Request, systemRoot string) (presetLaunch, error) {
	if err := ValidatePresetLaunch(req.PresetType, req.Path, req.Args); err != nil {
		return presetLaunch{}, err
	}
	dir := windowsDir(req.Path)
	switch req.PresetType {
	case "program":
		return presetLaunch{
			Exe:     req.Path,
			CmdLine: cmdLineOf(append([]string{req.Path}, req.Args...)),
			Dir:     dir,
			Target:  req.Path,
		}, nil
	case "script":
		switch strings.ToLower(filepath.Ext(req.Path)) {
		case ".ps1":
			ps := systemRoot + `\System32\WindowsPowerShell\v1.0\powershell.exe`
			argv := append([]string{ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", req.Path}, req.Args...)
			return presetLaunch{Exe: ps, CmdLine: cmdLineOf(argv), Dir: dir, Target: req.Path}, nil
		case ".bat", ".cmd":
			cmd := systemRoot + `\System32\cmd.exe`
			// /s: strip exactly the outer pair of quotes and run the rest
			// as written. /d: no AutoRun commands. /v:off: "!" is literal.
			inner, _ := cmdQuote(req.Path)
			for _, a := range req.Args {
				q, _ := cmdQuote(a) // ValidatePresetLaunch refused the unquotable
				inner += " " + q
			}
			return presetLaunch{
				Exe:     cmd,
				CmdLine: syscall.EscapeArg(cmd) + ` /d /v:off /s /c "` + inner + `"`,
				Dir:     dir,
				Target:  req.Path,
			}, nil
		}
	}
	return presetLaunch{}, badArgs("preset: cannot launch type %q", req.PresetType)
}

// windowsDir is the folder part of a Windows path, independent of GOOS.
func windowsDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i > 0 {
		d := p[:i]
		if len(d) == 2 && d[1] == ':' {
			d += `\`
		}
		return d
	}
	return ""
}

// startProcess starts l detached, with the user's own environment.
// Replaced by the tests.
var startProcess = func(l presetLaunch) error {
	// An interpreter starts fine for a missing script and only complains in
	// its own window; say so here instead.
	if _, err := os.Stat(l.Target); err != nil {
		return err
	}
	cmd := &exec.Cmd{
		Path:        l.Exe,
		Args:        []string{l.Exe},
		Dir:         l.Dir,
		Env:         userEnviron(),
		SysProcAttr: &syscall.SysProcAttr{CmdLine: l.CmdLine},
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// openURL hands a validated http/https URL to the default browser.
// Replaced by the tests.
var openURL = func(u string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// ShellExecute wants an apartment; the browser it may start inherits
	// this process's environment, so give it the user's.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}
	adoptUserEnviron()
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// handlePreset starts the preset and answers {"ok":true,"started":true}
// without waiting for it.
func handlePreset(req Request) (map[string]any, error) {
	if req.PresetType == "url" {
		if err := openURL(req.Path); err != nil {
			return nil, Failed("open url: %v", err)
		}
		return map[string]any{"started": true}, nil
	}
	l, err := buildPresetLaunch(req, systool.WindowsDir())
	if err != nil {
		return nil, err
	}
	if err := startProcess(l); err != nil {
		return nil, Failed("start %s: %v", filepath.Base(req.Path), err)
	}
	return map[string]any{"started": true}, nil
}
