// Package httpx holds the few request and response helpers every HTTP
// surface of the service shares (/st/v1, the WebUI and /api, the legacy
// command path).
package httpx

import (
	"encoding/json"
	"net"
	"net/http"
	"unicode/utf8"
)

// RemoteHost strips the port from an http.Request.RemoteAddr for the
// "from" field of notifications.
func RemoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// Truncate shortens attacker-controlled strings (paths, command names) to
// max runes before they go into a notification or a log line.
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// WriteJSON encodes v with the JSON content type and the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
