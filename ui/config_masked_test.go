package ui

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// maskingAPI is a service whose local session lapsed: until LocalLogin it
// serves the config with the preset paths masked, as GET /api/config does
// for a stale cookie with no secret set (a 200, not a 401).
type maskingAPI struct {
	configAPI
	lmu      sync.Mutex
	loggedIn bool
	loginErr error
	logins   int
}

func (a *maskingAPI) LocalLogin() error {
	a.lmu.Lock()
	defer a.lmu.Unlock()
	a.logins++
	if a.loginErr != nil {
		return a.loginErr
	}
	a.loggedIn = true
	return nil
}

func (a *maskingAPI) GetConfig() (gui.Config, error) {
	cfg, _ := a.configAPI.GetConfig()
	a.lmu.Lock()
	defer a.lmu.Unlock()
	if !a.loggedIn {
		for i := range cfg.Presets {
			cfg.Presets[i].Path, cfg.Presets[i].Args = "", nil
		}
		cfg.PresetsMasked = true
	}
	return cfg, nil
}

func maskedSetup(t *testing.T, loginErr error) (*UI, *maskingAPI) {
	t.Helper()
	api := &maskingAPI{loginErr: loginErr}
	api.cfg = baseConfig()
	api.cfg.Presets = []gui.Preset{{Slot: 1, Name: "메모장", Type: "program", Path: `C:\Windows\notepad.exe`}}
	u := sampleUI(gui.LangKo, false)
	u.api = api
	u.trust.Store(int32(trustLocal)) // it logged in once; the service restarted since
	return u, api
}

// The 10s config poll after a service restart the app did not notice: it
// logs in again and the presets keep their paths.
func TestReadConfigRelogsInWhenMasked(t *testing.T) {
	u, api := maskedSetup(t, nil)
	u.loadConfig()
	s := u.current()
	if len(s.cfg.Presets) != 1 || s.cfg.Presets[0].Path != `C:\Windows\notepad.exe` || s.cfg.PresetsMasked {
		t.Fatalf("presets after the poll: %+v masked=%v", s.cfg.Presets, s.cfg.PresetsMasked)
	}
	if api.logins != 1 || trustState(u.trust.Load()) != trustLocal || u.editorsLocked() {
		t.Errorf("logins=%d trust=%d locked=%v", api.logins, u.trust.Load(), u.editorsLocked())
	}
	// Once the paths come back, the next poll does not log in again.
	u.loadConfig()
	if api.logins != 1 {
		t.Errorf("an unmasked config logged in again: %d", api.logins)
	}
}

// The login fails (the service is busy restarting): the editors lock, with
// their own note, rather than offering presets with empty paths to save.
func TestReadConfigLocksWhenStillMasked(t *testing.T) {
	u, _ := maskedSetup(t, errors.New("connection refused"))
	u.loadConfig()
	if !u.editorsLocked() {
		t.Fatal("still masked, the editors are not locked")
	}
	if got := u.lockedNote(); got != u.t("localonly.relogin") {
		t.Errorf("note = %q", got)
	}
	if w := gui.WireConfig(u.current().cfg, u.editorsLocked()); w.Presets != nil || w.PresetsMasked {
		t.Errorf("a save would send presets %+v masked=%v", w.Presets, w.PresetsMasked)
	}
}

// A refusal for another reason than not_admin says which (#168), so the
// administrator note no longer stands for every failed check.
func TestRefusalNoteNamesTheReason(t *testing.T) {
	for _, tc := range []struct {
		code string
		want func(u *UI) string
	}{
		{"not_admin", func(u *UI) string { return u.t("localonly.note") }},
		{"other_exe", func(u *UI) string { return fmt.Sprintf(u.t("localonly.untrusted"), "other_exe") }},
		{"http_404", func(u *UI) string { return fmt.Sprintf(u.t("localonly.untrusted"), "http_404") }},
	} {
		u, _ := maskedSetup(t, nil)
		u.trust.Store(int32(trustUnknown))
		u.api = &maskingAPI{configAPI: configAPI{cfg: baseConfig()}, loginErr: realRefusal(t, tc.code)}
		u.relogin()
		if !u.editorsLocked() {
			t.Fatalf("%s: editors not locked", tc.code)
		}
		if got, want := u.lockedNote(), tc.want(u); got != want {
			t.Errorf("%s: note %q, want %q", tc.code, got, want)
		}
		if strings.Contains(u.lockedNote(), "%!") {
			t.Errorf("%s: note %q has a format error", tc.code, u.lockedNote())
		}
	}
}

// realRefusal is the error the real client returns for a refusal with
// code ("http_404": a reply without one), from a service stand-in.
func realRefusal(t *testing.T, code string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := strings.CutPrefix(code, "http_"); ok {
			n, _ := strconv.Atoi(c)
			w.WriteHeader(n)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"status":"error","code":%q}`, code)
	}))
	defer srv.Close()
	err := gui.NewClient(srv.Listener.Addr().(*net.TCPAddr).Port).LocalLogin()
	if !gui.IsLocalLoginRefused(err) {
		t.Fatalf("%s: %v is not a refusal", code, err)
	}
	return err
}

// A standard user (refused) keeps the usual note and is not asked again.
func TestReadConfigRefusedKeepsNote(t *testing.T) {
	u, api := maskedSetup(t, nil)
	u.trust.Store(int32(trustRefused))
	u.loadConfig()
	if api.logins != 0 || !u.editorsLocked() || u.lockedNote() != u.t("localonly.note") {
		t.Errorf("logins=%d locked=%v note=%q", api.logins, u.editorsLocked(), u.lockedNote())
	}
}
