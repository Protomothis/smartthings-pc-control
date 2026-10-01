package service

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/systool"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
)

// httpReadHeaderTimeout is how long the command and WebUI servers wait
// for a client to finish sending its request headers.
const httpReadHeaderTimeout = 10 * time.Second

// initLogger opens service.log next to the exe (internal/logx); a second
// call while it is open does nothing.
func initLogger() { logx.Init(installDir()) }

// closeLogger closes it at service stop.
func closeLogger() { logx.Close() }

// logMsg writes one line to service.log.
func logMsg(format string, args ...any) { logx.Printf(format, args...) }

// newCommandHandler returns the HTTP handler for processing SmartThings commands.
func newCommandHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		liveCfg := getConfig()
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		var command string

		from := httpx.RemoteHost(r.RemoteAddr)

		if liveCfg.Secret != "" {
			// With secret: /{secret}/{command}. A wrong secret counts like
			// a failed WebUI login (#120): five in a row lock the address
			// out for a minute, so the secret cannot be guessed at line rate.
			if !logins.Allow(httpx.RemoteHost(r.RemoteAddr)) {
				logMsg("Request: %s /***/... from %s (RATE LIMITED)", r.Method, r.RemoteAddr)
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockDuration/time.Second)))
				http.Error(w, "Too many attempts", http.StatusTooManyRequests)
				return
			}
			if len(parts) < 2 || !secret.Equal(parts[0], liveCfg.Secret) {
				logins.Fail(httpx.RemoteHost(r.RemoteAddr))
				logMsg("Request: %s /***/%s from %s (UNAUTHORIZED)", r.Method, strings.Join(parts[1:], "/"), r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				// count/window are filled by the aggregation stage (#58).
				emit("security", "unauthorized", map[string]string{
					"from": from,
					"path": httpx.Truncate("/***/"+strings.Join(parts[1:], "/"), 64),
				})
				return
			}
			logins.Reset(httpx.RemoteHost(r.RemoteAddr))
			command = parts[1]
		} else {
			// No secret: /{command}
			if len(parts) < 1 {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			command = parts[0]
		}

		logMsg("Request: %s /%s from %s", r.Method, command, r.RemoteAddr)

		name := strings.ToLower(command)
		cmd, ok := Commands[name]
		if !ok {
			http.Error(w, "Unknown command: "+command, http.StatusBadRequest)
			emit("security", "unknown_command", map[string]string{"from": from, "command": httpx.Truncate(command, 64)})
			return
		}

		// The reply is the same whether the command runs now or waits out
		// the grace period (the user can cancel from the tray app), and it
		// goes out first, as it always has for the Edge driver.
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, cmd.Response)
		dispatchCommand(name, from, originRemote, dispatchDefault)
	}
}

// StartHTTPServer starts the HTTP server compatible with SmartThings Edge driver
func StartHTTPServer(stop chan struct{}) {
	// Initialize if not already done (e.g., console mode)
	initLogger()
	cfg := getConfig()
	if cfg.Port == 0 {
		cfg = loadConfig()
		setConfig(cfg)
	}
	logMsg("Service starting on port %d", cfg.Port)

	if cfg.Secret == "" {
		logMsg("WARNING: No secret configured. Anyone on your network can control this PC.")
	}

	mux := http.NewServeMux()
	// The /st/v1 tree (edge-driver doc §3) shares the command port; a more
	// specific pattern wins over "/", so the legacy /{secret}/{command}
	// handler still sees everything else.
	registerSTRoutes(mux)
	mux.HandleFunc("/", newCommandHandler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
		// Bounds only the request headers (gosec G112, Slowloris): a LAN
		// client that opens a connection and never finishes its headers no
		// longer holds it forever. Bodies and responses are not limited.
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	log.Printf("Listening on port %d", cfg.Port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Printf("HTTP server error: %v", err)
	}
}

// executeCommand runs the System32 tool with args for the catalogue
// command `command` (shutdown, restart, ...) and logs the outcome; a
// failure also raises system.exec_failed naming that command.
func executeCommand(command string, tool string, args ...string) {
	notePowerCommand(command) // hint for power.stopping's reason (§3.5)
	output, err := sys.tool(tool, args...)
	if err != nil {
		logMsg("exec [%s %v] error: %v - output: %s", tool, args, err, string(output))
		reportExecFailure(command, err, output)
	} else {
		logMsg("exec [%s %v] ok", tool, args)
	}
}

// runSystemTool runs a System32 tool by absolute path (internal/systool) and
// returns its combined output (sys.tool).
func runSystemTool(tool string, args ...string) ([]byte, error) {
	return systool.Command(tool, args...).CombinedOutput()
}

// reportExecFailure emits system.exec_failed. The first line of output is
// appended when it is readable text (shutdown.exe writes in the console
// code page, which may not be UTF-8).
func reportExecFailure(command string, err error, output []byte) {
	msg := err.Error()
	if line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n"); line != "" && utf8.ValidString(line) {
		msg += ": " + httpx.Truncate(strings.TrimSpace(line), 200)
	}
	emit("system", "exec_failed", map[string]string{"command": command, "error": msg})
}
