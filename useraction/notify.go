package useraction

// PC notification (#106, docs/design/media-notify.md §2·§7): a toast with
// the title and text.
//
// The toast is not built with go-toast, which the tray app uses for its own
// fixed grace-period strings. go-toast pastes the title and message into a
// PowerShell double-quoted here-string, where "$(…)" is evaluated and a
// line starting with "@ ends the string, so a notification text from the
// network would be code. Here the toast XML is escaped in Go and handed to
// a fixed script through an environment variable: no user text ever
// appears in a script or on a command line.

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
)

// ToastAppID is the AppUserModelID a toast is shown under: the one the
// Start menu shortcut carries (internal/appid), which the tray app's
// go-toast notifications use too, so both land in one group. Without that
// shortcut Windows files the toast in the notification center but never
// shows its banner.
const ToastAppID = appid.AUMID

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

func init() {
	Register(ActionNotify, handleNotify)
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

// ensureShortcut creates or repairs the Start menu shortcut that carries
// ToastAppID. Replaced by the tests.
var ensureShortcut = func() error {
	_, err := appid.EnsureToastShortcut()
	return err
}

// handleNotify shows the toast.
//
// The Start menu shortcut is checked first: this child may be the first
// thing of the app to run in the session (the tray app, which makes it
// too, need not be running). When it cannot be made the toast is still
// sent, and the reply carries shortcut: "failed: …".
//
// Reply fields: toast ("shown" or "pending").
func handleNotify(req Request) (map[string]any, error) {
	shortcutErr := ensureShortcut()
	state, err := showToast(req.Title, req.Text)
	if err != nil {
		if shortcutErr != nil {
			return nil, Failed("%v (shortcut: %v)", err, shortcutErr)
		}
		return nil, Failed("%v", err)
	}
	out := map[string]any{"toast": state}
	if shortcutErr != nil {
		out["shortcut"] = "failed: " + shortcutErr.Error()
	}
	return out, nil
}
