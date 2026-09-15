package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// Version comparison and asset selection moved to internal/release (see
// its tests); the GUI only keeps the download/staging logic.

func TestStagingPath(t *testing.T) {
	dir := `C:\PC Control\update`
	cases := map[string]string{
		"v0.3.3":         `C:\PC Control\update\smartthings-pc-control.v0.3.3.exe`,
		`..\..\evil`:     `C:\PC Control\update\smartthings-pc-control..._.._evil.exe`,
		"v1.0.0-rc1 (x)": `C:\PC Control\update\smartthings-pc-control.v1.0.0-rc1__x_.exe`,
		"":               `C:\PC Control\update\smartthings-pc-control.latest.exe`,
	}
	for tag, want := range cases {
		if got := stagingPath(dir, tag); got != want {
			t.Errorf("stagingPath(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestChooseStagingDir(t *testing.T) {
	exeDir := t.TempDir()
	tmp := t.TempDir()
	if got, want := chooseStagingDir(exeDir, tmp), filepath.Join(exeDir, "update"); got != want {
		t.Errorf("writable exe dir: got %q, want %q", got, want)
	}
	if st, err := os.Stat(filepath.Join(exeDir, "update")); err != nil || !st.IsDir() {
		t.Errorf("staging dir was not created: %v", err)
	}

	// A regular file where the exe dir should be makes MkdirAll fail →
	// fall back under the temp dir.
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o644)
	got := chooseStagingDir(filepath.Join(blocker, "sub"), tmp)
	if want := filepath.Join(tmp, "smartthings-pc-control", "update"); got != want {
		t.Errorf("unwritable exe dir: got %q, want %q", got, want)
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Errorf("fallback dir was not created: %v", err)
	}
}

func TestDownloadUpdate(t *testing.T) {
	payload := strings.Repeat("MZ-fake-exe-", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			// Explicit length: the server would otherwise chunk a body
			// this large and the client would see ContentLength -1.
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Write([]byte(payload))
		case "/short":
			w.Header().Set("Content-Length", "99999")
			w.Write([]byte("tiny"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	var lastDone, lastTotal int64
	got, err := downloadUpdate(context.Background(), srv.URL+"/ok", dir, "v0.3.3", func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if want := stagingPath(dir, "v0.3.3"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(got); err != nil || string(data) != payload {
		t.Errorf("content mismatch (err=%v, %d bytes)", err, len(data))
	}
	if lastDone != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Errorf("progress ended at %d/%d, want %d/%d", lastDone, lastTotal, len(payload), len(payload))
	}
	if _, err := os.Stat(got + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part file left behind")
	}

	// Truncated body (Content-Length mismatch) must fail and clean up.
	if _, err := downloadUpdate(context.Background(), srv.URL+"/short", dir, "v9.9.9", nil); err == nil {
		t.Errorf("short download succeeded, want error")
	}
	if _, err := os.Stat(stagingPath(dir, "v9.9.9")); !os.IsNotExist(err) {
		t.Errorf("short download left final file")
	}
	if _, err := os.Stat(stagingPath(dir, "v9.9.9") + ".part"); !os.IsNotExist(err) {
		t.Errorf("short download left .part file")
	}

	if _, err := downloadUpdate(context.Background(), srv.URL+"/missing", dir, "v1.1.1", nil); err == nil {
		t.Errorf("HTTP 404 succeeded, want error")
	}

	// Cancelled context aborts before anything is written.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := downloadUpdate(ctx, srv.URL+"/ok", dir, "v2.2.2", nil); err == nil {
		t.Errorf("cancelled download succeeded, want error")
	}
}

func TestVersionOutputMatches(t *testing.T) {
	cases := []struct {
		out, tag string
		want     bool
	}{
		{"SmartThings PC Control v0.3.3\r\n", "v0.3.3", true},
		{"SmartThings PC Control V0.3.3", "v0.3.3", true},
		{"SmartThings PC Control v0.3.30", "v0.3.3", false}, // whole token only
		{"SmartThings PC Control dev", "v0.3.3", false},
		{"", "v0.3.3", false},
		{"v0.3.3", "", false},
	}
	for _, c := range cases {
		if got := versionOutputMatches(c.out, c.tag); got != c.want {
			t.Errorf("versionOutputMatches(%q, %q) = %v, want %v", c.out, c.tag, got, c.want)
		}
	}
}

func TestParseUpdateApplyArgs(t *testing.T) {
	exe, pid, err := ParseUpdateApplyArgs([]string{`C:\PC Control\update\smartthings-pc-control.v0.3.3.exe`, "4242"})
	if err != nil || exe != `C:\PC Control\update\smartthings-pc-control.v0.3.3.exe` || pid != 4242 {
		t.Errorf("valid args: exe=%q pid=%d err=%v", exe, pid, err)
	}
	bad := [][]string{
		nil,
		{`C:\a.exe`},
		{`C:\a.exe`, "1", "extra"},
		{`relative\a.exe`, "1"},
		{`C:\a.txt`, "1"},
		{`C:\a.exe`, "notapid"},
		{`C:\a.exe`, "-5"},
		{"", "1"},
	}
	for _, args := range bad {
		if _, _, err := ParseUpdateApplyArgs(args); err == nil {
			t.Errorf("ParseUpdateApplyArgs(%q) accepted, want error", args)
		}
	}
}

func TestVerifyDownloadedHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.exe")
	payload := []byte("MZ-not-really-an-exe")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	ok := &release.ManifestAsset{Name: "staged.exe", SHA256: strings.ToUpper(hexSum), Size: int64(len(payload))}
	if err := verifyDownloadedHash(path, ok); err != nil {
		t.Fatalf("matching hash rejected: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("matching file was removed: %v", err)
	}

	// Wrong hash → errHashMismatch and the file is gone.
	bad := &release.ManifestAsset{Name: "staged.exe", SHA256: strings.Repeat("00", 32), Size: int64(len(payload))}
	err := verifyDownloadedHash(path, bad)
	if !errors.Is(err, errHashMismatch) {
		t.Errorf("wrong hash: err = %v, want errHashMismatch", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("mismatching file left behind (stat err=%v)", err)
	}

	// Wrong size with the right hash is a mismatch too.
	os.WriteFile(path, payload, 0o644)
	short := &release.ManifestAsset{Name: "staged.exe", SHA256: hexSum, Size: 1}
	if err := verifyDownloadedHash(path, short); !errors.Is(err, errHashMismatch) {
		t.Errorf("wrong size: err = %v, want errHashMismatch", err)
	}

	// Missing file is an ordinary error, not a mismatch.
	if err := verifyDownloadedHash(filepath.Join(dir, "nope.exe"), ok); err == nil || errors.Is(err, errHashMismatch) {
		t.Errorf("missing file: err = %v", err)
	}

	// fileSHA256 reports lowercase hex and the byte count.
	os.WriteFile(path, payload, 0o644)
	if got, n, err := fileSHA256(path); err != nil || got != hexSum || n != int64(len(payload)) {
		t.Errorf("fileSHA256 = %q, %d, %v; want %q, %d", got, n, err, hexSum, len(payload))
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{-1: "?", 0: "0 KB", 1: "1 KB", 1024: "1 KB", 1536: "2 KB", 1048576: "1.0 MB", 15 * 1048576: "15.0 MB"}
	for n, want := range cases {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
