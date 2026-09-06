package service

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
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

// Rate limiting for login attempts
type loginAttempt struct {
	failures  int
	lockedUntil time.Time
}

var (
	loginAttempts   = make(map[string]*loginAttempt)
	loginAttemptsMu sync.Mutex
)

const maxLoginFailures = 5
const loginLockDuration = 60 * time.Second

// checkRateLimit returns true if the request is allowed, false if rate limited.
func checkRateLimit(remoteAddr string) bool {
	// Extract IP without port
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}

	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	attempt, exists := loginAttempts[ip]
	if !exists {
		return true
	}
	if time.Now().Before(attempt.lockedUntil) {
		return false
	}
	// Lock expired, reset
	if attempt.failures >= maxLoginFailures {
		attempt.failures = 0
	}
	return true
}

// recordLoginFailure records a failed login attempt.
func recordLoginFailure(remoteAddr string) {
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}

	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	attempt, exists := loginAttempts[ip]
	if !exists {
		attempt = &loginAttempt{}
		loginAttempts[ip] = attempt
	}
	attempt.failures++
	if attempt.failures >= maxLoginFailures {
		attempt.lockedUntil = time.Now().Add(loginLockDuration)
		logMsg("Login rate limit triggered for %s (locked %v)", ip, loginLockDuration)
	}
}

// resetLoginAttempts clears failed attempts for an IP on successful login.
func resetLoginAttempts(remoteAddr string) {
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}

	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, ip)
}

// StartWebUI starts a local web UI for configuration on a separate port
func StartWebUI(stop chan struct{}) {
	cfg := getConfig()
	if cfg.Port == 0 {
		// Console mode starts this concurrently with StartHTTPServer —
		// don't rely on the other goroutine having loaded the config yet.
		cfg = loadConfig()
		setConfig(cfg)
	}
	webPort := cfg.Port + 1 // WebUI runs on port+1 (default: 5002)

	// Browser access is opt-in and only honored with a secret set — without
	// auth, anyone on the LAN could reconfigure and control this PC.
	// When disabled, the HTML pages are blocked (local browsers included;
	// the desktop app is the primary UI) but the JSON API stays available
	// on localhost, since the desktop app talks to the service through it.
	pagesEnabled := cfg.WebUIRemote && cfg.Secret != ""
	bindAddr := "127.0.0.1"
	if pagesEnabled {
		bindAddr = ""
		if err := addWebUIFirewallRule(webPort); err != nil {
			logMsg("WARNING: WebUI firewall rule failed (remote clients may be blocked): %v", err)
		}
	} else {
		if cfg.WebUIRemote && cfg.Secret == "" {
			logMsg("WARNING: webui_remote is enabled but no secret is set — browser WebUI stays disabled. Set a secret first.")
		}
		// Best-effort cleanup when browser access was turned off.
		removeWebUIFirewallRule()
	}

	mux := http.NewServeMux()

	// disabledPage is served for the HTML pages while browser access is off.
	serveDisabledPage := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>SmartThings PC Control</title>
<style>body{font-family:'Segoe UI',sans-serif;background:#0f172a;color:#94a3b8;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;text-align:center}div{max-width:480px;padding:24px}h2{color:#f8fafc}</style></head>
<body><div><h2>WebUI is disabled</h2>
<p>Use the desktop app (double-click the exe) to manage this PC.<br>
To use the browser WebUI, enable "Allow browser access" in the app settings and set a secret, then restart the service.</p>
<p>브라우저 WebUI가 비활성화되어 있습니다.<br>
관리는 데스크톱 앱(exe 더블클릭)을 사용하세요.<br>
브라우저 접속이 필요하면 앱 설정에서 "브라우저 접속 허용"을 켜고 시크릿을 설정한 뒤 서비스를 재시작하세요.</p></div></body></html>`)
	}

	// Login page
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if !pagesEnabled {
			serveDisabledPage(w)
			return
		}
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

		// Rate limiting
		if !checkRateLimit(r.RemoteAddr) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Too many attempts. Try again later."})
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
			recordLoginFailure(r.RemoteAddr)
			logMsg("WebUI login failed from %s", r.RemoteAddr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Invalid secret"})
			return
		}

		// Generate session token
		resetLoginAttempts(r.RemoteAddr)
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
		if !pagesEnabled {
			serveDisabledPage(w)
			return
		}
		liveCfg := getConfig()
		if !checkAuth(r, liveCfg.Secret) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		settingsTmpl.Execute(w, struct {
			Port          int
			Secret        string
			WebUIRemote   bool
			ShutdownGrace bool
			Version       string
		}{liveCfg.Port, liveCfg.Secret, liveCfg.WebUIRemote, liveCfg.ShutdownGrace, Version})
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
			if newCfg.WebUIRemote && newCfg.Secret == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Remote WebUI access requires a secret. Set a secret first."})
				return
			}
			oldCfg := liveCfg
			if err := saveConfig(newCfg); err != nil {
				http.Error(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
				return
			}
			logMsg("Config updated via WebUI: port=%d, secret=%s, webui_remote=%v", newCfg.Port, maskSecret(newCfg.Secret), newCfg.WebUIRemote)
			msg := "Settings saved."
			if oldCfg.Port != newCfg.Port || oldCfg.WebUIRemote != newCfg.WebUIRemote {
				msg = "Settings saved. Restart service to apply port/remote-access changes."
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": msg})
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
		Addr:    fmt.Sprintf("%s:%d", bindAddr, webPort),
		Handler: mux,
	}

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	if bindAddr == "" {
		logMsg("WebUI listening on http://0.0.0.0:%d (remote access enabled)", webPort)
	} else {
		logMsg("WebUI listening on http://127.0.0.1:%d", webPort)
	}
	server.ListenAndServe()
}
