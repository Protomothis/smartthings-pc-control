package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
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
}

// SmartThingsConfig mirrors service.SmartThingsConfig. The widgets that
// edit it live in the network tab (#70); this struct only keeps the values
// alive across a GET/POST round trip.
type SmartThingsConfig struct {
	Discovery         bool     `json:"discovery"`
	AllowedHubs       []string `json:"allowed_hubs"`
	ExposeSession     bool     `json:"expose_session"`
	ExposeSessionUser bool     `json:"expose_session_user"`
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
	base string
	http *http.Client
}

func NewClient(webPort int) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: fmt.Sprintf("http://127.0.0.1:%d", webPort),
		http: &http.Client{Jar: jar, Timeout: 5 * time.Second},
	}
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, c.base+path, &buf)
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
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Message != "" {
			return fmt.Errorf("%s", e.Message)
		}
		return fmt.Errorf("login failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

var errUnauthorized = fmt.Errorf("unauthorized")

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
// this service. Connected is false — and the other fields empty — until a
// hub has polled recently; LastSeen is RFC3339.
type STHub struct {
	Connected     bool   `json:"connected"`
	IP            string `json:"ip"`
	DriverVersion string `json:"driver_version"`
	LastSeen      string `json:"last_seen"`
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

// SessionHeartbeat reports the interactive session's idle time to the
// service (#77). Only this app can measure it, and the service publishes
// the newest sample in the /st/v1/status session block for 90s; after that
// it reports null, so a missed post degrades to "unknown" rather than to a
// wrong number.
func (c *Client) SessionHeartbeat(idleSeconds int64) error {
	resp, err := c.do("POST", "/api/session/heartbeat", map[string]int64{"idle_seconds": idleSeconds})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
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

// TestCommand triggers a command through /api/test/{name}.
func (c *Client) TestCommand(name string) (string, error) {
	resp, err := c.do("GET", "/api/test/"+name, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Status != "ok" {
		return "", fmt.Errorf("%s", r.Message)
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
