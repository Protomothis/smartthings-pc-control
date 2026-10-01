package service

// The browser page (#122, refactor-plan §4): the desktop app is the
// settings UI, so the page served on / is cut down to what someone needs
// from another device — status, power commands, a schedule and the core
// settings (port, secret, remote access, grace). Everything else is edited
// in the app.

import (
	"net/http"
	"time"

	"golang.org/x/sys/windows"
)

// settingsView is what settings.html is rendered with. The secret itself is
// never handed to the page: it only learns whether one is set, and a new
// one replaces it on save.
type settingsView struct {
	Version       string
	Port          int
	SecretSet     bool
	WebUIRemote   bool
	ShutdownGrace bool
	GraceSeconds  int
}

func newSettingsView(cfg Config) settingsView {
	return settingsView{
		Version:       Version,
		Port:          cfg.Port,
		SecretSet:     cfg.Secret != "",
		WebUIRemote:   cfg.WebUIRemote,
		ShutdownGrace: cfg.ShutdownGrace,
		GraceSeconds:  int(cfg.graceDuration() / time.Second),
	}
}

// webUIStatus is GET /api/status: the page's status card in one poll.
type webUIStatus struct {
	Version       string         `json:"version"`
	Update        stUpdate       `json:"update"`
	Hostname      string         `json:"hostname"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	Display       string         `json:"display"`
	Session       stSession      `json:"session"`
	SmartThings   webUIHubState  `json:"smartthings"`
	Grace         stGrace        `json:"grace"`
	Schedule      map[string]any `json:"schedule"`
}

// webUIHubState is the Edge driver's last contact, as /api/st/hub reports
// it but without the diagnostics the app's network tab shows.
type webUIHubState struct {
	Connected     bool   `json:"connected"`
	LastSeen      string `json:"last_seen"`
	DriverVersion string `json:"driver_version"`
}

// webUIUptime is how long the PC has been up; a variable so tests need not
// depend on the machine.
var webUIUptime = windows.DurationSinceBoot

// webUISession is the session block under the SmartThings exposure
// settings: the page shows no more than the driver may. Unlike the driver's
// status it does not log a failed query — the page polls every 5 seconds
// and "nobody is signed in" is a normal answer.
func webUISession(cfg SmartThingsConfig) stSession {
	if !cfg.ExposeSession {
		return stSession{Exposed: false}
	}
	out := stSession{Exposed: true}
	info, err := stSessionQuery()
	if err != nil {
		return out
	}
	locked := info.Locked
	out.Locked = &locked
	if cfg.ExposeSessionUser {
		out.User = info.User
	}
	return out
}

func buildWebUIStatus(cfg Config) webUIStatus {
	out := webUIStatus{
		Version:       Version,
		Update:        stUpdateInfo(),
		Hostname:      hostname(),
		UptimeSeconds: int64(webUIUptime() / time.Second),
		Display:       getDisplayState(),
		Session:       webUISession(cfg.SmartThings),
		Grace:         stGrace{Enabled: cfg.ShutdownGrace, Seconds: int(cfg.graceDuration() / time.Second)},
		Schedule:      getSchedule(),
	}
	if seen, ok := hubLastSeenInfo(); ok {
		out.SmartThings = webUIHubState{
			Connected:     time.Since(seen.At) <= stHubStale,
			LastSeen:      seen.At.Format(time.RFC3339),
			DriverVersion: seen.DriverVersion,
		}
	}
	return out
}

// handleWebUIStatusAPI serves GET /api/status behind the session cookie.
func handleWebUIStatusAPI(w http.ResponseWriter, r *http.Request) {
	liveCfg := getConfig()
	if !checkAuth(r, liveCfg.Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, buildWebUIStatus(liveCfg))
}
