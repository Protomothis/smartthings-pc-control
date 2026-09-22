package gui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-toast/toast"
	"golang.org/x/sys/windows/registry"
)

// protocolScheme is the custom URI scheme the toast action buttons launch.
// It is registered per-user (HKCU, no elevation) and points back at this exe.
const protocolScheme = "stpc"

// registerToastProtocol (re-)registers stpc:// to this exe so toast buttons
// keep working after the exe moves. Called on every GUI start; idempotent.
func registerToastProtocol() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	root, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme, registry.ALL_ACCESS)
	if err != nil {
		return
	}
	defer root.Close()
	root.SetStringValue("", "URL:SmartThings PC Control")
	root.SetStringValue("URL Protocol", "")

	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme+`\shell\open\command`, registry.ALL_ACCESS)
	if err != nil {
		return
	}
	defer cmd.Close()
	cmd.SetStringValue("", fmt.Sprintf(`"%s" toast "%%1"`, exe))
}

// showGraceToast pops a Windows toast with Run now / Cancel buttons for the
// scheduled command. title and message are already localised (they differ
// by origin, see #54). Returns an error so the caller can fall back to a
// plain Fyne notification.
func showGraceToast(lang Lang, title, message string) error {
	n := toast.Notification{
		AppID:   windowTitle,
		Title:   title,
		Message: message,
		Actions: []toast.Action{
			{Type: "protocol", Label: T(lang, "toast.runnow"), Arguments: protocolScheme + "://runnow"},
			{Type: "protocol", Label: T(lang, "toast.cancel"), Arguments: protocolScheme + "://cancel"},
		},
	}
	return n.Push()
}

// HandleToastAction is invoked as `exe toast stpc://...` when the user
// clicks a toast button. It talks to the service API and exits.
func HandleToastAction(rawURL string) {
	c := NewClient(webUIPort)

	// The schedule API needs a session when a secret is configured; the
	// secret lives in config.json next to the exe.
	if secret := localSecret(); secret != "" {
		c.Login(secret)
	}

	switch {
	case strings.Contains(rawURL, "cancel"):
		c.CancelSchedule("toast")
	case strings.Contains(rawURL, "runnow"):
		s, err := c.GetSchedule()
		if err != nil || !s.Active {
			return
		}
		if err := c.CancelSchedule("toast"); err != nil {
			return
		}
		c.TestCommand(s.Command)
	}
}

// localConfig is the subset of config.json (next to the exe) the GUI needs
// before it can talk to the service: the secret for the API session and
// the SmartThings port, because the WebUI/API listens on port+1.
type localConfig struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
	// SmartThings carries the one flag the idle heartbeat needs (#77);
	// reading the file is cheaper than an authenticated /api/config call
	// every 30s, and the service rewrites it on every save.
	SmartThings struct {
		ExposeSession bool `json:"expose_session"`
	} `json:"smartthings"`
}

// readLocalConfig parses config.json next to the exe; zero values when
// the file is missing or unreadable.
func readLocalConfig() localConfig {
	var cfg localConfig
	exe, err := os.Executable()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "config.json"))
	if err != nil {
		return cfg
	}
	json.Unmarshal(data, &cfg)
	return cfg
}

// localSecret reads the secret from config.json next to the exe.
func localSecret() string { return readLocalConfig().Secret }

// localExposeSession reports whether the service currently publishes the
// session block (smartthings.expose_session, edge-driver doc §3.2). A
// missing key means off, matching the service's default.
func localExposeSession() bool { return readLocalConfig().SmartThings.ExposeSession }

// localWebUIPort returns the service's WebUI/API port (SmartThings port +
// 1, matching service/webui.go), defaulting to 5002 when config.json has
// no usable port. Read once at startup: a port change needs a service
// restart anyway.
func localWebUIPort() int {
	if p := readLocalConfig().Port; p >= 1 && p < 65535 {
		return p + 1
	}
	return defaultWebUIPort
}
