package service

// The WebUI port (service/webui): the browser pages and the app's /api,
// wired to the service, and the server that runs them.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/ratelimit"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
	"github.com/Protomothis/smartthings-pc-control/service/webui"
	"github.com/Protomothis/smartthings-pc-control/useraction"

	"golang.org/x/sys/windows"
)

// Version is set by main package at startup
var Version = "dev"

// Failed logins: five wrong secrets in a row lock the address out for a
// minute, on the WebUI login and the legacy URL alike (#120).
const (
	maxLoginFailures  = 5
	loginLockDuration = 60 * time.Second
)

// logins is the lockout both share.
var logins = ratelimit.NewLockout(maxLoginFailures, loginLockDuration, func(ip string) {
	logMsg("Login rate limit triggered for %s (locked %v)", ip, loginLockDuration)
	emit("security", "login_limited", map[string]string{"from": ip})
})

// webSrv is the service's WebUI and /api surface.
var webSrv = webui.New(webui.Deps{
	Config:     getConfig,
	SaveConfig: saveConfig,
	Version:    func() string { return Version },
	Emit:       func(cat, kind string, fields map[string]string) { emit(cat, kind, fields) },
	Restart:    restartSelf,
	Logins:     logins,
	UserToken: func(session uint32) (windows.Token, error) {
		return wts.QueryUserToken(session)
	},
	Status:    sources,
	Hub:       hubInfo{},
	Commands:  uiCommands{},
	Awake:     awakeControl{},
	Media:     mediaControl{},
	Presets:   presetControl{}, // the app's buttons are not last_command
	Notify:    pcNotifier{},
	Telegram:  telegramInfo{},
	Heartbeat: heartbeat,
})

// StartWebUI starts a local web UI for configuration on a separate port
func StartWebUI(stop chan struct{}) {
	cfg := getConfig()
	if cfg.Port == 0 {
		// Console mode starts this concurrently with StartHTTPServer —
		// don't rely on the other goroutine having loaded the config yet.
		cfg = loadConfig()
		setConfig(cfg)
	}
	webPort := cfg.Port + 1 // WebUI runs on port+1 (default: 5002)

	// Browser access is opt-in and only honored with a secret set — without
	// auth, anyone on the LAN could reconfigure and control this PC.
	// When disabled, the HTML pages are blocked (local browsers included;
	// the desktop app is the primary UI) but the JSON API stays available
	// on localhost, since the desktop app talks to the service through it.
	pagesEnabled := cfg.WebUIRemote && cfg.Secret != ""
	bindAddr := "127.0.0.1"
	if pagesEnabled {
		bindAddr = ""
		if err := addWebUIFirewallRule(webPort); err != nil {
			logMsg("WARNING: WebUI firewall rule failed (remote clients may be blocked): %v", err)
		}
	} else {
		if cfg.WebUIRemote && cfg.Secret == "" {
			logMsg("WARNING: webui_remote is enabled but no secret is set — browser WebUI stays disabled. Set a secret first.")
		}
		// Best-effort cleanup when browser access was turned off.
		removeWebUIFirewallRule()
	}

	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", bindAddr, webPort),
		Handler:           webSrv.Handler(webPort, pagesEnabled),
		ReadHeaderTimeout: httpReadHeaderTimeout, // gosec G112, see server.go
	}

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	if bindAddr == "" {
		logMsg("WebUI listening on http://0.0.0.0:%d (remote access enabled)", webPort)
	} else {
		logMsg("WebUI listening on http://127.0.0.1:%d", webPort)
	}
	server.ListenAndServe()
}

// hubInfo is the SmartThings side the app's SmartThings section shows:
// the /st/v1 surface plus the discovery firewall rule.
type hubInfo struct{}

func (hubInfo) HubLastSeen() (status.HubSeen, bool) { return stSrv.HubLastSeen() }
func (hubInfo) WoLView(cfg SmartThingsConfig) (status.WoL, *status.WoLSelected) {
	return stSrv.WoLView(cfg)
}
func (hubInfo) SSDPRunning() bool                         { return stSrv.SSDPRunning() }
func (hubInfo) LastSSDPSearch() (status.SSDPSearch, bool) { return stSrv.LastSSDPSearch() }
func (hubInfo) SSDPFirewallRule() bool                    { return ssdpFirewallRuleOK() }

// uiCommands is the catalogue and the schedule slot on behalf of the UIs
// on this PC: origin "ui", at once (the person asking is at the PC).
type uiCommands struct{}

func (uiCommands) RunNow(name, by string) bool {
	_, ok := dispatchCommand(name, by, originUI, dispatchImmediate)
	return ok
}

func (uiCommands) Schedule(command string, delay time.Duration) error {
	return setSchedule(command, delay, originUI)
}

func (uiCommands) CancelSchedule(by string) bool { return cancelScheduleBy(by) }

// telegramInfo is what the app's notify tab asks about Telegram.
type telegramInfo struct{}

func (telegramInfo) Client(token string) *telegram.Client { return newTelegramClient(token) }
func (telegramInfo) PCName(tg TelegramConfig) string      { return telegramPCName(tg) }
func (telegramInfo) Polling() bool                        { return tgCtl.Running() }
func (telegramInfo) Conflict() (bool, time.Time)          { return tgCtl.Conflict() }

// heartbeatStore takes the tray app's heartbeat into the device stores.
// target is the session the commands act on (targetUserSession); the
// tests replace it so they need no real sessions.
type heartbeatStore struct {
	target func() (uint32, error)
}

var heartbeat = &heartbeatStore{target: targetUserSession}

func (h *heartbeatStore) Now() time.Time                         { return clock.audio() }
func (h *heartbeatStore) Target() (uint32, error)                { return h.target() }
func (h *heartbeatStore) Ignored(from, target uint32)            { noteIgnoredHeartbeat(from, target) }
func (h *heartbeatStore) Idle(seconds int64)                     { noteIdleHeartbeat(seconds) }
func (h *heartbeatStore) Audio(a useraction.Audio, at time.Time) { recordAudioSample(a, at) }
func (h *heartbeatStore) Media(np useraction.NowPlaying, at time.Time) {
	recordMediaSample(np, at)
}
