package webui

// The browser page (#122, refactor-plan §4): the desktop app is the
// settings UI, so the page served on / is cut down to what someone needs
// from another device — status, power commands, a schedule and the core
// settings (port, secret, remote access, grace). Everything else is edited
// in the app.

import (
	"net/http"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/status"
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

func newSettingsView(cfg config.Config, version string) settingsView {
	return settingsView{
		Version:       version,
		Port:          cfg.Port,
		SecretSet:     cfg.Secret != "",
		WebUIRemote:   cfg.WebUIRemote,
		ShutdownGrace: cfg.ShutdownGrace,
		GraceSeconds:  int(cfg.GraceDuration() / time.Second),
	}
}

// StatusCard is GET /api/status: the page's status card in one poll.
type StatusCard struct {
	Version       string         `json:"version"`
	Update        status.Update  `json:"update"`
	Hostname      string         `json:"hostname"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	Display       string         `json:"display"`
	Session       status.Session `json:"session"`
	SmartThings   HubState       `json:"smartthings"`
	Grace         status.Grace   `json:"grace"`
	Schedule      map[string]any `json:"schedule"`
}

// HubState is the Edge driver's last contact, as /api/st/hub reports it
// but without the diagnostics the app's network tab shows.
type HubState struct {
	Connected     bool   `json:"connected"`
	LastSeen      string `json:"last_seen"`
	DriverVersion string `json:"driver_version"`
}

// sessionBlock is the session block under the SmartThings exposure
// settings: the page shows no more than the driver may. Unlike the driver's
// status it does not log a failed query — the page polls every 5 seconds
// and "nobody is signed in" is a normal answer.
func (s *Server) sessionBlock(cfg config.SmartThingsConfig) status.Session {
	if !cfg.ExposeSession {
		return status.Session{Exposed: false}
	}
	out := status.Session{Exposed: true}
	info, err := s.d.Status.Session()
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

func (s *Server) statusCard(cfg config.Config) StatusCard {
	src := s.d.Status
	out := StatusCard{
		Version:       s.d.Version(),
		Update:        src.Update(),
		Hostname:      src.Hostname(),
		UptimeSeconds: int64(s.Uptime() / time.Second),
		Display:       src.Display(),
		Session:       s.sessionBlock(cfg.SmartThings),
		Grace:         status.Grace{Enabled: cfg.ShutdownGrace, Seconds: int(cfg.GraceDuration() / time.Second)},
		Schedule:      src.Schedule(),
	}
	if seen, ok := s.d.Hub.HubLastSeen(); ok {
		out.SmartThings = HubState{
			Connected:     time.Since(seen.At) <= status.HubStale,
			LastSeen:      seen.At.Format(time.RFC3339),
			DriverVersion: seen.DriverVersion,
		}
	}
	return out
}

// serveStatus serves GET /api/status behind the session cookie.
func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.statusCard(s.d.Config()))
}

// ---- SmartThings hub state (#67) -------------------------------------------

// hubView is GET /api/st/hub: what the GUI SmartThings section (#70)
// shows about the Edge driver's last contact. "connected" means the hub
// polled within status.HubStale (2× the longest poll interval the driver
// offers). machine_id and ssdp were added for the search diagnostics
// (#95): they describe this PC, not the hub, so they are filled in even
// when no hub has ever called — that is exactly the case the user needs
// them in.
type hubView struct {
	Connected     bool     `json:"connected"`
	IP            string   `json:"ip"`
	DriverVersion string   `json:"driver_version"`
	LastSeen      string   `json:"last_seen"`
	MachineID     string   `json:"machine_id"`
	SSDP          ssdpView `json:"ssdp"`
	// WoL is the adapter choice (#96) the section's dropdown edits. It
	// rides on this poll rather than on a second endpoint so the whole
	// section refreshes in one round trip.
	WoL hubWoLView `json:"wol"`
}

// hubWoLView is what the app needs to draw the WoL adapter dropdown:
// the adapter in force, the one the automatic rule would choose (the
// first dropdown entry names it even while a manual MAC overrides it) and
// the list to choose from. Selected and Auto are null when this PC has no
// adapter with a MAC.
type hubWoLView struct {
	Selected *status.WoLSelected `json:"selected"`
	Auto     *status.WoLSelected `json:"auto"`
	Adapters []status.WoLAdapter `json:"adapters"`
}

// ssdpView is the responder's state: whether it holds a socket, whether
// the inbound UDP 1900 rule was found, and the last M-SEARCH this PC
// matched (null until one arrives).
type ssdpView struct {
	Running      bool        `json:"running"`
	FirewallRule bool        `json:"firewall_rule"`
	LastSearch   *searchView `json:"last_search"`
}

type searchView struct {
	IP string `json:"ip"`
	At string `json:"at"`
}

// ssdpStatus assembles the responder block.
func (s *Server) ssdpStatus() ssdpView {
	out := ssdpView{Running: s.d.Hub.SSDPRunning(), FirewallRule: s.d.Hub.SSDPFirewallRule()}
	if search, ok := s.d.Hub.LastSSDPSearch(); ok {
		out.LastSearch = &searchView{IP: search.IP, At: search.At.Format(time.RFC3339)}
	}
	return out
}

// serveSTHub serves GET /api/st/hub.
func (s *Server) serveSTHub(w http.ResponseWriter, r *http.Request) {
	liveCfg := s.d.Config()
	wol, auto := s.d.Hub.WoLView(liveCfg.SmartThings)
	view := hubView{
		MachineID: s.d.Status.MachineID(),
		SSDP:      s.ssdpStatus(),
		WoL:       hubWoLView{Selected: wol.Selected, Auto: auto, Adapters: wol.Adapters},
	}
	if seen, ok := s.d.Hub.HubLastSeen(); ok {
		view.Connected = time.Since(seen.At) <= status.HubStale
		view.IP = seen.IP
		view.DriverVersion = seen.DriverVersion
		view.LastSeen = seen.At.Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}
