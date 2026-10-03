package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"

	"golang.org/x/sys/windows"
)

func mySID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// setDACL gives path a protected DACL from SDDL.
func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// presetFiles makes a folder shaped like a user's own (SYSTEM,
// Administrators, this account) with two scripts: safe.ps1 as the folder
// gives it, open.ps1 with an extra Everyone write ACE.
func presetFiles(t *testing.T) (dir, safe, open string) {
	t.Helper()
	me := mySID(t)
	dir = filepath.Join(t.TempDir(), "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	own := "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + me + ")"
	setDACL(t, dir, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+me+")")
	safe, open = filepath.Join(dir, "safe.ps1"), filepath.Join(dir, "open.ps1")
	for _, p := range []string{safe, open} {
		if err := os.WriteFile(p, []byte("Write-Host hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		setDACL(t, p, own)
	}
	setDACL(t, open, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+me+")(A;;0x120116;;;WD)")
	return dir, safe, open
}

// localRequest is r from the desktop app: loopback and the local session.
func localRequest(s *testServer, r *http.Request) *http.Request {
	s.SetLocalSessionToken("local")
	r.RemoteAddr = "127.0.0.1:52431"
	r.AddCookie(&http.Cookie{Name: "session", Value: "local"})
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	return r
}

func TestPresetWarningsOnSaveAndTest(t *testing.T) {
	_, safe, open := presetFiles(t)
	s := newTestServer(t, config.Config{Port: 5001})
	me := mySID(t)
	s.presetUserSID = func() string { return me }

	presets := []config.Preset{
		{Slot: 1, Name: "safe", Type: "script", Path: safe},
		{Slot: 2, Name: "open", Type: "script", Path: open},
		{Slot: 3, Name: "web", Type: "url", Path: "https://example.com"},
	}
	body, _ := json.Marshal(map[string]any{"port": 5001, "presets": presets})
	w := httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("POST", "/api/config", strings.NewReader(string(body)))))
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var reply struct {
		Status   string          `json:"status"`
		Warnings []PresetWarning `json:"warnings"`
	}
	json.Unmarshal(w.Body.Bytes(), &reply)
	if reply.Status != "ok" || len(reply.Warnings) != 1 ||
		reply.Warnings[0] != (PresetWarning{Slot: 2, Code: "writable_by_others", Path: open}) {
		t.Errorf("save reply = %s", w.Body.String())
	}
	if len(s.cfg.Presets) != 3 {
		t.Errorf("the save did not go through: %+v", s.cfg.Presets)
	}

	// A save that leaves the presets alone repeats nothing.
	w = httptest.NewRecorder()
	s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("POST", "/api/config", strings.NewReader(`{"port":5001,"grace_seconds":60}`))))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "warnings") {
		t.Errorf("unrelated save: %d %s", w.Code, w.Body.String())
	}

	// [테스트] carries the warning too (the fake runner fails; the
	// warning rides along), and none for the safe file.
	test := func(p config.Preset) string {
		b, _ := json.Marshal(p)
		w := httptest.NewRecorder()
		s.Routes(false).ServeHTTP(w, localRequest(s, httptest.NewRequest("POST", "/api/presets/test", strings.NewReader(string(b)))))
		return w.Body.String()
	}
	if got := test(presets[1]); !strings.Contains(got, "writable_by_others") {
		t.Errorf("test of open.ps1: %s", got)
	}
	if got := test(presets[0]); strings.Contains(got, "warnings") {
		t.Errorf("test of safe.ps1: %s", got)
	}
}

func TestPresetWarningsFolderAndUser(t *testing.T) {
	dir, safe, _ := presetFiles(t)
	s := newTestServer(t, config.Config{})
	me := mySID(t)

	// The user the preset runs as may write to its own scripts; without
	// knowing that user, this account is just another principal (unless
	// it owns the file, which a test cannot rely on: an elevated token
	// makes Administrators the owner).
	s.presetUserSID = func() string { return me }
	if got := s.presetWarnings([]config.Preset{{Slot: 1, Type: "script", Path: safe}}); len(got) != 0 {
		t.Errorf("own script: %+v", got)
	}

	// A folder others may add files to warns with the folder's path.
	setDACL(t, dir, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+me+")(A;;0x1301bf;;;AU)")
	got := s.presetWarnings([]config.Preset{{Slot: 4, Type: "script", Path: safe}})
	if len(got) != 1 || got[0].Path != dir || got[0].Slot != 4 {
		t.Errorf("writable folder: %+v", got)
	}

	// URL presets, UNC paths and missing files say nothing.
	none := s.presetWarnings([]config.Preset{
		{Slot: 5, Type: "url", Path: "https://example.com"},
		{Slot: 6, Type: "program", Path: `\\server\share\x.exe`},
	})
	if len(none) != 0 {
		t.Errorf("url/unc: %+v", none)
	}
}

func TestParentDir(t *testing.T) {
	for in, want := range map[string]string{
		`C:\a\b.exe`: `C:\a`,
		`C:\b.exe`:   `C:\`,
		`b.exe`:      "",
	} {
		if got := parentDir(in); got != want {
			t.Errorf("parentDir(%q) = %q, want %q", in, got, want)
		}
	}
}
