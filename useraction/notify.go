package useraction

// PC notification (#106, docs/design/media-notify.md §2·§7): a toast with
// the title and text, and — with --speak — the text read aloud by SAPI.
//
// The toast is not built with go-toast, which the tray app uses for its own
// fixed grace-period strings. go-toast pastes the title and message into a
// PowerShell double-quoted here-string, where "$(…)" is evaluated and a
// line starting with "@ ends the string, so a notification text from the
// network would be code. Here the toast XML is escaped in Go and handed to
// a fixed script through an environment variable: no user text ever
// appears in a script or on a command line.
//
// Speaking takes seconds per sentence, far longer than the service's 3 s
// budget for one user-action run (the child is killed when it runs out).
// So notify only resolves the voice, starts a detached `user-action speak`
// child and answers at once; that child reads the text and exits. Speakers
// queue on a named mutex so two notifications are not read over each other.

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/sapi"
)

// ToastAppID is the application name a toast is shown under. It must stay
// equal to the tray app's windowTitle (gui/singleinstance.go), which its
// go-toast notifications use, so both land in one Action Center group.
const ToastAppID = "SmartThings PC Control"

// Environment variables the fixed toast script reads.
const (
	toastXMLEnv   = "STPC_TOAST_XML"
	toastAppIDEnv = "STPC_TOAST_APPID"
)

// toastScript shows the toast whose XML is in $env:STPC_TOAST_XML. It is a
// constant: nothing a caller sends is ever part of it.
const toastScript = `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($env:` + toastXMLEnv + `)
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:` + toastAppIDEnv + `).Show($toast)
`

// toastWait is how long notify waits for PowerShell to report the toast.
// Past it the toast is left to appear on its own and the reply says
// "pending": together with finding the session and starting this child, a
// cold PowerShell start could otherwise overrun the service's 3 s budget.
var toastWait = 1200 * time.Millisecond

// speakMutexName serialises the detached speakers of one user session.
const speakMutexName = `Local\SmartThingsPCControl-speak`

// speakQueueWait is how long a speaker waits for the one before it; past
// that it gives up rather than pile up behind a stuck engine.
const speakQueueWait = 90 * time.Second

func init() {
	Register(ActionNotify, handleNotify)
	Register(ActionSpeak, handleSpeak)
}

// xmlText escapes s for XML character data and attribute values.
func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// toastXML is the toast document: title (when set) and text, the default
// notification sound, and deliberately nothing else — no launch argument,
// no action buttons, no links (§7).
func toastXML(title, text string) string {
	var b strings.Builder
	b.WriteString(`<toast><visual><binding template="ToastGeneric">`)
	if title != "" {
		b.WriteString("<text>" + xmlText(title) + "</text>")
	}
	b.WriteString("<text>" + xmlText(text) + "</text>")
	b.WriteString(`</binding></visual><audio src="ms-winsoundevent:Notification.Default"/></toast>`)
	return b.String()
}

// encodedCommand is script in the UTF-16LE base64 form -EncodedCommand
// takes.
func encodedCommand(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// powershellExe is Windows PowerShell by absolute path, so a powershell.exe
// earlier on the user's PATH is never the one started.
func powershellExe() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\System32\WindowsPowerShell\v1.0\powershell.exe`
}

// toastCommand builds the PowerShell process that shows one toast.
func toastCommand(title, text string) *exec.Cmd {
	cmd := exec.Command(powershellExe(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodedCommand(toastScript))
	cmd.Env = append(userEnviron(),
		toastXMLEnv+"="+toastXML(title, text),
		toastAppIDEnv+"="+ToastAppID)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// showToast runs the toast script. It returns "shown" when PowerShell
// finished cleanly within toastWait, "pending" when it is still starting
// (it is left running and shows the toast on its own), or an error.
var showToast = func(title, text string) (string, error) {
	cmd := toastCommand(title, text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start PowerShell: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if line, _, _ := strings.Cut(msg, "\n"); line != "" {
				return "", fmt.Errorf("toast: %v: %s", err, strings.TrimSpace(line))
			}
			return "", fmt.Errorf("toast: %w", err)
		}
		return "shown", nil
	case <-time.After(toastWait):
		return "pending", nil
	}
}

// resolveVoice is sapi.Resolve, replaced by the tests.
var resolveVoice = sapi.Resolve

// startSpeaker launches the detached `user-action speak` child. Replaced
// by the tests.
var startSpeaker = func(text, voice string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, speakerArgs(text, voice)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Nobody waits for it: it outlives this process on purpose.
	return cmd.Process.Release()
}

// speakerArgs is the argument vector of the detached speaker.
func speakerArgs(text, voice string) []string {
	args := []string{"user-action", ActionSpeak, "--text", text}
	if voice != "" {
		args = append(args, "--voice", voice)
	}
	return args
}

// handleNotify shows the toast and, with --speak, starts reading the text.
//
// Reply fields: toast ("shown" or "pending"); with --speak also spoken
// (the reader was started), voice_used (the voice it reads with) and
// voice_found (false when --voice matched no installed voice and the
// system default is used). A speech failure does not fail the toast that
// was already shown: spoken is false and speak_error says why.
func handleNotify(req Request) (map[string]any, error) {
	state, err := showToast(req.Title, req.Text)
	if err != nil {
		return nil, Failed("%v", err)
	}
	out := map[string]any{"toast": state}
	if !req.Speak {
		return out, nil
	}
	out["spoken"] = false
	used, found, err := resolveVoice(req.Voice)
	if err != nil {
		out["speak_error"] = err.Error()
		return out, nil
	}
	out["voice_used"] = used
	out["voice_found"] = found
	// Hand the speaker the exact name, or nothing for the default: it
	// matches again, and an exact name only matches itself.
	voice := ""
	if found {
		voice = used
	}
	if err := startSpeaker(req.Text, voice); err != nil {
		out["speak_error"] = "start speaker: " + err.Error()
		return out, nil
	}
	out["spoken"] = true
	return out, nil
}

// handleSpeak is the detached reader: wait for the speaker before it, read
// the text, exit. Its reply line goes nowhere (nobody reads its output),
// but it keeps the one-line contract anyway.
func handleSpeak(req Request) (map[string]any, error) {
	// A mutex belongs to the thread that took it, so take, speak and
	// release on one OS thread (sapi nests its own lock).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	name, _ := windows.UTF16PtrFromString(speakMutexName)
	h, err := windows.CreateMutex(nil, false, name)
	if h == 0 {
		return nil, Failed("speak queue: %v", err)
	}
	defer windows.CloseHandle(h)
	switch ev, err := windows.WaitForSingleObject(h, uint32(speakQueueWait/time.Millisecond)); {
	case err != nil:
		return nil, Failed("speak queue: %v", err)
	case ev == uint32(windows.WAIT_TIMEOUT):
		return nil, Failed("speak queue: still busy after %v", speakQueueWait)
	}
	defer windows.ReleaseMutex(h)

	used, err := sapi.Speak(req.Text, req.Voice)
	if err != nil {
		return nil, Unsupported("%v", err)
	}
	return map[string]any{"voice_used": used}, nil
}
