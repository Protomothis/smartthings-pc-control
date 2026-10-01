package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
	"github.com/Protomothis/smartthings-pc-control/useraction"
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

// ensureToastShortcut creates or repairs the Start menu shortcut whose
// AUMID lets toasts show as banners (internal/appid). Called on every GUI
// start, off the UI thread; cheap (a read) when the shortcut is right.
// Best effort like registerToastProtocol: the GUI has no console to report
// to, and without the shortcut toasts still reach the notification center.
func ensureToastShortcut() { appid.EnsureToastShortcut() }

// graceToastActions are the grace-period toast's Run now / Cancel buttons,
// which launch stpc:// back into this exe (registerToastProtocol).
func graceToastActions(lang Lang) []useraction.ToastAction {
	return []useraction.ToastAction{
		{Label: T(lang, "toast.runnow"), Arguments: protocolScheme + "://runnow"},
		{Label: T(lang, "toast.cancel"), Arguments: protocolScheme + "://cancel"},
	}
}

// showGraceToast pops a Windows toast with Run now / Cancel buttons for the
// scheduled command. title and message are already localised (they differ
// by origin, see #54). It is the PC notification's toast path
// (useraction.ShowToast): escaped XML, a fixed script, and the Start menu
// shortcut's AUMID (with any other app ID Windows keeps the toast out of
// sight in the notification center). Returns an error so the caller can
// fall back to a plain Fyne notification.
func showGraceToast(lang Lang, title, message string) error {
	return useraction.ShowToast(title, message, graceToastActions(lang))
}

// HandleToastAction is invoked as `exe toast stpc://...` when the user
// clicks a toast button. It talks to the service API and exits.
func HandleToastAction(rawURL string) {
	// A process of its own (`exe toast …`): Run never set webUIPort here.
	runToastAction(NewClient(localWebUIPort()), rawURL)
}

// toastAPI is what the toast buttons use of the service; *Client, or a
// fake in the tests.
type toastAPI interface {
	GetConfig() (Config, error)
	LocalLogin() error
	GetSchedule() (Schedule, error)
	CancelSchedule(by string) error
	TestCommand(name string) (string, error)
}

// runToastAction carries out one toast button.
func runToastAction(c toastAPI, rawURL string) {
	// The schedule API needs a session when a secret is configured. This
	// process is the same exe in the same session as the tray, so the
	// service vouches for it like for the tray (#131); GetConfig only
	// tells whether a session is needed. Refused, the calls below fail
	// with 401 and nothing happens — the toast has no window to ask in.
	if _, err := c.GetConfig(); errors.Is(err, errUnauthorized) {
		_ = c.LocalLogin() // refused: see above
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

// trayConfigFile is the service's user-readable copy of the settings the
// app needs without a session (service/private_files.go). config.json
// itself, with the secret, is SYSTEM and Administrators only since #131;
// the app no longer reads it at all.
const trayConfigFile = "tray.json"

// localConfig is tray.json: what the GUI needs before it can talk to the
// service — the SmartThings port, because the WebUI/API listens on
// port+1 — and the heartbeat's switches.
type localConfig struct {
	Port int `json:"port"`
	// SmartThings carries the one flag the idle heartbeat needs (#77);
	// reading the file is cheaper than an authenticated /api/config call
	// every 30s, and the service rewrites it on every save.
	SmartThings struct {
		ExposeSession bool `json:"expose_session"`
	} `json:"smartthings"`
	// Media gates the heartbeat's audio block (#104). A pointer because a
	// missing key means on, the service's default.
	Media struct {
		Enabled *bool `json:"enabled"`
		// NowPlaying is the opt-in for the track in the media block
		// (#117); missing means off.
		NowPlaying bool `json:"now_playing"`
	} `json:"media"`
}

// readLocalConfig parses tray.json next to the exe; zero values when the
// file is missing or unreadable (an older service, or one that has not
// started yet: the default port, and port.go follows the real one later).
func readLocalConfig() localConfig {
	exe, err := os.Executable()
	if err != nil {
		return localConfig{}
	}
	return readLocalConfigFile(filepath.Join(filepath.Dir(exe), trayConfigFile))
}

// readLocalConfigFile is readLocalConfig for one path.
func readLocalConfigFile(path string) localConfig {
	var cfg localConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	json.Unmarshal(data, &cfg)
	return cfg
}

// localExposeSession reports whether the service currently publishes the
// session block (smartthings.expose_session, edge-driver doc §3.2). A
// missing key means off, matching the service's default.
func localExposeSession() bool { return readLocalConfig().SmartThings.ExposeSession }

// localMediaEnabled reports media.enabled (#104); a missing key (or no
// tray.json at all) means on, matching the service's default.
func localMediaEnabled() bool {
	on := readLocalConfig().Media.Enabled
	return on == nil || *on
}

// localNowPlaying reports the media.now_playing opt-in (#117): whether the
// heartbeat's media block may carry the track and the app.
func localNowPlaying() bool { return readLocalConfig().Media.NowPlaying }

// localWebUIPort returns the service's WebUI/API port (SmartThings port +
// 1, matching service/webui.go), defaulting to 5002 when tray.json has
// no usable port. Read at startup, and again while the service is
// unreachable to follow a port change once it has restarted (port.go).
func localWebUIPort() int {
	if p := readLocalConfig().Port; p >= 1 && p < 65535 {
		return p + 1
	}
	return defaultWebUIPort
}
