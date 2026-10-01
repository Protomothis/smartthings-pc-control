package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"
)

// Config mirrors the service config exposed by /api/config.
//
// POST sends the whole struct back and the service keeps the live value of
// any key that is omitted — so callers must start from the last GET (see
// ui.cfgBaseline) rather than a zero Config, or telegram/notify would be
// reset to zero values.
type Config struct {
	Port          int    `json:"port"`
	Secret        string `json:"secret"`
	WebUIRemote   bool   `json:"webui_remote"`
	ShutdownGrace bool   `json:"shutdown_grace"`
	GraceSeconds  int    `json:"grace_seconds"`
	// Telegram is the "telegram" object (design doc §10); Notify is the
	// "notify" catalogue: Category → Kind → enabled.
	Telegram TelegramConfig             `json:"telegram"`
	Notify   map[string]map[string]bool `json:"notify"`
	// SmartThings is the "smartthings" object (edge-driver doc §3.7).
	SmartThings SmartThingsConfig `json:"smartthings"`
	// Activity is the opt-in running-app detection (#110), edited in the
	// network tab's SmartThings section.
	Activity ActivityConfig `json:"activity"`
	// Media is the "media" object (#104): volume and media-key commands.
	Media MediaConfig `json:"media"`
	// NotifyPC is the "notify_pc" object (#106); Presets the "presets"
	// list (#109): the settings tab's 미디어·알림 section (notify_section.go)
	// and the presets tab (presets_tab.go) edit them.
	NotifyPC NotifyPCConfig `json:"notify_pc"`
	Presets  []Preset       `json:"presets"`
}

// NotifyPCConfig mirrors service.NotifyPCConfig.
type NotifyPCConfig struct {
	Enabled bool `json:"enabled"`
}

// MediaConfig mirrors service.MediaConfig; the settings tab edits it.
type MediaConfig struct {
	Enabled bool `json:"enabled"`
	// NowPlaying is the opt-in to share title/artist/album/app (#117).
	NowPlaying bool `json:"now_playing"`
}

// ActivityConfig mirrors service.ActivityConfig (media-notify doc §11).
// Watch must travel as [] rather than null when emptied: the service reads
// a missing/null list as "keep the stored one".
type ActivityConfig struct {
	Enabled bool            `json:"enabled"`
	Watch   []ActivityWatch `json:"watch"`
}

// ActivityWatch is one watched program: a file name ("steam.exe") and the
// label it is shown by. The list order is the priority (#123).
type ActivityWatch struct {
	Process string `json:"process"`
	Label   string `json:"label"`
}

// Preset mirrors service.Preset: one slot SmartThings and Telegram can
// run by number.
type Preset struct {
	Slot int      `json:"slot"`
	Name string   `json:"name"`
	Type string   `json:"type"` // program | url | script
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}

// SmartThingsConfig mirrors service.SmartThingsConfig. The widgets that
// edit it live in the network tab (#70); this struct only keeps the values
// alive across a GET/POST round trip.
type SmartThingsConfig struct {
	// There is no "discovery" key: SSDP is always on (#95).
	AllowedHubs       []string `json:"allowed_hubs"`
	ExposeSession     bool     `json:"expose_session"`
	ExposeSessionUser bool     `json:"expose_session_user"`
	// WoLMAC pins the adapter the Edge driver wakes this PC through (#96).
	// Empty means the service chooses; the network tab's dropdown edits it.
	WoLMAC string `json:"wol_mac"`
}

// TelegramConfig mirrors service.TelegramConfig plus the GET-only
// bot_token_set flag.
type TelegramConfig struct {
	Enabled bool `json:"enabled"`
	// BotToken arrives MASKED from GET ("****6789", or "" when unset). On
	// POST: "" or a "****"-prefixed value keeps the stored token, "-" clears
	// it, anything else replaces it (plaintext; the service encrypts).
	BotToken string `json:"bot_token"`
	// BotTokenSet reports whether a token is stored (GET only; the service
	// ignores it on POST).
	BotTokenSet    bool       `json:"bot_token_set"`
	ChatID         string     `json:"chat_id"`
	ControlEnabled bool       `json:"control_enabled"`
	AllowedChatIDs []string   `json:"allowed_chat_ids"`
	Detail         string     `json:"detail"` // "simple" | "full"
	Lang           string     `json:"lang"`   // "ko" | "en"
	PCName         string     `json:"pc_name"`
	QuietHours     QuietHours `json:"quiet_hours"`
}

// QuietHours mirrors notify.QuietHours (design doc §5).
type QuietHours struct {
	Enabled        bool   `json:"enabled"`
	Start          string `json:"start"` // "22:00", local time
	End            string `json:"end"`   // "07:00"; may cross midnight
	SecurityBypass bool   `json:"security_bypass"`
	Digest         bool   `json:"digest"`
}

// Client talks to the service's WebUI API on localhost.
type Client struct {
	// baseMu guards base, which SetPort moves while the polling goroutines
	// keep using the same *Client (#121).
	baseMu sync.RWMutex
	base   string
	http   *http.Client
}

func NewClient(webPort int) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: webBase(webPort),
		http: &http.Client{Jar: jar, Timeout: 5 * time.Second},
	}
}

func webBase(webPort int) string { return fmt.Sprintf("http://127.0.0.1:%d", webPort) }

// SetPort points the client at another WebUI port, for the service coming
// back on a new port after a restart (#121). Requests already in flight
// finish against the old one. The cookie jar is kept: cookies ignore the
// port, and a restarted service answers the stale session with a 401 like
// any restart.
func (c *Client) SetPort(webPort int) {
	c.baseMu.Lock()
	c.base = webBase(webPort)
	c.baseMu.Unlock()
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	c.baseMu.RLock()
	base := c.base
	c.baseMu.RUnlock()
	req, err := http.NewRequestWithContext(context.Background(), method, base+path, &buf)
	if err != nil {
		return nil, err
	}
	// The WebUI API requires this header for CSRF protection on POST.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// Login authenticates against /api/login and stores the session cookie.
func (c *Client) Login(secret string) error {
	resp, err := c.do("POST", "/api/login", map[string]string{"secret": secret})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return errLoginInvalid
	case http.StatusTooManyRequests:
		// service/webui.go locks an address out for 60 s after 5 failures.
		return errLoginLimited
	}
	var e struct {
		Message string `json:"message"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	if e.Message != "" {
		return fmt.Errorf("%s", e.Message)
	}
	return fmt.Errorf("login failed (HTTP %d)", resp.StatusCode)
}

var errUnauthorized = fmt.Errorf("unauthorized")

// Login outcomes the dialog words itself (#98): a wrong secret, and the
// service's rate-limit lockout.
var (
	errLoginInvalid = fmt.Errorf("invalid secret")
	errLoginLimited = fmt.Errorf("too many login attempts")
)

// GetConfig fetches the current service config. Returns errUnauthorized
// when a login is required first.
func (c *Client) GetConfig() (Config, error) {
	var cfg Config
	resp, err := c.do("GET", "/api/config", nil)
	if err != nil {
		return cfg, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return cfg, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return cfg, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return cfg, json.NewDecoder(resp.Body).Decode(&cfg)
}

// SaveConfig persists a new config via the service (hot-reloads secret).
func (c *Client) SaveConfig(cfg Config) (string, error) {
	resp, err := c.do("POST", "/api/config", cfg)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode != http.StatusOK || r.Status != "ok" {
		if r.Message != "" {
			return "", fmt.Errorf("%s", r.Message)
		}
		return "", fmt.Errorf("save failed (HTTP %d)", resp.StatusCode)
	}
	return r.Message, nil
}

// Logs returns the last log lines from the service.
func (c *Client) Logs() ([]string, error) {
	resp, err := c.do("GET", "/api/logs", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Lines []string `json:"lines"`
		Error string   `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return r.Lines, fmt.Errorf("%s", r.Error)
	}
	return r.Lines, nil
}

// WoLAdapter mirrors the service's per-adapter WoL info.
type WoLAdapter struct {
	Name       string   `json:"name"`
	MacAddress string   `json:"mac"`
	IPs        []string `json:"ips"`
	Status     string   `json:"status"`
	WoLEnabled bool     `json:"wolEnabled"`
	WoLCapable bool     `json:"wolCapable"`
}

// WoLStatus mirrors /api/wol-status.
type WoLStatus struct {
	Adapters   []WoLAdapter `json:"adapters"`
	ExternalIP string       `json:"externalIP"`
	Ready      bool         `json:"ready"`
	Warning    string       `json:"warning"`
	Error      string       `json:"error"`
}

// GetWoLStatus fetches network adapter / WoL information.
func (c *Client) GetWoLStatus() (WoLStatus, error) {
	var s WoLStatus
	resp, err := c.do("GET", "/api/wol-status", nil)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// STHub mirrors GET /api/st/hub (#67): the Edge driver's last contact with
// this service. Connected is false — and the hub fields empty — until a
// hub has polled recently; LastSeen is RFC3339. MachineID and SSDP (#95)
// describe this PC instead and are filled in even when no hub ever called.
type STHub struct {
	Connected     bool        `json:"connected"`
	IP            string      `json:"ip"`
	DriverVersion string      `json:"driver_version"`
	LastSeen      string      `json:"last_seen"`
	MachineID     string      `json:"machine_id"`
	SSDP          STSSDPState `json:"ssdp"`
	// WoL is the adapter choice (#96) the section's dropdown shows and
	// edits. An older service leaves it zero: no adapters, no selection.
	WoL STWoLInfo `json:"wol"`
}

// STWoLInfo is the WoL adapter picture: the adapter in force, the one the
// service's automatic rule would pick (the dropdown's first entry names
// it, even while a manual MAC overrides it) and the adapters to choose
// from. Selected and Auto are nil when this PC has no adapter with a MAC.
type STWoLInfo struct {
	Selected *STWoLSelected `json:"selected"`
	Auto     *STWoLSelected `json:"auto"`
	Adapters []STWoLAdapter `json:"adapters"`
}

// STWoLSelected is one chosen adapter; Source is "manual" or "auto".
type STWoLSelected struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	WoLEnabled bool   `json:"wol_enabled"`
	WoLCapable bool   `json:"wol_capable"`
	Source     string `json:"source"`
}

// STWoLAdapter is one row of the dropdown.
type STWoLAdapter struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	WoLEnabled bool   `json:"wol_enabled"`
	WoLCapable bool   `json:"wol_capable"`
	Selected   bool   `json:"selected"`
}

// STSSDPState is the SSDP responder's state: whether it holds a socket,
// whether the inbound UDP 1900 rule was found, and the last search that
// reached this PC (nil until one does).
type STSSDPState struct {
	Running      bool          `json:"running"`
	FirewallRule bool          `json:"firewall_rule"`
	LastSearch   *STLastSearch `json:"last_search"`
}

// STLastSearch is one M-SEARCH: the hub's address and an RFC3339 time.
type STLastSearch struct {
	IP string `json:"ip"`
	At string `json:"at"`
}

// GetSTHub fetches the SmartThings hub connection state shown by the
// network tab's SmartThings section (#70).
func (c *Client) GetSTHub() (STHub, error) {
	var h STHub
	resp, err := c.do("GET", "/api/st/hub", nil)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return h, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return h, json.NewDecoder(resp.Body).Decode(&h)
}

// RunningProcesses returns the unique .exe names running on this PC, for
// the watch-list picker (#110). The service answers loopback callers only;
// the list is shown in the picker dialog and kept nowhere.
func (c *Client) RunningProcesses() ([]string, error) {
	resp, err := c.do("GET", "/api/processes", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Processes []string `json:"processes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return r.Processes, nil
}

// Heartbeat is the body of POST /api/session/heartbeat. Both parts are
// optional; a nil one is left out.
type Heartbeat struct {
	// IdleSeconds is the interactive session's idle time (#77).
	IdleSeconds *int64 `json:"idle_seconds,omitempty"`
	// Audio is the default playback device's state (#104).
	Audio *HeartbeatAudio `json:"audio,omitempty"`
	// Media is the system media session (#117).
	Media *HeartbeatMedia `json:"media,omitempty"`
	// SessionID is the Windows session this app runs in. The service
	// stores the samples only from the session its commands act on and
	// ignores the others. Never 0 for a tray app; 0 (unknown) is left out.
	SessionID uint32 `json:"session_id,omitempty"`
}

// HeartbeatMedia is the heartbeat's media block: the status always, the
// track and the app only with the media.now_playing opt-in.
type HeartbeatMedia struct {
	Status    string `json:"status"`
	Title     string `json:"title,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Album     string `json:"album,omitempty"`
	App       string `json:"app,omitempty"`
	SampledAt string `json:"sampled_at"`
}

// HeartbeatAudio is the heartbeat's audio block. SampledAt (RFC3339) is
// when it was read: the service keeps whichever of this and a command
// result is newer by that time, not by arrival.
type HeartbeatAudio struct {
	Volume    int    `json:"volume"`
	Muted     bool   `json:"muted"`
	Device    string `json:"device"`
	SampledAt string `json:"sampled_at"`
}

// SessionHeartbeat reports what only this app can measure (#77, #104): the
// session's idle time, which the service publishes in the /st/v1/status
// session block for 90s (after that it reports null, so a missed post
// degrades to "unknown" rather than to a wrong number), and the volume the
// user may have changed with the keyboard.
//
// ignored is true when the service took the post but stored nothing
// because this app runs in another session than the one its commands act
// on ({"status":"ignored"}); an older service always answers "ok".
func (c *Client) SessionHeartbeat(body Heartbeat) (ignored bool, err error) {
	resp, err := c.do("POST", "/api/session/heartbeat", body)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return false, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Status string `json:"status"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&r)
	return r.Status == "ignored", nil
}

// Schedule mirrors /api/schedule GET.
type Schedule struct {
	Active       bool   `json:"active"`
	Command      string `json:"command"`
	RemainingSec int    `json:"remainingSec"`
	// Origin is "remote" for a SmartThings grace deferral, "ui" for a
	// schedule made from this app or the WebUI.
	Origin string `json:"origin"`
	// Replaced is the schedule this one displaced, when there was one.
	Replaced *ReplacedSchedule `json:"replaced"`
}

// ReplacedSchedule mirrors the "replaced" object of /api/schedule.
type ReplacedSchedule struct {
	Command string `json:"command"`
	Origin  string `json:"origin"`
}

// IsRemote reports whether the schedule came from a SmartThings command.
func (s Schedule) IsRemote() bool { return s.Origin == "remote" }

// GetSchedule returns the currently active scheduled command, if any.
func (c *Client) GetSchedule() (Schedule, error) {
	var s Schedule
	resp, err := c.do("GET", "/api/schedule", nil)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// SetSchedule schedules a command after the given number of minutes.
func (c *Client) SetSchedule(command string, minutes int) error {
	resp, err := c.do("POST", "/api/schedule", map[string]any{"command": command, "minutes": minutes})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode != http.StatusOK || r.Status != "ok" {
		if r.Message != "" {
			return fmt.Errorf("%s", r.Message)
		}
		return fmt.Errorf("schedule failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// CancelSchedule cancels the active schedule. by names the UI the user
// used ("app", "tray", "toast") so notifications can say where the cancel
// came from; the service falls back to "api" for anything else.
func (c *Client) CancelSchedule(by string) error {
	resp, err := c.do("DELETE", "/api/schedule?by="+by, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cancel failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Awake mirrors /api/awake (#111): the keep-awake state. Until is RFC3339,
// or "" while off and while on until turned off; RemainingSeconds is 0 then.
type Awake struct {
	On               bool   `json:"on"`
	Until            string `json:"until"`
	RemainingSeconds int    `json:"remaining_seconds"`
	DefaultMinutes   int    `json:"default_minutes"`
	KeepDisplay      bool   `json:"keep_display"`
}

// errAwakeUnsupported is what an older service answers /api/awake with
// (the WebUI mux sends unknown paths to the settings page, a 404 or 403).
var errAwakeUnsupported = fmt.Errorf("keep-awake needs service v1.2.0")

// awakeCall does one /api/awake request and decodes the state it returns.
func (c *Client) awakeCall(method string, body any) (Awake, error) {
	var a Awake
	resp, err := c.do(method, "/api/awake", body)
	if err != nil {
		return a, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return a, errUnauthorized
	case http.StatusNotFound, http.StatusForbidden:
		return a, errAwakeUnsupported
	default:
		var r apiStatus
		json.NewDecoder(resp.Body).Decode(&r)
		return a, r.err(resp, "keep-awake failed")
	}
	return a, json.NewDecoder(resp.Body).Decode(&a)
}

// GetAwake returns the keep-awake state.
func (c *Client) GetAwake() (Awake, error) { return c.awakeCall("GET", nil) }

// SetAwake keeps the PC awake for minutes (0 = until turned off).
func (c *Client) SetAwake(minutes int) (Awake, error) {
	return c.awakeCall("POST", map[string]int{"minutes": minutes})
}

// AwakeOff lets the PC sleep on its idle timer again.
func (c *Client) AwakeOff() (Awake, error) { return c.awakeCall("DELETE", nil) }

// Battery mirrors GET /api/battery (#112). Percent is -1 when Windows does
// not know it; Present is false on a desktop.
type Battery struct {
	Present  bool `json:"present"`
	Percent  int  `json:"percent"`
	Charging bool `json:"charging"`
	AC       bool `json:"ac"`
}

// GetBattery returns the service's newest battery reading. An older service
// (no such route) reads as no battery.
func (c *Client) GetBattery() (Battery, error) {
	var b Battery
	resp, err := c.do("GET", "/api/battery", nil)
	if err != nil {
		return b, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return b, json.NewDecoder(resp.Body).Decode(&b)
	case http.StatusUnauthorized:
		return b, errUnauthorized
	case http.StatusNotFound, http.StatusForbidden:
		return b, nil
	}
	return b, fmt.Errorf("HTTP %d", resp.StatusCode)
}

// RestartService asks the service to restart itself.
func (c *Client) RestartService() error {
	resp, err := c.do("POST", "/api/restart-service", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("restart failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// TestCommand runs a command right away through POST /api/command (#120);
// the service logs and notifies it as coming from the desktop app.
func (c *Client) TestCommand(name string) (string, error) {
	resp, err := c.do("POST", "/api/command", map[string]string{"command": name, "by": "app"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r apiStatus
	json.NewDecoder(resp.Body).Decode(&r)
	if err := r.err(resp, "command failed"); err != nil {
		return "", err
	}
	return r.Message, nil
}

// --- Telegram helper endpoints (#63) ---

// apiStatus is the {status, message} envelope every telegram endpoint
// returns; the ok replies carry extra fields decoded by the callers.
type apiStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// err turns a non-ok envelope into the error the GUI shows: the service's
// message when there is one, else the HTTP status.
func (a apiStatus) err(resp *http.Response, fallback string) error {
	if resp.StatusCode == http.StatusOK && a.Status == "ok" {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if a.Message != "" {
		return fmt.Errorf("%s", a.Message)
	}
	return fmt.Errorf("%s (HTTP %d)", fallback, resp.StatusCode)
}

// TestTelegram asks the service to send one test message. token and chatID
// are the form's unsaved values; "" or a masked token means "use the stored
// one" (the service resolves that, the GUI never sees the plaintext).
func (c *Client) TestTelegram(token, chatID string) error {
	resp, err := c.do("POST", "/api/telegram/test", map[string]string{"bot_token": token, "chat_id": chatID})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r apiStatus
	json.NewDecoder(resp.Body).Decode(&r)
	return r.err(resp, "test failed")
}

// TelegramMe returns the bot behind the STORED token (username without the
// "@", display name) via getMe — the connection-status line of the tab.
func (c *Client) TelegramMe() (username, name string, err error) {
	resp, err := c.do("GET", "/api/telegram/me", nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var r struct {
		apiStatus
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if err := r.err(resp, "getMe failed"); err != nil {
		return "", "", err
	}
	return r.Username, r.Name, nil
}

// TelegramState mirrors GET /api/telegram/state: the local state of
// inbound Telegram control, with no Bot API call behind it.
type TelegramState struct {
	// Polling is true while this PC runs the getUpdates loop.
	Polling bool `json:"polling"`
	// Conflict is true while getUpdates keeps answering 409 because
	// another PC shares this bot token (#75). Since is when that started.
	Conflict bool   `json:"conflict"`
	Since    string `json:"since"`
}

// TelegramState fetches that state for the notify tab's warning line.
func (c *Client) TelegramState() (TelegramState, error) {
	var s TelegramState
	resp, err := c.do("GET", "/api/telegram/state", nil)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return s, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// TelegramChat is one recent chat of the bot, from /api/telegram/chats.
type TelegramChat struct {
	ChatID   string `json:"chat_id"`
	Title    string `json:"title"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// TelegramChats lists the chats that recently wrote to the bot, so the user
// can pick a Chat ID instead of typing it. token is the form's unsaved
// value ("" or masked → stored token).
func (c *Client) TelegramChats(token string) ([]TelegramChat, error) {
	resp, err := c.do("POST", "/api/telegram/chats", map[string]string{"bot_token": token})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		apiStatus
		Chats []TelegramChat `json:"chats"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if err := r.err(resp, "chat lookup failed"); err != nil {
		return nil, err
	}
	return r.Chats, nil
}

// --- Media card (#117) ---

// MediaState mirrors /api/media: the two switches, whether anyone is
// logged in, and the audio and media blocks as /st/v1/status has them.
type MediaState struct {
	Enabled    bool       `json:"enabled"`
	NowPlaying bool       `json:"now_playing"`
	Session    bool       `json:"session"`
	Audio      MediaAudio `json:"audio"`
	Media      MediaInfo  `json:"media"`
}

// MediaAudio is the status audio block; the pointers are nil while
// Available is false.
type MediaAudio struct {
	Available bool    `json:"available"`
	Volume    *int    `json:"volume"`
	Muted     *bool   `json:"muted"`
	Device    *string `json:"device"`
}

// MediaInfo is the status media block. Status is playing, paused, stopped
// or none; the text fields are empty without the opt-in.
type MediaInfo struct {
	Status string `json:"status"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	App    string `json:"app"`
}

// errMediaUnsupported is what an older service answers /api/media with.
var errMediaUnsupported = fmt.Errorf("the media card needs service v1.2.0")

// mediaCommandError is a refused media command: Code is the service's
// error code (media_disabled, no_user_session, unsupported, failed,
// timeout, or a range message), Message its detail.
type mediaCommandError struct {
	Code    string
	Message string
}

func (e *mediaCommandError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// mediaCall does one /api/media request and decodes the state it returns.
func (c *Client) mediaCall(method string, body any) (MediaState, error) {
	var m MediaState
	resp, err := c.do(method, "/api/media", body)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return m, json.NewDecoder(resp.Body).Decode(&m)
	case http.StatusUnauthorized:
		return m, errUnauthorized
	case http.StatusNotFound:
		return m, errMediaUnsupported
	}
	var r struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	switch {
	case r.Error != "":
		return m, &mediaCommandError{Code: r.Error, Message: r.Message}
	case resp.StatusCode == http.StatusForbidden:
		// Not our JSON: the settings page an older service falls back to.
		return m, errMediaUnsupported
	case r.Message != "":
		return m, &mediaCommandError{Code: r.Message}
	}
	return m, fmt.Errorf("media: HTTP %d", resp.StatusCode)
}

// GetMedia returns the media card's state.
func (c *Client) GetMedia() (MediaState, error) { return c.mediaCall("GET", nil) }

// MediaCommand runs one volume, mute or media command (the /st/v1 names)
// and returns the state after it. value is nil for the commands without one.
func (c *Client) MediaCommand(command string, value *int) (MediaState, error) {
	body := map[string]any{"command": command}
	if value != nil {
		body["value"] = *value
	}
	return c.mediaCall("POST", body)
}
