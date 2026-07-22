package service

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed web/login.html
var loginPage string

//go:embed web/settings.html
var settingsPage string

var settingsTmpl = template.Must(template.New("settings").Parse(settingsPage))

// Version is set by main package at startup
var Version = "dev"

var (
	sessionToken string
	sessionMu    sync.RWMutex
)

func generateSessionToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// checkAuth validates session cookie. Returns true if authenticated.
func checkAuth(r *http.Request, secret string) bool {
	if secret == "" {
		return true // No auth required
	}
	cookie, err := r.Cookie("session")
	if err != nil {
		return false
	}
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return cookie.Value == sessionToken && sessionToken != ""
}

// checkCSRF validates CSRF protection for POST requests.
func checkCSRF(r *http.Request) bool {
	if r.Method != "POST" {
		return true
	}
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// StartWebUI starts a local web UI for configuration on a separate port
func StartWebUI(stop chan struct{}) {
	cfg := getConfig()
	webPort := cfg.Port + 1 // WebUI runs on port+1 (default: 5002)

	mux := http.NewServeMux()

	// Login page
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if liveCfg.Secret == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, loginPage)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// Login API
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		var body struct {
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		liveCfg := getConfig()
		if body.Secret != liveCfg.Secret {
			logMsg("WebUI login failed from %s", r.RemoteAddr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Invalid secret"})
			return
		}

		// Generate session token
		sessionMu.Lock()
		sessionToken = generateSessionToken()
		sessionMu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    sessionToken,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})

		logMsg("WebUI login successful from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Serve the settings page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		settingsTmpl.Execute(w, struct {
			Port    int
			Secret  string
			Version string
		}{liveCfg.Port, liveCfg.Secret, Version})
	})

	// API: Get config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(liveCfg)
			return
		}
		if r.Method == "POST" {
			if !checkCSRF(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			var newCfg Config
			if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}
			if msg := validatePort(newCfg.Port); msg != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": msg})
				return
			}
			if err := saveConfig(newCfg); err != nil {
				http.Error(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
				return
			}
			logMsg("Config updated via WebUI: port=%d, secret=%s", newCfg.Port, maskSecret(newCfg.Secret))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Settings saved. Restart service to apply port changes."})
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// API: Test commands
	mux.HandleFunc("/api/test/", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		command := r.URL.Path[len("/api/test/"):]
		w.Header().Set("Content-Type", "application/json")

		cmd, ok := Commands[command]
		if !ok {
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "command": command, "message": "Unknown command"})
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "command": command, "message": "Command sent"})
		if cmd.Execute != nil {
			go cmd.Execute()
		}
	})

	// API: Restart service (exit with code 1 to trigger Recovery Action)
	mux.HandleFunc("/api/restart-service", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !checkCSRF(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Service restarting..."})
		logMsg("Service restart requested via WebUI")
		go func() {
			time.Sleep(500 * time.Millisecond)
			// Use sc.exe to cleanly restart the service (avoids Recovery Action side effects)
			restartSelf()
		}()
	})

	// API: Log viewer (tail last 100 lines)
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if logPath == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{"lines": []string{}, "error": "log path unknown"})
			return
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"lines": []string{}, "error": err.Error()})
			return
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		// Return last 100 lines max
		maxLines := 100
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"lines": lines})
	})

	// API: WoL status
	mux.HandleFunc("/api/wol-status", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		status := getWoLStatus()
		json.NewEncoder(w).Encode(status)
	})

	// API: Schedule command
	mux.HandleFunc("/api/schedule", func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if r.Method == "GET" {
			json.NewEncoder(w).Encode(getSchedule())
			return
		}
		if r.Method == "POST" {
			if !checkCSRF(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			var body struct {
				Command string `json:"command"`
				Minutes int    `json:"minutes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Invalid JSON"})
				return
			}
			if body.Minutes < 1 || body.Minutes > 1440 {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Minutes must be between 1 and 1440"})
				return
			}
			if err := setSchedule(body.Command, body.Minutes); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": fmt.Sprintf("%s scheduled in %d minutes", body.Command, body.Minutes)})
			return
		}
		if r.Method == "DELETE" {
			if !checkCSRF(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			if cancelSchedule() {
				json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Schedule cancelled"})
			} else {
				json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "No active schedule"})
			}
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", webPort),
		Handler: mux,
	}

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	logMsg("WebUI listening on http://127.0.0.1:%d", webPort)
	server.ListenAndServe()
}
