package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// StartWebUI starts a local web UI for configuration on a separate port
func StartWebUI(stop chan struct{}) {
	cfg := loadConfig()
	webPort := cfg.Port + 1 // WebUI runs on port+1 (default: 5002)

	mux := http.NewServeMux()

	// Serve the settings page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, settingsHTML(cfg))
	})

	// API: Get config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg)
			return
		}
		if r.Method == "POST" {
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

func settingsHTML(cfg Config) string {
	return `<!DOCTYPE html>
<html lang="ko">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Remote Shutdown Service - Settings</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #1a1a2e; color: #eee; min-height: 100vh; display: flex; align-items: center; justify-content: center; }
        .container { background: #16213e; border-radius: 12px; padding: 32px; width: 400px; box-shadow: 0 8px 32px rgba(0,0,0,0.3); }
        h1 { font-size: 1.4em; margin-bottom: 24px; color: #4fc3f7; }
        .field { margin-bottom: 20px; }
        label { display: block; font-size: 0.85em; color: #aaa; margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px; }
        input { width: 100%; padding: 10px 14px; border: 1px solid #333; border-radius: 6px; background: #0f3460; color: #fff; font-size: 1em; outline: none; transition: border-color 0.2s; }
        input:focus { border-color: #4fc3f7; }
        .btn { padding: 10px 20px; border: none; border-radius: 6px; cursor: pointer; font-size: 0.9em; font-weight: 500; transition: all 0.2s; }
        .btn-primary { background: #4fc3f7; color: #000; }
        .btn-primary:hover { background: #81d4fa; }
        .btn-danger { background: #e53935; color: #fff; }
        .btn-danger:hover { background: #ef5350; }
        .btn-secondary { background: #333; color: #eee; }
        .btn-secondary:hover { background: #444; }
        .actions { display: flex; gap: 8px; margin-top: 24px; }
        .status { margin-top: 16px; padding: 10px; border-radius: 6px; font-size: 0.85em; display: none; }
        .status.success { display: block; background: #1b5e20; color: #a5d6a7; }
        .status.error { display: block; background: #b71c1c; color: #ef9a9a; }
        .section-title { font-size: 0.9em; color: #888; margin-top: 28px; margin-bottom: 12px; border-top: 1px solid #333; padding-top: 16px; }
        .test-btns { display: flex; flex-wrap: wrap; gap: 6px; }
        .test-btns .btn { font-size: 0.8em; padding: 6px 12px; }
        .info { font-size: 0.75em; color: #666; margin-top: 4px; }
    </style>
</head>
<body>
    <div class="container">
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
    <script>
        async function saveConfig() {
            const port = parseInt(document.getElementById('port').value);
            const secret = document.getElementById('secret').value;
            try {
                const res = await fetch('/api/config', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({port, secret})
                });
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
                const res = await fetch('/api/test/' + cmd);
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
    </script>
</body>
</html>`
}
