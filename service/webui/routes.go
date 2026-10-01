package webui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/power"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

//go:embed web/login.html
var loginPage string

//go:embed web/settings.html
var settingsPage string

var settingsTmpl = template.Must(template.New("settings").Parse(settingsPage))

// Routes registers the pages and the JSON API, without the Host check
// (Handler adds it). Without pagesEnabled the HTML pages answer with the
// "disabled" notice; the API stays, the desktop app talks to the service
// through it.
func (s *Server) Routes(pagesEnabled bool) *http.ServeMux {
	mux := http.NewServeMux()

	// Login page
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if !pagesEnabled {
			serveDisabledPage(w)
			return
		}
		if s.d.Config().Secret == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, loginPage)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// Login API
	mux.HandleFunc("/api/login", s.handleLogin)

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
		liveCfg := s.d.Config()
		if !s.checkAuth(r, liveCfg.Secret) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		settingsTmpl.Execute(w, newSettingsView(liveCfg, s.d.Version()))
	})

	// API: the browser page's status card (#122); WebUI only.
	mux.HandleFunc("/api/status", s.apiAuth(s.serveStatus, http.MethodGet))

	// API: Get/update config (token masking rules: design doc §10)
	mux.HandleFunc("/api/config", s.apiAuth(s.serveConfig, http.MethodGet, http.MethodPost))

	// API: SmartThings hub connection state for the GUI (#67, shown by #70)
	mux.HandleFunc("/api/st/hub", s.apiAuth(s.serveSTHub, http.MethodGet))

	// API: a session for this app's own tray without the secret, for
	// trusted loopback callers only (#131, see locallogin.go)
	mux.HandleFunc("/api/local-login", s.handleLocalLogin)

	// API: idle-time heartbeat from the tray app (#77, see heartbeat.go)
	mux.HandleFunc("/api/session/heartbeat", s.apiAuth(s.serveHeartbeat, http.MethodPost))

	// API: keep-awake toggle on the app's command tab (#111)
	mux.HandleFunc("/api/awake", s.apiAuth(s.serveAwake, http.MethodGet, http.MethodPost, http.MethodDelete))

	// API: battery for the app's status bar (#112)
	mux.HandleFunc("/api/battery", s.apiAuth(s.serveBattery, http.MethodGet))

	// API: the command tab's media card (#117)
	mux.HandleFunc("/api/media", s.apiAuth(s.serveMedia, http.MethodGet, http.MethodPost))

	// API: running program names for the app's watch-list picker (#110);
	// loopback callers only.
	mux.HandleFunc("/api/processes", s.apiAuth(s.serveProcesses, http.MethodGet))

	// API: the app's [테스트 알림] (#106) and the preset [실행]/[테스트]
	// buttons (#109)
	mux.HandleFunc("/api/notify/test", s.apiAuth(s.serveNotifyTest, "POST"))
	mux.HandleFunc("/api/presets/run", s.apiAuth(s.servePresetsRun, "POST"))
	mux.HandleFunc("/api/presets/test", s.apiAuth(s.servePresetsTest, "POST"))

	// API: Telegram helpers for the GUI notify tab (design doc §11, #63)
	mux.HandleFunc("/api/telegram/test", s.apiAuth(s.serveTelegramTest, "POST"))
	mux.HandleFunc("/api/telegram/me", s.apiAuth(s.serveTelegramMe, "GET"))
	mux.HandleFunc("/api/telegram/chats", s.apiAuth(s.serveTelegramChats, "GET", "POST"))
	mux.HandleFunc("/api/telegram/state", s.apiAuth(s.serveTelegramState, "GET"))

	// API: run a command now — the app's command tab, tray menu and toast
	// [Run now], and the settings page's buttons (#120). The old GET
	// /api/test/{cmd} is gone: any page the browser loaded could use it to
	// run forceshutdown.
	mux.HandleFunc("/api/command", s.apiAuth(s.serveCommand, "POST"))

	// API: Restart service (exit with code 1 to trigger Recovery Action)
	mux.HandleFunc("/api/restart-service", s.apiAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Service restarting..."})
		logx.Printf("Service restart requested via WebUI")
		go func() {
			time.Sleep(500 * time.Millisecond)
			// Use sc.exe to cleanly restart the service (avoids Recovery Action side effects)
			s.d.Restart()
		}()
	}, http.MethodPost))

	// API: Log viewer (tail last 100 lines)
	mux.HandleFunc("/api/logs", s.apiAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		logPath := logx.Path()
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
	}))

	// API: WoL status
	mux.HandleFunc("/api/wol-status", s.apiAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.d.Status.WoLScan())
	}))

	// API: Schedule command
	mux.HandleFunc("/api/schedule", s.apiAuth(s.serveSchedule, http.MethodGet, http.MethodPost, http.MethodDelete))

	return mux
}

// serveDisabledPage is served for the HTML pages while browser access is off.
func serveDisabledPage(w http.ResponseWriter) {
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

// handleLogin serves POST /api/login: the browser page's secret login,
// behind the failed-login lockout it shares with the legacy URL.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Rate limiting
	from := httpx.RemoteHost(r.RemoteAddr)
	if !s.d.Logins.Allow(from) {
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

	if !secret.Equal(body.Secret, s.d.Config().Secret) {
		s.d.Logins.Fail(from)
		logx.Printf("WebUI login failed from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Invalid secret"})
		return
	}

	// Generate session token
	s.d.Logins.Reset(from)
	token := generateSessionToken()
	s.SetSessionToken(token)

	setSessionCookie(w, token)

	logx.Printf("WebUI login successful from %s", r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---- /api/schedule -----------------------------------------------------------

func (s *Server) serveSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(s.d.Status.Schedule())
		return
	}
	if r.Method == http.MethodPost {
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
		if body.Minutes < 1 || body.Minutes > power.MaxScheduleMinutes {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"status": "error",
				"message": fmt.Sprintf("Minutes must be between 1 and %d", power.MaxScheduleMinutes)})
			return
		}
		if err := s.d.Commands.Schedule(body.Command, time.Duration(body.Minutes)*time.Minute); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
			return
		}
		msg := fmt.Sprintf("%s scheduled in %d minutes", body.Command, body.Minutes)
		if rep, ok := s.d.Status.Schedule()["replaced"].(*power.Replaced); ok && rep != nil {
			msg += fmt.Sprintf(" (replaced %s schedule: %s)", rep.Origin, rep.Command)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": msg})
		return
	}
	// DELETE. ?by=app|tray|toast|webui|smartthings says which UI the
	// user cancelled from; it only affects the notification wording
	// (default "api").
	by := "api"
	switch v := r.URL.Query().Get("by"); v {
	case "app", "tray", "toast", "webui", "smartthings":
		by = v
	}
	if s.d.Commands.CancelSchedule(by) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Schedule cancelled"})
	} else {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "No active schedule"})
	}
}

// ---- /api/command ------------------------------------------------------------

// commandCallers are the UIs POST /api/command may name in "by". Anything
// else counts as the desktop app.
var commandCallers = map[string]bool{"app": true, "tray": true, "toast": true, "webui": true}

// serveCommand serves POST /api/command {"command": "lock", "by": "app"}
// for the UIs on this PC (#120): auth, POST only and the CSRF header, like
// every other state-changing endpoint. The command runs at once — the user
// is at the PC, so there is no grace period — but it is logged, recorded
// as last_command (origin "ui") and notified like a remote command, so a
// command nobody at the PC asked for still shows up. An unknown command is
// a 404.
func (s *Server) serveCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
		By      string `json:"by"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	name := strings.ToLower(strings.TrimSpace(body.Command))
	by := body.By
	if !commandCallers[by] {
		by = "app"
	}
	// Recorded as last_command (origin "ui"); notified only from the
	// WebUI.
	if !s.d.Commands.RunNow(name, by) {
		httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"status": "error", "command": httpx.Truncate(name, 64), "message": "Unknown command"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "command": name, "message": "Command sent"})
}

// ---- /api/config -------------------------------------------------------------

// configView is what GET /api/config returns: the live Config with the
// bot token replaced by its masked form plus bot_token_set. The outer
// Telegram field shadows the embedded one for encoding/json.
type configView struct {
	config.Config
	Telegram telegramConfigView `json:"telegram"`
}

type telegramConfigView struct {
	config.TelegramConfig
	// BotTokenSet tells the GUI a token exists even though bot_token only
	// carries "****1234".
	BotTokenSet bool `json:"bot_token_set"`
}

// maskedConfig builds the GET view on a copy; the live config is untouched.
func maskedConfig(cfg config.Config) configView {
	tg := telegramConfigView{TelegramConfig: cfg.Telegram, BotTokenSet: cfg.Telegram.BotToken != ""}
	tg.BotToken = ""
	if cfg.Telegram.BotToken != "" {
		// Mask the decrypted value so the GUI sees a stable "****" + last 4
		// no matter how the token is stored. A token this machine cannot
		// decrypt (Load blanks those, so this is defensive) masks fully.
		plain, err := secret.Unprotect(cfg.Telegram.BotToken)
		if err != nil || plain == "" {
			tg.BotToken = config.MaskedTokenPrefix
		} else {
			tg.BotToken = secret.Mask(plain)
		}
	}
	return configView{Config: cfg, Telegram: tg}
}

// serveConfig serves GET/POST /api/config.
func (s *Server) serveConfig(w http.ResponseWriter, r *http.Request) {
	liveCfg := s.d.Config()
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, maskedConfig(liveCfg))
		return
	}
	// POST. Decode over the live config: keys the client omits (an older
	// GUI sends no telegram/notify at all) keep their current values.
	newCfg := liveCfg.ForUpdate()
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if msg := config.ValidatePort(newCfg.Port); msg != "" {
		writeAPIError(w, http.StatusBadRequest, msg)
		return
	}
	if newCfg.WebUIRemote && newCfg.Secret == "" {
		writeAPIError(w, http.StatusBadRequest, "Remote WebUI access requires a secret. Set a secret first.")
		return
	}
	// Normalize applies the token rules: ""/masked keep, "-" clears.
	newCfg = config.Normalize(newCfg, liveCfg)
	if msg := config.ValidateGraceSeconds(newCfg.GraceSeconds); msg != "" {
		writeAPIError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := config.ValidateActivity(newCfg.Activity); msg != "" {
		writeAPIError(w, http.StatusBadRequest, msg)
		return
	}
	// #109: a preset that could never run is rejected here rather
	// than on use.
	if msg := config.ValidatePresets(newCfg.Presets); msg != "" {
		writeAPIError(w, http.StatusBadRequest, msg)
		return
	}
	oldCfg := liveCfg
	if err := s.d.SaveConfig(newCfg); err != nil {
		http.Error(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	logx.Printf("Config updated via WebUI: port=%d, secret=%s, webui_remote=%v, shutdown_grace=%v, grace_seconds=%d, telegram=%v",
		newCfg.Port, logx.MaskSecret(newCfg.Secret), newCfg.WebUIRemote, newCfg.ShutdownGrace, newCfg.GraceSeconds, newCfg.Telegram.Enabled)
	// newCfg still holds a replaced token in plaintext while oldCfg holds
	// the stored (protected) one, so a real change always differs and a
	// kept token compares equal — ChangedKeys never sees values.
	if keys := config.ChangedKeys(oldCfg, newCfg); len(keys) > 0 {
		// The app and the browser share this endpoint; neither can be told apart.
		s.d.Emit("security", "config_changed", map[string]string{"keys": strings.Join(keys, ", "), "by": "api"})
	}
	msg := "Settings saved."
	if oldCfg.Port != newCfg.Port || oldCfg.WebUIRemote != newCfg.WebUIRemote {
		msg = "Settings saved. Restart service to apply port/remote-access changes."
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": msg})
}
