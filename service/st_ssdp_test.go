package service

// Tests for SSDP discovery and GET /st/v1/description (edge-driver doc
// §3.6, #69). Nothing here joins a multicast group: the responder's socket
// factory is replaced with a loopback pair, so the tests run on a build
// agent with no LAN.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/stapi"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// ---- M-SEARCH parsing ------------------------------------------------------

// ---- response formatting ---------------------------------------------------

// ---- per-source rate limit -------------------------------------------------

// ---- GET /st/v1/description ------------------------------------------------

func TestSTDescriptionNeedsNoSecret(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "topsecret"})
	// Earlier tests authenticated as a hub; start from a clean slate so
	// the "an anonymous probe is not a hub" check below means something.
	prevHub, _ := stSrv.HubLastSeen()
	stSrv.SetHubLastSeen(status.HubSeen{})
	t.Cleanup(func() { stSrv.SetHubLastSeen(prevHub) })

	// No X-PC-Secret at all: an unconfigured driver has none yet (§3.6).
	r := httptest.NewRequest(http.MethodGet, "/st/v1/description", nil)
	r.RemoteAddr = "192.168.1.20:51234"
	w := httptest.NewRecorder()
	stSrv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := stJSON(t, w)
	if got["protocol"] != float64(stapi.Protocol) {
		t.Errorf("protocol = %v", got["protocol"])
	}
	if got["machine_id"] != machineID() || got["hostname"] != hostname() {
		t.Errorf("identity = %v / %v", got["machine_id"], got["hostname"])
	}
	if got["service_version"] != Version {
		t.Errorf("service_version = %v, want %q", got["service_version"], Version)
	}
	if got["port"] != float64(5001) {
		t.Errorf("port = %v", got["port"])
	}
	if got["secret_set"] != true {
		t.Errorf("secret_set = %v, want true", got["secret_set"])
	}
	// The secret itself must never appear in a public document.
	if strings.Contains(w.Body.String(), "topsecret") {
		t.Fatal("the description leaks the secret")
	}
	// An anonymous probe is not a hub: it must not make the GUI claim one
	// is connected.
	if _, ok := stSrv.HubLastSeen(); ok {
		t.Error("the description recorded a hub contact")
	}
}

func TestSTDescriptionWithoutSecretConfigured(t *testing.T) {
	stSetup(t, Config{Port: 5002})
	w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.20", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	got := stJSON(t, w)
	if got["secret_set"] != false {
		t.Errorf("secret_set = %v, want false", got["secret_set"])
	}
	if got["port"] != float64(5002) {
		t.Errorf("port = %v, want the live port", got["port"])
	}
}

func TestSTDescriptionIgnoresAllowedHubs(t *testing.T) {
	// A hub allow-list guards the control routes; discovery has to work
	// before the user knows which IP to list.
	stSetup(t, Config{Port: 5001, Secret: "s", SmartThings: SmartThingsConfig{AllowedHubs: []string{"192.168.1.20"}}})
	if w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.99", "", ""); w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
}

func TestSTDescriptionMethodAndRateLimit(t *testing.T) {
	stSetup(t, Config{Port: 5001})
	if w := stDo(t, http.MethodPost, "/st/v1/description", "192.168.1.20", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d", w.Code)
	}

	stSrv.ResetRateLimit()
	for i := 0; i < stapi.RatePerSecond; i++ {
		if w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.30", "", ""); w.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, w.Code)
		}
	}
	if w := stDo(t, http.MethodGet, "/st/v1/description", "192.168.1.30", "", ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("burst past the limit: status %d, want 429", w.Code)
	}
}

// ---- reconcile lifecycle ---------------------------------------------------

// ---- config hot reload (§3.7) ----------------------------------------------

func TestConfigAPIRoundTripsSmartThings(t *testing.T) {
	protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001, SmartThings: SmartThingsConfig{
		AllowedHubs: []string{"192.168.1.20"}, ExposeSession: true,
	}})

	// GET hands the GUI every §3.7 key.
	w := httptest.NewRecorder()
	webAPI(w, httptest.NewRequest("GET", "/api/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	st, ok := decodeBody(t, w)["smartthings"].(map[string]any)
	if !ok {
		t.Fatalf("no smartthings object: %s", w.Body.String())
	}
	if st["expose_session"] != true || st["expose_session_user"] != false {
		t.Errorf("smartthings = %v", st)
	}
	// #95: the retired key is not offered to a client any more.
	if _, ok := st["discovery"]; ok {
		t.Errorf("GET still exposes smartthings.discovery: %v", st)
	}
	hubs, _ := st["allowed_hubs"].([]any)
	if len(hubs) != 1 || hubs[0] != "192.168.1.20" {
		t.Errorf("allowed_hubs = %v", st["allowed_hubs"])
	}

	// POST writes them back and the live config follows without a restart.
	// The body still carries the retired discovery key, the way an older
	// WebUI page would send it: it must be ignored, not rejected (#95).
	w = httptest.NewRecorder()
	webAPI(w, postJSON("/api/config", `{"port":5001,"smartthings":{"discovery":false,"allowed_hubs":["10.0.0.7"],"expose_session":false,"expose_session_user":true}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", w.Code, w.Body.String())
	}
	got := getConfig().SmartThings
	if got.ExposeSession || !got.ExposeSessionUser {
		t.Errorf("live config = %+v", got)
	}
	if len(got.AllowedHubs) != 1 || got.AllowedHubs[0] != "10.0.0.7" {
		t.Errorf("allowed_hubs = %v", got.AllowedHubs)
	}

	// A request made right afterwards sees the saved values (no restart).
	stSrv.ResetRateLimit()
	d := stDo(t, http.MethodGet, "/st/v1/description", "10.0.0.7", "", "")
	if d.Code != http.StatusOK {
		t.Fatalf("description after save: status %d", d.Code)
	}
	if stJSON(t, d)["port"] != float64(5001) {
		t.Errorf("description port = %v", stJSON(t, d)["port"])
	}
}

// ---- last search bookkeeping (#95) -----------------------------------------
