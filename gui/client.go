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
type Config struct {
	Port          int    `json:"port"`
	Secret        string `json:"secret"`
	WebUIRemote   bool   `json:"webui_remote"`
	ShutdownGrace bool   `json:"shutdown_grace"`
	GraceSeconds  int    `json:"grace_seconds"`
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

// Schedule mirrors /api/schedule GET.
type Schedule struct {
	Active       bool   `json:"active"`
	Command      string `json:"command"`
	RemainingSec int    `json:"remainingSec"`
}

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

// CancelSchedule cancels the active schedule.
func (c *Client) CancelSchedule() error {
	resp, err := c.do("DELETE", "/api/schedule", nil)
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
