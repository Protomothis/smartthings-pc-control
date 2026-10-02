// Package status holds the wire form of the blocks the service reports
// about this PC (docs/design/edge-driver.md §3.2, media-notify.md): the
// pieces of GET /st/v1/status, which the push bodies, the WebUI status
// card, the app's /api endpoints and the Telegram /status reply reuse.
//
// It is data only. The root service fills the blocks from its stores;
// service/stapi, service/webui and service/tgcontrol read them through the
// small interfaces they declare, so none of them needs the others or the
// stores (#127).
package status

import (
	"slices"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/devstate"
)

// Grace is the grace-period block.
type Grace struct {
	Enabled bool `json:"enabled"`
	Seconds int  `json:"seconds"`
}

// LastCommand is the last remote command, null in the status before one.
type LastCommand struct {
	Command string `json:"command"`
	Origin  string `json:"origin"`
	At      string `json:"at"`
	// Preset and Result are set for a preset command (#109): which slot
	// ran and "started" or the error code.
	Preset *PresetRef `json:"preset,omitempty"`
	Result string     `json:"result,omitempty"`
}

// Update is the newest release this service knows about.
type Update struct {
	Available bool   `json:"available"`
	Latest    string `json:"latest"`
}

// WoL is the §3.2 wol block. Selected is the adapter the driver must
// address its magic packet to (#96, #97); it is null only when this PC has
// no adapter with a MAC at all, and Ready describes that one adapter
// rather than "any adapter somewhere".
type WoL struct {
	Ready    bool         `json:"ready"`
	Selected *WoLSelected `json:"selected"`
	Adapters []WoLAdapter `json:"adapters"`
}

// WoLSelected names the chosen adapter and says who chose it: "manual"
// when smartthings.wol_mac matched it, "auto" otherwise.
type WoLSelected struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	WoLEnabled bool   `json:"wol_enabled"`
	WoLCapable bool   `json:"wol_capable"`
	Source     string `json:"source"`
}

// WoLAdapter is one adapter of the wol block.
type WoLAdapter struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	WoLEnabled bool   `json:"wol_enabled"`
	WoLCapable bool   `json:"wol_capable"`
	Selected   bool   `json:"selected"`
}

// Session is the opt-in session block (§3.2). Everything but Exposed is
// omitted while smartthings.expose_session is off; Locked is null when the
// service cannot read the session state, and IdleSeconds is null unless the
// tray app posted a heartbeat within the heartbeat TTL (#77).
type Session struct {
	Exposed     bool   `json:"exposed"`
	Locked      *bool  `json:"locked,omitempty"`
	IdleSeconds *int64 `json:"idle_seconds,omitempty"`
	User        string `json:"user,omitempty"`
}

// AwakeView is the keep-awake state (#111). Until is zero while off and
// while on without a time limit.
type AwakeView struct {
	On    bool
	Until time.Time
}

// Awake is the status "awake" block: until is RFC3339, or "" when off or
// on until turned off.
type Awake struct {
	On    bool   `json:"on"`
	Until string `json:"until"`
}

// Wire is the status block for v.
func (v AwakeView) Wire() Awake {
	out := Awake{On: v.On}
	if v.On && !v.Until.IsZero() {
		out.Until = v.Until.Format(time.RFC3339)
	}
	return out
}

// Battery is the newest GetSystemPowerStatus reading (#112, §13).
type Battery = devstate.BatteryInfo

// Activity is the §11 status block, also the activity.changed push data
// (#110, #123).
type Activity struct {
	Enabled bool `json:"enabled"`
	// Apps has one entry per filled watch slot, sorted by slot; never null.
	Apps []ActivityApp `json:"apps"`
	// Top is the id of the running app in the lowest slot, "" when none.
	Top string `json:"top"`
}

// ActivityApp is one watched program in the status block. Slot (1–5) is
// its priority and the SmartThings condition "감시 N" it answers to.
type ActivityApp struct {
	Slot    int    `json:"slot"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	Running bool   `json:"running"`
}

// Equal compares two blocks, apps in order.
func (a Activity) Equal(b Activity) bool {
	return a.Enabled == b.Enabled && a.Top == b.Top && slices.Equal(a.Apps, b.Apps)
}

// Clone copies the block so a caller cannot alias the scanner's slice.
func (a Activity) Clone() Activity {
	a.Apps = slices.Clone(a.Apps)
	if a.Apps == nil {
		a.Apps = []ActivityApp{}
	}
	return a
}

// TopLabel is the label of the top app (the lowest running slot) and how
// many others run.
func (a Activity) TopLabel() (label string, others int) {
	for _, app := range a.Apps {
		if !app.Running {
			continue
		}
		if app.ID == a.Top && label == "" {
			label = app.Label
			continue
		}
		others++
	}
	return label, others
}

// Audio is the default playback device's last known state (#104, §3).
// Available is false — and every other key absent — while there is
// nothing trustworthy to report: nobody is logged in, no sample has
// arrived since the service started, or media.enabled is off. Device is a
// pointer so that a device without a name still reports "" rather than
// dropping the key.
type Audio struct {
	Available bool    `json:"available"`
	Volume    *int    `json:"volume,omitempty"`
	Muted     *bool   `json:"muted,omitempty"`
	Device    *string `json:"device,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
}

// Media is the §15 block (#117). status is always there: "none" while no
// session plays and also while nothing trustworthy is known (media.enabled
// off, nobody logged in, no fresh sample). The text fields appear only
// with the media.now_playing opt-in and only when the app set them.
type Media struct {
	Status    string `json:"status"`
	Title     string `json:"title,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Album     string `json:"album,omitempty"`
	App       string `json:"app,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// PresetRef is one {slot, name} of status "presets" (#109, §10).
type PresetRef struct {
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

// WoLStatus is the adapter scan behind the wol block and GET
// /api/wol-status: every physical adapter with its WoL state.
type WoLStatus struct {
	Adapters   []NetAdapter `json:"adapters"`
	ExternalIP string       `json:"externalIP,omitempty"`
	Ready      bool         `json:"ready"`             // true if at least one active adapter has WoL enabled
	Warning    string       `json:"warning,omitempty"` // non-fatal warning (e.g., WoL query failed)
	Error      string       `json:"error,omitempty"`
}

// NetAdapter is one physical network adapter of the scan.
type NetAdapter struct {
	Name       string   `json:"name"`
	MacAddress string   `json:"mac"`
	IPs        []string `json:"ips"`
	Status     string   `json:"status"` // "Up" or "Down"
	WoLEnabled bool     `json:"wolEnabled"`
	WoLCapable bool     `json:"wolCapable"`
}

// HubStale is 2× the longest poll interval the driver offers (5 min,
// edge-driver doc §7), after which the app calls the hub disconnected.
const HubStale = 10 * time.Minute

// HubSeen is the last authenticated /st/v1 caller. The app's SmartThings
// section (#70) shows it as "hub 192.168.1.20 · driver v1.0.0 · 3s ago".
type HubSeen struct {
	IP            string
	DriverVersion string
	At            time.Time
}

// SSDPSearch is the last M-SEARCH this PC matched: which address sent it
// and when (#95). The app shows it as "마지막 검색 요청 192.168.1.105, 12초
// 전", the only evidence a user has that the hub's search reached the PC.
type SSDPSearch struct {
	IP string
	At time.Time
}
