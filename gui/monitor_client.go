package gui

// API calls behind the command tab's monitor card (#144): /api/monitor.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// MonitorConfig mirrors service.MonitorConfig: the settings tab's opt-in.
type MonitorConfig struct {
	Enabled bool `json:"enabled"`
}

// MonitorInfo is one entry of the status "monitors" block.
type MonitorInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Primary    bool     `json:"primary"`
	DDC        bool     `json:"ddc"`
	Brightness *int     `json:"brightness"`
	Input      *string  `json:"input"`
	Inputs     []string `json:"inputs"`
	Power      string   `json:"power"`
}

// MonitorState mirrors /api/monitor.
type MonitorState struct {
	Enabled  bool          `json:"enabled"`
	Monitors []MonitorInfo `json:"monitors"`
}

// Primary is the monitor the commands act on: the primary, else the first.
func (s MonitorState) Primary() (MonitorInfo, bool) {
	for _, m := range s.Monitors {
		if m.Primary {
			return m, true
		}
	}
	if len(s.Monitors) > 0 {
		return s.Monitors[0], true
	}
	return MonitorInfo{}, false
}

// errMonitorUnsupported is what a service without /api/monitor answers.
var errMonitorUnsupported = fmt.Errorf("the monitor card needs service v1.3.0")

// monitorCall does one /api/monitor request and decodes the state.
func (c *Client) monitorCall(method string, body any) (MonitorState, error) {
	var m MonitorState
	resp, err := c.do(method, "/api/monitor", body)
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
		return m, errMonitorUnsupported
	}
	var r struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || r.Error == "" {
		// Not our JSON: the settings page an older service falls back to.
		return m, errMonitorUnsupported
	}
	return m, &actionError{Code: r.Error, Message: r.Message}
}

// GetMonitors returns the monitor card's state.
func (c *Client) GetMonitors() (MonitorState, error) { return c.monitorCall("GET", nil) }

// MonitorCommand runs monitor_brightness (an int), monitor_input or
// monitor_power (strings) and returns the state after it.
func (c *Client) MonitorCommand(command string, value any) (MonitorState, error) {
	return c.monitorCall("POST", map[string]any{"command": command, "value": value})
}
