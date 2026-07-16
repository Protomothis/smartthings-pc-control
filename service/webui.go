package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
	cfg := loadConfig()
	webPort := cfg.Port + 1 // WebUI runs on port+1 (default: 5002)

	mux := http.NewServeMux()

	// Login page
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Secret == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, loginHTML())
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

		if body.Secret != cfg.Secret {
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
		if !checkAuth(r, cfg.Secret) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, settingsHTML(cfg))
	})

	// API: Get config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, cfg.Secret) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg)
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
			if newCfg.Port == 0 {
				newCfg.Port = 5001
			}
			if err := saveConfig(newCfg); err != nil {
				http.Error(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
				return
			}
			cfg = newCfg
			logMsg("Config updated via WebUI: port=%d, secret=%s", cfg.Port, maskSecret(cfg.Secret))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Settings saved. Restart service to apply port changes."})
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// API: Test commands
	mux.HandleFunc("/api/test/", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, cfg.Secret) {
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
		if !checkAuth(r, cfg.Secret) {
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
			os.Exit(1)
		}()
	})

	// API: Log viewer (tail last 50 lines)
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, cfg.Secret) {
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

func loginHTML() string {
	return `<!DOCTYPE html>
<html lang="ko">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Remote Shutdown Service - Login</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #1a1a2e; color: #eee; min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px 16px; }
        .container { background: #16213e; border-radius: 12px; padding: 32px; width: 100%; max-width: 360px; box-shadow: 0 8px 32px rgba(0,0,0,0.3); }
        h1 { font-size: 1.4em; margin-bottom: 8px; color: #4fc3f7; }
        .subtitle { font-size: 0.85em; color: #888; margin-bottom: 24px; }
        .field { margin-bottom: 20px; }
        label { display: block; font-size: 0.85em; color: #aaa; margin-bottom: 6px; }
        input { width: 100%; padding: 12px 14px; border: 1px solid #333; border-radius: 6px; background: #0f3460; color: #fff; font-size: 1em; outline: none; }
        input:focus { border-color: #4fc3f7; }
        .btn { width: 100%; padding: 14px; border: none; border-radius: 6px; cursor: pointer; font-size: 1em; font-weight: 500; background: #4fc3f7; color: #000; }
        .btn:hover { background: #81d4fa; }
        .error { margin-top: 12px; padding: 10px; border-radius: 6px; background: #b71c1c; color: #ef9a9a; font-size: 0.85em; display: none; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔒 Login</h1>
        <p class="subtitle">Enter your secret key to access the WebUI</p>
        <div class="field">
            <label>Secret Key</label>
            <input type="password" id="secret" placeholder="Enter secret..." autofocus onkeydown="if(event.key==='Enter')login()">
        </div>
        <button class="btn" onclick="login()">Login</button>
        <div id="error" class="error"></div>
    </div>
    <script>
        async function login() {
            const secret = document.getElementById('secret').value;
            try {
                const res = await fetch('/api/login', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest'},
                    body: JSON.stringify({secret})
                });
                if (res.ok) {
                    window.location.href = '/';
                } else {
                    const data = await res.json();
                    document.getElementById('error').textContent = data.message || 'Login failed';
                    document.getElementById('error').style.display = 'block';
                }
            } catch(e) {
                document.getElementById('error').textContent = 'Connection error';
                document.getElementById('error').style.display = 'block';
            }
        }
    </script>
</body>
</html>`
}

func settingsHTML(cfg Config) string {
	return `<!DOCTYPE html>
<html lang="ko">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Remote Shutdown Service</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #1a1a2e; color: #eee; height: 100vh; overflow: hidden; padding: 16px; }
        .layout { display: grid; grid-template-columns: 1fr 1.5fr; gap: 16px; max-width: 1400px; margin: 0 auto; height: 100%; }
        .panel { background: #16213e; border-radius: 12px; padding: 24px; box-shadow: 0 8px 32px rgba(0,0,0,0.3); overflow: hidden; }
        .panel-left { display: flex; flex-direction: column; overflow-y: auto; }
        .panel-right { display: flex; flex-direction: column; }
        h1 { font-size: 1.3em; margin-bottom: 20px; color: #4fc3f7; }
        h2 { font-size: 1em; color: #4fc3f7; margin-bottom: 12px; }
        .field { margin-bottom: 16px; }
        label { display: block; font-size: 0.8em; color: #aaa; margin-bottom: 5px; text-transform: uppercase; letter-spacing: 0.5px; }
        input[type="number"], input[type="text"] { width: 100%; padding: 10px 12px; border: 1px solid #333; border-radius: 6px; background: #0f3460; color: #fff; font-size: 0.95em; outline: none; transition: border-color 0.2s; }
        input:focus { border-color: #4fc3f7; }
        .btn { padding: 10px 16px; border: none; border-radius: 6px; cursor: pointer; font-size: 0.85em; font-weight: 500; transition: all 0.2s; }
        .btn-primary { background: #4fc3f7; color: #000; }
        .btn-primary:hover { background: #81d4fa; }
        .btn-danger { background: #e53935; color: #fff; }
        .btn-danger:hover { background: #ef5350; }
        .btn-secondary { background: #333; color: #eee; }
        .btn-secondary:hover { background: #444; }
        .actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 16px; }
        .status { margin-top: 12px; padding: 8px 10px; border-radius: 6px; font-size: 0.8em; display: none; }
        .status.success { display: block; background: #1b5e20; color: #a5d6a7; }
        .status.error { display: block; background: #b71c1c; color: #ef9a9a; }
        .section-title { font-size: 0.85em; color: #888; margin-top: 20px; margin-bottom: 10px; border-top: 1px solid #333; padding-top: 12px; }
        .test-btns { display: flex; flex-wrap: wrap; gap: 6px; }
        .test-btns .btn { font-size: 0.8em; padding: 8px 12px; }
        .info { font-size: 0.72em; color: #666; margin-top: 3px; }
        .log-header { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
        .log-viewer { background: #0a0a1a; border: 1px solid #333; border-radius: 6px; padding: 12px; font-family: 'Consolas', 'Courier New', monospace; font-size: 0.73em; line-height: 1.6; flex: 1; overflow-y: auto; color: #ccc; white-space: pre-wrap; word-break: break-all; min-height: 0; }
        @media (max-width: 768px) {
            body { height: auto; overflow: auto; padding: 12px; }
            .layout { grid-template-columns: 1fr; height: auto; }
            .panel-right { max-height: 400px; }
            .log-viewer { max-height: 300px; }
        }
        @media (max-width: 480px) {
            body { padding: 8px; }
            .panel { padding: 20px 16px; }
            h1 { font-size: 1.1em; }
            .btn { padding: 10px 14px; }
            .test-btns .btn { flex: 1 1 calc(50% - 3px); text-align: center; }
            .actions { flex-direction: column; }
            .actions .btn { width: 100%; }
            .panel-right { max-height: 350px; }
            .log-viewer { max-height: 250px; font-size: 0.68em; }
        }
    </style>
</head>
<body>
    <div class="layout">
        <div class="panel panel-left">
            <h1>⚡ Remote Shutdown Service</h1>
            <div class="field">
                <label>Port</label>
                <input type="number" id="port" value="` + strconv.Itoa(cfg.Port) + `">
                <div class="info">SmartThings Edge driver default: 5001</div>
            </div>
            <div class="field">
                <label>Secret Key</label>
                <input type="text" id="secret" value="` + cfg.Secret + `" placeholder="(none)">
                <div class="info">Leave empty to disable authentication</div>
            </div>
            <div class="actions">
                <button class="btn btn-primary" onclick="saveConfig()">Save Settings</button>
                <button class="btn btn-secondary" onclick="restartService()">Restart Service</button>
            </div>
            <div id="status" class="status"></div>

            <div class="section-title">Test Commands</div>
            <div class="test-btns">
                <button class="btn btn-secondary" onclick="testCmd('lock')">Lock</button>
                <button class="btn btn-secondary" onclick="testCmd('turnscreenoff')">Screen Off</button>
                <button class="btn btn-secondary" onclick="testCmd('suspend')">Suspend</button>
                <button class="btn btn-secondary" onclick="testCmd('hibernate')">Hibernate</button>
                <button class="btn btn-secondary" onclick="testCmd('restart')">Restart</button>
                <button class="btn btn-danger" onclick="testCmd('shutdown')">Shutdown</button>
                <button class="btn btn-danger" onclick="testCmd('forceshutdown')">Force Shutdown</button>
            </div>
        </div>
        <div class="panel panel-right">
            <h2>📋 Logs</h2>
            <div class="log-header">
                <button class="btn btn-secondary" onclick="loadLogs()" style="font-size:0.8em;padding:6px 12px;">Refresh</button>
                <label style="display:flex;align-items:center;gap:4px;font-size:0.8em;color:#888;text-transform:none;letter-spacing:0;"><input type="checkbox" id="autoRefresh" onchange="toggleAutoRefresh()"> Auto (5s)</label>
            </div>
            <div id="logViewer" class="log-viewer">Loading...</div>
        </div>
    </div>
    <script>
        const headers = {'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest'};
        async function saveConfig() {
            const port = parseInt(document.getElementById('port').value);
            const secret = document.getElementById('secret').value;
            try {
                const res = await fetch('/api/config', {method: 'POST', headers, body: JSON.stringify({port, secret})});
                if (res.status === 401) { window.location.href = '/login'; return; }
                const data = await res.json();
                showStatus(data.message, 'success');
            } catch(e) {
                showStatus('Failed to save settings', 'error');
            }
        }
        async function testCmd(cmd) {
            if (cmd === 'shutdown' || cmd === 'restart' || cmd === 'forceshutdown') {
                if (!confirm('Are you sure?')) return;
            }
            try {
                const res = await fetch('/api/test/' + cmd, {headers: {'X-Requested-With': 'XMLHttpRequest'}});
                if (res.status === 401) { window.location.href = '/login'; return; }
                const data = await res.json();
                showStatus(cmd + ' command sent', 'success');
            } catch(e) {
                showStatus('Failed to send command', 'error');
            }
        }
        function showStatus(msg, type) {
            const el = document.getElementById('status');
            el.textContent = msg;
            el.className = 'status ' + type;
            setTimeout(() => { el.className = 'status'; }, 3000);
        }
        async function restartService() {
            if (!confirm('Restart the service? Connection will be lost briefly.')) return;
            try {
                await fetch('/api/restart-service', {method: 'POST', headers});
                showStatus('Service restarting...', 'success');
                setTimeout(() => location.reload(), 3000);
            } catch(e) {
                showStatus('Restart triggered (page will reload)', 'success');
                setTimeout(() => location.reload(), 3000);
            }
        }
        let autoRefreshTimer = null;
        function toggleAutoRefresh() {
            if (document.getElementById('autoRefresh').checked) {
                loadLogs();
                autoRefreshTimer = setInterval(loadLogs, 5000);
            } else {
                if (autoRefreshTimer) { clearInterval(autoRefreshTimer); autoRefreshTimer = null; }
            }
        }
        function convertLogTime(line) {
            const match = line.match(/^(\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}) (.*)$/);
            if (!match) return line;
            const serverTime = match[1].replace(/\//g, '-').replace(' ', 'T');
            const d = new Date(serverTime);
            if (isNaN(d.getTime())) return line;
            const local = d.toLocaleString(undefined, {month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false});
            return local + ' ' + match[2];
        }
        async function loadLogs() {
            try {
                const res = await fetch('/api/logs', {headers: {'X-Requested-With': 'XMLHttpRequest'}});
                if (res.status === 401) { window.location.href = '/login'; return; }
                const data = await res.json();
                const viewer = document.getElementById('logViewer');
                if (data.lines && data.lines.length > 0) {
                    viewer.textContent = data.lines.map(convertLogTime).join('\n');
                    viewer.scrollTop = viewer.scrollHeight;
                } else {
                    viewer.textContent = '(no logs)';
                }
            } catch(e) {
                document.getElementById('logViewer').textContent = 'Failed to load logs';
            }
        }
        loadLogs();
    </script>
</body>
</html>`
}
