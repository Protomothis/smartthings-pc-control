package service

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
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
	failures    int
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
		emit("security", "login_limited", map[string]string{"from": ip})
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
			// SmartThings is shown as plain form fields (#70); the desktop
			// app's network tab is the designed UI for these.
			SmartThings SmartThingsConfig
			AllowedHubs string
			// telegram.pc_name is the one Telegram setting this page edits
			// (#75); the token and the chat id belong to the app's
			// notifications tab, and are deliberately not handed to the
			// template. Hostname is the entry's placeholder: what an empty
			// pc_name falls back to.
			PCName   string
			Hostname string
		}{liveCfg.Port, liveCfg.Secret, liveCfg.WebUIRemote, liveCfg.ShutdownGrace, Version,
			liveCfg.SmartThings, strings.Join(liveCfg.SmartThings.AllowedHubs, ", "),
			liveCfg.Telegram.PCName, hostname()})
	})

	// API: Get/update config (token masking rules: design doc §10)
	mux.HandleFunc("/api/config", handleConfigAPI)

	// API: SmartThings hub connection state for the GUI (#67, shown by #70)
	mux.HandleFunc("/api/st/hub", handleSTHubAPI)

	// API: idle-time heartbeat from the tray app (#77, see st_idle.go)
	mux.HandleFunc("/api/session/heartbeat", handleSessionHeartbeat)

	// API: Telegram helpers for the GUI notify tab (design doc §11, #63)
	mux.HandleFunc("/api/telegram/test", handleTelegramTest)
	mux.HandleFunc("/api/telegram/me", handleTelegramMe)
	mux.HandleFunc("/api/telegram/chats", handleTelegramChats)
	mux.HandleFunc("/api/telegram/state", handleTelegramState)

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
			// #89: the same ceiling as /st/v1, the Telegram bot and the app.
			if body.Minutes < 1 || body.Minutes > maxScheduleMinutes {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error",
					"message": fmt.Sprintf("Minutes must be between 1 and %d", maxScheduleMinutes)})
				return
			}
			if err := setSchedule(body.Command, time.Duration(body.Minutes)*time.Minute, originUI); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
				return
			}
			msg := fmt.Sprintf("%s scheduled in %d minutes", body.Command, body.Minutes)
			if rep, ok := getSchedule()["replaced"].(*replacedSchedule); ok && rep != nil {
				msg += fmt.Sprintf(" (replaced %s schedule: %s)", rep.Origin, rep.Command)
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": msg})
			return
		}
		if r.Method == "DELETE" {
			if !checkCSRF(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			// ?by=app|tray|toast|webui|smartthings says which UI the user
			// cancelled from; it only affects the notification wording
			// (default "api").
			by := "api"
			switch v := r.URL.Query().Get("by"); v {
			case "app", "tray", "toast", "webui", "smartthings":
				by = v
			}
			if cancelScheduleBy(by) {
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

// writeJSON encodes v with the JSON content type and the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeAPIError is the {status:"error", message} shape the GUI expects.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"status": "error", "message": msg})
}

// configView is what GET /api/config returns: the live Config with the
// bot token replaced by its masked form plus bot_token_set. The outer
// Telegram field shadows the embedded one for encoding/json.
type configView struct {
	Config
	Telegram telegramConfigView `json:"telegram"`
}

type telegramConfigView struct {
	TelegramConfig
	// BotTokenSet tells the GUI a token exists even though bot_token only
	// carries "****1234".
	BotTokenSet bool `json:"bot_token_set"`
}

// maskedConfig builds the GET view on a copy; the live config is untouched.
func maskedConfig(cfg Config) configView {
	tg := telegramConfigView{TelegramConfig: cfg.Telegram, BotTokenSet: cfg.Telegram.BotToken != ""}
	tg.BotToken = ""
	if cfg.Telegram.BotToken != "" {
		// Mask the decrypted value so the GUI sees a stable "****" + last 4
		// no matter how the token is stored. A token this machine cannot
		// decrypt (loadConfig blanks those, so this is defensive) masks fully.
		plain, err := liveBotToken(cfg.Telegram)
		if err != nil || plain == "" {
			tg.BotToken = maskedTokenPrefix
		} else {
			tg.BotToken = secret.Mask(plain)
		}
	}
	return configView{Config: cfg, Telegram: tg}
}

// handleConfigAPI serves GET/POST /api/config.
func handleConfigAPI(w http.ResponseWriter, r *http.Request) {
	liveCfg := getConfig()
	if !checkAuth(r, liveCfg.Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == "GET" {
		writeJSON(w, http.StatusOK, maskedConfig(liveCfg))
		return
	}
	if r.Method == "POST" {
		if !checkCSRF(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		// Decode over the live config: keys the client omits (an older
		// GUI sends no telegram/notify at all) keep their current values.
		newCfg := liveCfg.forUpdate()
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if msg := validatePort(newCfg.Port); msg != "" {
			writeAPIError(w, http.StatusBadRequest, msg)
			return
		}
		if newCfg.WebUIRemote && newCfg.Secret == "" {
			writeAPIError(w, http.StatusBadRequest, "Remote WebUI access requires a secret. Set a secret first.")
			return
		}
		// normalizeConfig applies the token rules: ""/masked keep, "-" clears.
		newCfg = normalizeConfig(newCfg, liveCfg)
		if msg := validateGraceSeconds(newCfg.GraceSeconds); msg != "" {
			writeAPIError(w, http.StatusBadRequest, msg)
			return
		}
		oldCfg := liveCfg
		if err := saveConfig(newCfg); err != nil {
			http.Error(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		logMsg("Config updated via WebUI: port=%d, secret=%s, webui_remote=%v, shutdown_grace=%v, grace_seconds=%d, telegram=%v",
			newCfg.Port, maskSecret(newCfg.Secret), newCfg.WebUIRemote, newCfg.ShutdownGrace, newCfg.GraceSeconds, newCfg.Telegram.Enabled)
		// newCfg still holds a replaced token in plaintext while oldCfg holds
		// the stored (protected) one, so a real change always differs and a
		// kept token compares equal — configChangedKeys never sees values.
		if keys := configChangedKeys(oldCfg, newCfg); len(keys) > 0 {
			// The app and the browser share this endpoint; neither can be told apart.
			emit("security", "config_changed", map[string]string{"keys": strings.Join(keys, ", "), "by": "api"})
		}
		msg := "Settings saved."
		if oldCfg.Port != newCfg.Port || oldCfg.WebUIRemote != newCfg.WebUIRemote {
			msg = "Settings saved. Restart service to apply port/remote-access changes."
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": msg})
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// ---- SmartThings hub state (#67) -------------------------------------------

// stHubView is GET /api/st/hub: what the GUI SmartThings section (#70)
// shows about the Edge driver's last contact. "connected" means the hub
// polled within stHubStale (2× the longest poll interval the driver
// offers). machine_id and ssdp were added for the search diagnostics
// (#95): they describe this PC, not the hub, so they are filled in even
// when no hub has ever called — that is exactly the case the user needs
// them in.
type stHubView struct {
	Connected     bool       `json:"connected"`
	IP            string     `json:"ip"`
	DriverVersion string     `json:"driver_version"`
	LastSeen      string     `json:"last_seen"`
	MachineID     string     `json:"machine_id"`
	SSDP          stSSDPView `json:"ssdp"`
	// WoL is the adapter choice (#96) the section's dropdown edits. It
	// rides on this poll rather than on a second endpoint so the whole
	// section refreshes in one round trip.
	WoL stHubWoLView `json:"wol"`
}

// stHubWoLView is what the app needs to draw the WoL adapter dropdown:
// the adapter in force, the one the automatic rule would choose (the
// first dropdown entry names it even while a manual MAC overrides it) and
// the list to choose from. Selected and Auto are null when this PC has no
// adapter with a MAC.
type stHubWoLView struct {
	Selected *stWoLSelected `json:"selected"`
	Auto     *stWoLSelected `json:"auto"`
	Adapters []stWoLAdapter `json:"adapters"`
}

// stSSDPView is the responder's state: whether it holds a socket, whether
// the inbound UDP 1900 rule was found, and the last M-SEARCH this PC
// matched (null until one arrives).
type stSSDPView struct {
	Running      bool          `json:"running"`
	FirewallRule bool          `json:"firewall_rule"`
	LastSearch   *stSearchView `json:"last_search"`
}

type stSearchView struct {
	IP string `json:"ip"`
	At string `json:"at"`
}

// stSSDPStatus assembles the responder block.
func stSSDPStatus() stSSDPView {
	out := stSSDPView{Running: ssdpRunning(), FirewallRule: ssdpFirewallRuleOK()}
	if s, ok := lastSSDPSearch(); ok {
		out.LastSearch = &stSearchView{IP: s.IP, At: s.At.Format(time.RFC3339)}
	}
	return out
}

// handleSTHubAPI serves GET /api/st/hub.
func handleSTHubAPI(w http.ResponseWriter, r *http.Request) {
	liveCfg := getConfig()
	if !checkAuth(r, liveCfg.Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wol, auto := stWoLView(liveCfg.SmartThings)
	view := stHubView{
		MachineID: machineID(),
		SSDP:      stSSDPStatus(),
		WoL:       stHubWoLView{Selected: wol.Selected, Auto: auto, Adapters: wol.Adapters},
	}
	if seen, ok := hubLastSeenInfo(); ok {
		view.Connected = time.Since(seen.At) <= stHubStale
		view.IP = seen.IP
		view.DriverVersion = seen.DriverVersion
		view.LastSeen = seen.At.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, view)
}

// ---- Telegram helper endpoints (#63) ---------------------------------------

// telegramOverride is the optional POST body that lets the GUI try values
// it has not saved yet. bot_token may be plaintext here (it travels in the
// body over localhost / the authenticated session, never in a URL).
type telegramOverride struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// decodeTelegramOverride reads an optional JSON body; an empty body or a
// non-POST request yields the zero value.
func decodeTelegramOverride(r *http.Request) (telegramOverride, error) {
	var o telegramOverride
	if r.Method != "POST" || r.Body == nil {
		return o, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return o, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return o, nil
	}
	err = json.Unmarshal(raw, &o)
	return o, err
}

// telegramTarget resolves the plaintext token and chat id to use for one
// helper call: the override when it carries a value, else the live config
// (decrypted). A masked placeholder in the override means "use the live
// token" so the GUI can send its form as-is.
func telegramTarget(tg TelegramConfig, o telegramOverride) (token, chatID string, err error) {
	token = o.BotToken
	if token == "" || strings.HasPrefix(token, maskedTokenPrefix) || token == clearTokenSentinel {
		token, err = liveBotToken(tg)
		if err != nil {
			return "", "", err
		}
	}
	chatID = o.ChatID
	if chatID == "" {
		chatID = tg.ChatID
	}
	return token, chatID, nil
}

// telegramCallStatus maps a Bot API failure to an HTTP status for the GUI:
// 429 when Telegram rate-limited us, 502 for anything else it (or the
// network) reported.
func telegramCallStatus(err error) int {
	if _, ok := telegram.RetryAfterOf(err); ok {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

// telegramErrorMessage extracts the Bot API description when there is one
// so the GUI shows "Unauthorized" / "chat not found" rather than the whole
// wrapped chain.
func telegramErrorMessage(err error) string {
	var api *telegram.APIError
	if errors.As(err, &api) {
		return fmt.Sprintf("Telegram API error %d: %s", api.Code, api.Description)
	}
	var rl *telegram.RateLimitError
	if errors.As(err, &rl) {
		return fmt.Sprintf("Telegram rate limited, retry after %s", rl.RetryAfter)
	}
	return err.Error()
}

// authTelegramRequest runs the shared checks; it reports false after
// writing the response when the request must not proceed.
func authTelegramRequest(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	liveCfg := getConfig()
	if !checkAuth(r, liveCfg.Secret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !slices.Contains(methods, r.Method) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !checkCSRF(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// handleTelegramTest serves POST /api/telegram/test: one system.test event
// straight through a telegram.Sink (bypassing the bus so quiet hours or a
// mute cannot swallow it). Body {bot_token, chat_id} is optional and lets
// the GUI test unsaved values; otherwise the live config is used. The
// telegram.enabled flag is deliberately ignored here — testing is how the
// user decides whether to enable it.
func handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "POST") {
		return
	}
	o, err := decodeTelegramOverride(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	tg := getConfig().Telegram
	token, chatID, err := telegramTarget(tg, o)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bot token cannot be decrypted on this machine; enter it again.")
		return
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: bot token is missing.")
		return
	}
	if chatID == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: chat id is missing.")
		return
	}
	rd, err := telegram.NewRenderer(tg.Lang, tg.Detail)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sink := telegram.NewSink(newTelegramClient(token), chatID, rd, telegramPCName(tg))
	ctx, cancel := context.WithTimeout(r.Context(), telegramAPITimeout)
	defer cancel()
	ev := notify.Event{Category: "system", Kind: "test", At: time.Now(), Fields: map[string]string{}}
	if err := sink.Send(ctx, ev); err != nil {
		logMsg("Telegram test message failed: %v", err)
		writeAPIError(w, telegramCallStatus(err), telegramErrorMessage(err))
		return
	}
	logMsg("Telegram test message sent to chat %s", chatID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "sent"})
}

// handleTelegramMe serves GET /api/telegram/me with the live token:
// {status:"ok", username, name}. The token never comes from the URL.
func handleTelegramMe(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "GET") {
		return
	}
	token, err := liveBotToken(getConfig().Telegram)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bot token cannot be decrypted on this machine; enter it again.")
		return
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: bot token is missing.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramAPITimeout)
	defer cancel()
	u, err := newTelegramClient(token).GetMe(ctx)
	if err != nil {
		writeAPIError(w, telegramCallStatus(err), telegramErrorMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"username": u.Username,
		"name":     strings.TrimSpace(u.FirstName + " " + u.LastName),
	})
}

// handleTelegramState serves GET /api/telegram/state: the local state of
// inbound Telegram control, with no Bot API call of its own.
//
//	{status:"ok", polling:bool, conflict:bool, since:"RFC3339"}
//
// conflict is true while getUpdates keeps answering 409 because another PC
// shares this bot token (#75); since is when that started and is omitted
// otherwise. The GUI notify tab shows a warning for it.
func handleTelegramState(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "GET") {
		return
	}
	conflict, since := telegramConflictState()
	out := map[string]any{
		"status":   "ok",
		"polling":  telegramControlRunning(),
		"conflict": conflict,
	}
	if conflict && !since.IsZero() {
		out["since"] = since.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// telegramChat is one entry of /api/telegram/chats.
type telegramChat struct {
	ChatID   string `json:"chat_id"`
	Title    string `json:"title"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// handleTelegramChats serves GET (live token) and POST (optional body
// {bot_token}) /api/telegram/chats: getUpdates(offset 0, timeout 0) reduced
// to the distinct chats that wrote to the bot, in first-seen order:
// {status:"ok", chats:[{chat_id, title, username, type}]}.
func handleTelegramChats(w http.ResponseWriter, r *http.Request) {
	if !authTelegramRequest(w, r, "GET", "POST") {
		return
	}
	o, err := decodeTelegramOverride(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	token, _, err := telegramTarget(getConfig().Telegram, o)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bot token cannot be decrypted on this machine; enter it again.")
		return
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "Telegram is not configured: bot token is missing.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramAPITimeout)
	defer cancel()
	updates, err := newTelegramClient(token).GetUpdates(ctx, 0, 0)
	if err != nil {
		writeAPIError(w, telegramCallStatus(err), telegramErrorMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "chats": distinctChats(updates)})
}

// distinctChats collects each chat once (message and callback_query
// updates alike), keeping first-seen order. Never nil, so the JSON is [].
func distinctChats(updates []telegram.Update) []telegramChat {
	out := []telegramChat{}
	seen := map[int64]bool{}
	add := func(c telegram.Chat) {
		if c.ID == 0 || seen[c.ID] {
			return
		}
		seen[c.ID] = true
		title := c.Title
		if title == "" {
			title = strings.TrimSpace(c.FirstName + " " + c.LastName)
		}
		out = append(out, telegramChat{ChatID: c.IDString(), Title: title, Username: c.Username, Type: c.Type})
	}
	for _, u := range updates {
		if u.Message != nil {
			add(u.Message.Chat)
		}
		if u.CallbackQuery != nil && u.CallbackQuery.Message != nil {
			add(u.CallbackQuery.Message.Chat)
		}
	}
	return out
}
