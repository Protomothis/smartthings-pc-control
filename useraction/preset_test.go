package useraction

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestBuildPresetLaunch(t *testing.T) {
	const root = `C:\Windows`
	cases := []struct {
		name string
		req  Request
		want presetLaunch
	}{
		{"program, no args",
			Request{PresetType: "program", Path: `C:\Games\Steam\steam.exe`},
			presetLaunch{Exe: `C:\Games\Steam\steam.exe`, CmdLine: `C:\Games\Steam\steam.exe`, Dir: `C:\Games\Steam`, Target: `C:\Games\Steam\steam.exe`}},
		{"program, quoting",
			Request{PresetType: "program", Path: `C:\Program Files\OBS\obs64.exe`, Args: []string{"--startstreaming", "two words", `say "hi"`, "", `C:\dir\`}},
			presetLaunch{Exe: `C:\Program Files\OBS\obs64.exe`,
				CmdLine: `"C:\Program Files\OBS\obs64.exe" --startstreaming "two words" "say \"hi\"" "" C:\dir\`,
				Dir:     `C:\Program Files\OBS`, Target: `C:\Program Files\OBS\obs64.exe`}},
		{"program in a drive root",
			Request{PresetType: "program", Path: `D:\tool.exe`},
			presetLaunch{Exe: `D:\tool.exe`, CmdLine: `D:\tool.exe`, Dir: `D:\`, Target: `D:\tool.exe`}},
		{"UNC program",
			Request{PresetType: "program", Path: `\\nas\apps\x.EXE`, Args: []string{"&", "|"}},
			presetLaunch{Exe: `\\nas\apps\x.EXE`, CmdLine: `\\nas\apps\x.EXE & |`, Dir: `\\nas\apps`, Target: `\\nas\apps\x.EXE`}},
		{"powershell script",
			Request{PresetType: "script", Path: `C:\Scripts\game mode.ps1`, Args: []string{"-Mode", "on; rm -r C:\\"}},
			presetLaunch{Exe: root + `\System32\WindowsPowerShell\v1.0\powershell.exe`,
				CmdLine: root + `\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -ExecutionPolicy Bypass -File "C:\Scripts\game mode.ps1" -Mode "on; rm -r C:\\"`,
				Dir:     `C:\Scripts`, Target: `C:\Scripts\game mode.ps1`}},
		{"batch file: every part quoted, cmd /s /c",
			Request{PresetType: "script", Path: `C:\Scripts\go.cmd`, Args: []string{"a&b", "x y", "^|<>!"}},
			presetLaunch{Exe: root + `\System32\cmd.exe`,
				CmdLine: root + `\System32\cmd.exe /d /v:off /s /c ""C:\Scripts\go.cmd" "a&b" "x y" "^|<>!""`,
				Dir:     `C:\Scripts`, Target: `C:\Scripts\go.cmd`}},
		{"bat without args",
			Request{PresetType: "script", Path: `C:\s\run.BAT`},
			presetLaunch{Exe: root + `\System32\cmd.exe`, CmdLine: root + `\System32\cmd.exe /d /v:off /s /c ""C:\s\run.BAT""`, Dir: `C:\s`, Target: `C:\s\run.BAT`}},
	}
	for _, c := range cases {
		got, err := buildPresetLaunch(c.req, root)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestBuildPresetLaunchRefuses(t *testing.T) {
	for name, req := range map[string]Request{
		"relative program":       {PresetType: "program", Path: `steam.exe`},
		"drive-relative program": {PresetType: "program", Path: `C:steam.exe`},
		"rooted, no drive":       {PresetType: "program", Path: `\Games\steam.exe`},
		"batch as program":       {PresetType: "program", Path: `C:\s\go.bat`},
		"shortcut as program":    {PresetType: "program", Path: `C:\s\game.lnk`},
		"relative script":        {PresetType: "script", Path: `scripts\go.ps1`},
		"quote in batch arg":     {PresetType: "script", Path: `C:\s\go.bat`, Args: []string{`a" & calc & "`}},
		"percent in batch arg":   {PresetType: "script", Path: `C:\s\go.cmd`, Args: []string{"%PATH%"}},
		"percent in batch path":  {PresetType: "script", Path: `C:\100%\go.cmd`},
		"device path":            {PresetType: "program", Path: `\\?\C:\x.exe`},
	} {
		_, err := buildPresetLaunch(req, `C:\Windows`)
		var ue *Error
		if !errors.As(err, &ue) || ue.Code != CodeBadArgs {
			t.Errorf("%s: err = %v, want bad_args", name, err)
		}
	}
	// A quote is fine in a PowerShell argument: EscapeArg handles it.
	if _, err := buildPresetLaunch(Request{PresetType: "script", Path: `C:\s\go.ps1`, Args: []string{`a"b`, "%x%"}}, `C:\Windows`); err != nil {
		t.Errorf("ps1 with quotes: %v", err)
	}
}

func TestHandlePreset(t *testing.T) {
	var launched []presetLaunch
	var opened []string
	sp, ou := startProcess, openURL
	startProcess = func(l presetLaunch) error { launched = append(launched, l); return nil }
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { startProcess, openURL = sp, ou })

	out, err := handlePreset(Request{PresetType: "url", Path: "https://example.com/x"})
	if err != nil || out["started"] != true || len(opened) != 1 || opened[0] != "https://example.com/x" || len(launched) != 0 {
		t.Fatalf("url: %v, %v, opened %q", out, err, opened)
	}
	out, err = handlePreset(Request{PresetType: "program", Path: `C:\x\y.exe`, Args: []string{"-a"}})
	if err != nil || out["started"] != true || len(launched) != 1 || launched[0].CmdLine != `C:\x\y.exe -a` {
		t.Fatalf("program: %v, %v, %+v", out, err, launched)
	}

	startProcess = func(presetLaunch) error { return errors.New("The system cannot find the file specified.") }
	if _, err := handlePreset(Request{PresetType: "program", Path: `C:\x\y.exe`}); err == nil {
		t.Error("a start failure must fail the action")
	} else if ue := (*Error)(nil); !errors.As(err, &ue) || ue.Code != CodeFailed {
		t.Errorf("err = %v, want failed", err)
	}
	openURL = func(string) error { return errors.New("no browser") }
	if _, err := handlePreset(Request{PresetType: "url", Path: "http://x/"}); err == nil {
		t.Error("a ShellExecute failure must fail the action")
	}
}

// TestPresetFailureHidesThePath: a failed start names the file by its base
// name only, says why in Reason, and keeps the full error in Detail (for
// service.log). Telegram and SmartThings see the message.
func TestPresetFailureHidesThePath(t *testing.T) {
	sp, ou := startProcess, openURL
	t.Cleanup(func() { startProcess, openURL = sp, ou })
	const path = `C:\Users\kim\secret-project\run.ps1`
	for _, tc := range []struct {
		name   string
		err    error
		reason string
		msg    string
	}{
		{"missing", &fs.PathError{Op: "CreateFile", Path: path, Err: windows.ERROR_FILE_NOT_FOUND}, PresetNotFound, "file not found: run.ps1"},
		{"missing folder", &fs.PathError{Op: "CreateFile", Path: path, Err: windows.ERROR_PATH_NOT_FOUND}, PresetNotFound, "file not found: run.ps1"},
		{"denied", &fs.PathError{Op: "fork/exec", Path: path, Err: windows.ERROR_ACCESS_DENIED}, PresetAccessDenied, "access denied: run.ps1"},
		{"other", &fs.PathError{Op: "fork/exec", Path: path, Err: windows.ERROR_BAD_EXE_FORMAT}, PresetStartFailed, "could not start: run.ps1"},
	} {
		startProcess = func(presetLaunch) error { return tc.err }
		_, err := handlePreset(Request{PresetType: "script", Path: path})
		var ue *Error
		if !errors.As(err, &ue) {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if ue.Code != CodeFailed || ue.Reason != tc.reason || ue.Message != tc.msg {
			t.Errorf("%s: %+v, want reason %s message %q", tc.name, ue, tc.reason, tc.msg)
		}
		if !strings.Contains(ue.Detail, path) {
			t.Errorf("%s: detail %q lost the full error", tc.name, ue.Detail)
		}
		line, _ := render(nil, err)
		var reply map[string]any
		if jerr := json.Unmarshal(line, &reply); jerr != nil {
			t.Fatal(jerr)
		}
		if msg, _ := reply["message"].(string); strings.Contains(msg, "secret-project") || reply["reason"] != tc.reason || reply["detail"] == nil {
			t.Errorf("%s: reply %s", tc.name, line)
		}
	}

	openURL = func(string) error { return errors.New("ShellExecute https://x/?token=abc: no association") }
	_, err := handlePreset(Request{PresetType: "url", Path: "https://x/?token=abc"})
	var ue *Error
	if !errors.As(err, &ue) || ue.Reason != PresetURLFailed || strings.Contains(ue.Message, "token") || strings.Contains(ue.Message, "x/") {
		t.Errorf("url failure: %+v", ue)
	}
	if got := PresetFileName(`C:\a\b\c.exe`); got != "c.exe" {
		t.Errorf("PresetFileName = %q", got)
	}
}

func TestIsAbsWindowsPath(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\x.exe`:       true,
		`c:/x.exe`:       true,
		`\\srv\share\x`:  true,
		`C:x.exe`:        false,
		`\x.exe`:         false,
		`x.exe`:          false,
		`1:\x.exe`:       false,
		`\\?\C:\x.exe`:   false,
		`\\.\pipe\x`:     false,
		`\\\srv\x`:       false,
		``:               false,
		`https://x.com/`: false,
	} {
		if got := isAbsWindowsPath(p); got != want {
			t.Errorf("isAbsWindowsPath(%q) = %v, want %v", p, got, want)
		}
	}
}
