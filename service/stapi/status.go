package stapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/status"

	"golang.org/x/sys/windows"
)

// ---- status (§3.2) ---------------------------------------------------------

// Status is the §3.2 status document. Push bodies carry the very same
// object (§3.5), so the driver never needs a diff.
type Status struct {
	Protocol          int                 `json:"protocol"`
	ServiceVersion    string              `json:"service_version"`
	MachineID         string              `json:"machine_id"`
	Hostname          string              `json:"hostname"`
	Power             string              `json:"power"`
	UptimeSeconds     int64               `json:"uptime_seconds"`
	LastShutdownClean bool                `json:"last_shutdown_clean"`
	SecretSet         bool                `json:"secret_set"`
	Grace             status.Grace        `json:"grace"`
	Schedule          map[string]any      `json:"schedule"`
	LastCommand       *status.LastCommand `json:"last_command"`
	Update            status.Update       `json:"update"`
	WoL               status.WoL          `json:"wol"`
	Display           string              `json:"display"`
	Session           status.Session      `json:"session"`
	// Features names the v1.2.0 additions this service offers
	// (docs/design/media-notify.md §3); the driver checks it before it
	// shows or sends anything that needs them. An older service has no
	// key at all. Always an array, never null.
	Features []string `json:"features"`
	// Awake is the keep-awake state (#111, §12).
	Awake status.Awake `json:"awake"`
	// Battery is the newest GetSystemPowerStatus reading (#112, §13). A
	// desktop reports present=false, and "battery" is then left out of
	// Features so the driver keeps the profile without a battery.
	Battery status.Battery `json:"battery"`
	// Activity is the opt-in running-app block (#110, #123, §11): one
	// entry per filled watch slot (1–5) in slot order, and the top one
	// (the running app in the lowest slot).
	Activity status.Activity `json:"activity"`
	// Audio is the default playback device's last known state (#104, §3).
	Audio status.Audio `json:"audio"`
	// Media is the system media session (#117, §15): the status, and the
	// track with the media.now_playing opt-in.
	Media status.Media `json:"media"`
	// Presets are the registered preset slots and their names (#109, §10),
	// in slot order; never null. What a slot runs never leaves this PC.
	Presets []status.PresetRef `json:"presets"`
}

// features lists what this service supports right now. Some entries depend
// on the machine (a battery) rather than on the version, and some on a
// setting: "activity" is listed only while activity.enabled is on, the
// media entries only while media.enabled is.
func features(b status.Battery, cfg config.Config) []string {
	out := []string{"awake"}
	if b.Present {
		out = append(out, "battery")
	}
	if cfg.Activity.Enabled {
		out = append(out, "activity")
	}
	out = append(out, mediaFeatures(cfg)...)
	// Unlike media, "notify" (#106) is listed whatever notify_pc.enabled
	// says: the driver sends and shows the 403 notify_disabled as "PC 알림
	// 꺼짐", where a missing feature would read as "not supported".
	out = append(out, "notify")
	// "presets" (#109) likewise, even with no preset registered: the driver
	// then says "slot empty" instead of "not supported", and hides the empty
	// slots through status presets.
	out = append(out, "presets")
	return out
}

// mediaFeatures are the §3 features entries media.enabled turns on, plus
// "nowplaying" with the media.now_playing opt-in (#117).
func mediaFeatures(cfg config.Config) []string {
	if !cfg.Media.Enabled {
		return nil
	}
	if cfg.Media.NowPlaying {
		return []string{"audio", "media", "nowplaying"}
	}
	return []string{"audio", "media"}
}

// presetList is the status "presets" array: slot order, never null.
func presetList(ps []config.Preset) []status.PresetRef {
	out := make([]status.PresetRef, 0, len(ps))
	for _, p := range config.NormalizePresets(ps) {
		out = append(out, status.PresetRef{Slot: p.Slot, Name: p.Name})
	}
	return out
}

// ---- wol (§3.2, #96) -------------------------------------------------------

// wolCacheTTL is how long the adapter scan is kept: the driver polls the
// status every 10–30s and each scan runs a PowerShell query.
const wolCacheTTL = time.Minute

type wolCache struct {
	mu  sync.Mutex
	val status.WoLStatus
	at  time.Time
}

// wolScan returns the adapter scan, refreshing it at most once per
// wolCacheTTL.
func (s *Server) wolScan() status.WoLStatus {
	s.wol.mu.Lock()
	defer s.wol.mu.Unlock()
	if s.wol.at.IsZero() || time.Since(s.wol.at) > wolCacheTTL {
		s.wol.val = s.d.Status.WoLScan()
		s.wol.at = time.Now()
	}
	return s.wol.val
}

// ResetWoLCache drops the cached scan, so the next status scans again
// (tests).
func (s *Server) ResetWoLCache() {
	s.wol.mu.Lock()
	s.wol.val, s.wol.at = status.WoLStatus{}, time.Time{}
	s.wol.mu.Unlock()
}

// WoLView builds the §3.2 wol block from the (cached) adapter scan, with
// the adapter cfg selects marked, and alongside it the pick the automatic
// rule would make whatever wol_mac says. The app's adapter dropdown labels
// its first entry with that second value ("자동 (이더넷 ·
// B4-2E-99-45-B4-F5)"), so it has to be computed even while a manual MAC
// is in force. Both are null when this PC has no adapter with a MAC. The
// config is read on every call, so a changed wol_mac takes effect on the
// next poll without a restart.
func (s *Server) WoLView(cfg config.SmartThingsConfig) (block status.WoL, auto *status.WoLSelected) {
	scan := s.wolScan()
	hubIP := s.lastHubLocalIP()

	sel, haveSel := selectWoLAdapter(scan.Adapters, cfg.WoLMAC, hubIP)
	if autoSel, ok := selectWoLAdapter(scan.Adapters, "", hubIP); ok {
		auto = selectedView(autoSel)
	}

	block = status.WoL{Adapters: []status.WoLAdapter{}}
	if haveSel {
		block.Selected = selectedView(sel)
		// §3.2: readiness is about the adapter that will actually be woken,
		// not about "some adapter, somewhere, has WoL on".
		block.Ready = sel.WoLEnabled
	}
	for _, a := range scan.Adapters {
		mac := config.NormalizeMAC(a.MacAddress)
		block.Adapters = append(block.Adapters, status.WoLAdapter{
			Name:       a.Name,
			MAC:        a.MacAddress,
			IP:         adapterIPv4(a),
			WoLEnabled: a.WoLEnabled,
			WoLCapable: a.WoLCapable,
			Selected:   haveSel && mac != "" && mac == sel.MAC,
		})
	}
	return block, auto
}

// selectedView is the wire form of a choice.
func selectedView(s wolSelection) *status.WoLSelected {
	return &status.WoLSelected{
		Name:       s.Name,
		MAC:        s.MAC,
		IP:         s.IP,
		WoLEnabled: s.WoLEnabled,
		WoLCapable: s.WoLCapable,
		Source:     s.Source,
	}
}

// ---- schedule and session --------------------------------------------------

// scheduleView is the schedule slot under the §3.2 key names. The wire
// form of /api/schedule (executeAt/remainingSec) stays as it is for the
// app.
func (s *Server) scheduleView() map[string]any {
	sch := s.d.Status.Schedule()
	if sch["active"] != true {
		return map[string]any{"active": false}
	}
	return map[string]any{
		"active":            true,
		"command":           sch["command"],
		"origin":            sch["origin"],
		"remaining_seconds": sch["remainingSec"],
		"execute_at":        sch["executeAt"], // RFC3339, local offset
	}
}

// sessionInfo builds the session block for the live config. The lock
// state and the user name come from WTS; the idle time comes from the tray
// app's heartbeat (#77) and is independent of them, so a machine with no
// tray app still reports the lock state and one with WTS refusing still
// reports the idle time.
func (s *Server) sessionInfo(cfg config.SmartThingsConfig) status.Session {
	if !cfg.ExposeSession {
		return status.Session{Exposed: false}
	}
	out := status.Session{Exposed: true}
	if idle, ok := s.d.Status.IdleSeconds(); ok {
		out.IdleSeconds = &idle
	}
	info, err := s.d.Status.Session()
	if err != nil {
		// Nobody is logged in, or WTS refused: locked stays null rather
		// than guessing.
		logx.Printf("ST API: session info unavailable: %v", err)
		return out
	}
	locked := info.Locked
	out.Locked = &locked
	if cfg.ExposeSessionUser {
		out.User = info.User
	}
	return out
}

// handleStatus serves GET /st/v1/status.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.BuildStatus(s.d.Config()))
}

// BuildStatus assembles the §3.2 status document for cfg.
func (s *Server) BuildStatus(cfg config.Config) Status {
	src := s.d.Status
	bat := src.Battery()
	block, _ := s.WoLView(cfg.SmartThings)
	return Status{
		Protocol:       Protocol,
		ServiceVersion: s.d.Version(),
		MachineID:      src.MachineID(),
		Hostname:       src.Hostname(),
		// Answering at all proves the PC is awake (§3.2); sleep and
		// shutdown are the driver's job to infer.
		Power: "on",
		// Uptime describes the PC, not this process: the driver shows it
		// next to the power state.
		UptimeSeconds:     int64(windows.DurationSinceBoot() / time.Second),
		LastShutdownClean: src.LastShutdownClean(),
		SecretSet:         cfg.Secret != "",
		Grace:             status.Grace{Enabled: cfg.ShutdownGrace, Seconds: int(cfg.GraceDuration() / time.Second)},
		Schedule:          s.scheduleView(),
		LastCommand:       src.LastCommand(),
		Update:            src.Update(),
		WoL:               block,
		Display:           src.Display(),
		Session:           s.sessionInfo(cfg.SmartThings),
		Features:          features(bat, cfg),
		Awake:             s.d.Awake.View().Wire(),
		Battery:           bat,
		Activity:          src.Activity(cfg),
		Audio:             src.Audio(cfg),
		Media:             src.Media(cfg),
		Presets:           presetList(cfg.Presets),
	}
}
