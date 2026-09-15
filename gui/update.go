package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// downloadTimeout bounds the whole asset download (GitHub CDN is usually
// fast, but a stalled connection must not hang the progress dialog forever).
const downloadTimeout = 10 * time.Minute

// checkLatestRelease asks GitHub for the newest published release. The
// request, asset lookup and version comparison live in internal/release so
// the service can share them for system.update_available.
func checkLatestRelease() (*release.Info, error) {
	return release.Latest(context.Background(), &http.Client{Timeout: 8 * time.Second})
}

// fetchManifest downloads and verifies rel's signed update manifest (#66).
// Errors: release.ErrNoManifest (unsigned release), release.ErrBadSignature
// (tampered / wrong key / other release's manifest), or a network error.
func fetchManifest(rel *release.Info) (*release.Manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return release.FetchManifest(ctx, &http.Client{Timeout: 20 * time.Second}, rel)
}

// verifyDownloadedHash compares the staged file's SHA-256 and size with what
// the signed manifest promised. This runs before the file is ever executed
// (verifyDownloadedExe is the second, weaker check). On mismatch the file
// is removed and the error wraps errHashMismatch.
func verifyDownloadedHash(path string, asset *release.ManifestAsset) error {
	sum, size, err := fileSHA256(path)
	if err != nil {
		os.Remove(path)
		return err
	}
	if err := asset.Verify(sum, size); err != nil {
		os.Remove(path)
		return fmt.Errorf("%w: %v", errHashMismatch, err)
	}
	return nil
}

// errHashMismatch marks a download whose hash/size differ from the manifest;
// the GUI shows update.hashmismatch for it.
var errHashMismatch = errors.New("downloaded file does not match the update manifest")

// fileSHA256 returns the lowercase hex SHA-256 and the size of path.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// chooseStagingDir picks where the downloaded exe is staged: an "update"
// folder next to the running exe when that is writable, otherwise one under
// tempDir (the exe may live in Program Files). The directory is created.
func chooseStagingDir(exeDir, tempDir string) string {
	primary := filepath.Join(exeDir, "update")
	if dirWritable(primary) {
		return primary
	}
	fallback := filepath.Join(tempDir, "smartthings-pc-control", "update")
	os.MkdirAll(fallback, 0o755)
	return fallback
}

// dirWritable creates dir if needed and probes it with a temp file.
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// stagingDir resolves chooseStagingDir for the running exe.
func stagingDir() string {
	exe, err := os.Executable()
	if err != nil {
		return chooseStagingDir("", os.TempDir())
	}
	return chooseStagingDir(filepath.Dir(exe), os.TempDir())
}

// stagingPath is the final name of a staged download for tag inside dir,
// e.g. <dir>\smartthings-pc-control.v0.3.3.exe. The tag is sanitised so a
// hostile release name cannot escape dir.
func stagingPath(dir, tag string) string {
	var b strings.Builder
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := b.String()
	if name == "" {
		name = "latest"
	}
	return filepath.Join(dir, "smartthings-pc-control."+name+".exe")
}

// downloadUpdate streams url into stagingPath(dir, tag), writing to a ".part"
// file first and renaming only after the full body arrived. progress (may be
// nil) receives bytes so far and the total (-1 when unknown). Returns the
// final path.
func downloadUpdate(ctx context.Context, url, dir, tag string, progress func(done, total int64)) (string, error) {
	final := stagingPath(dir, tag)
	part := final + ".part"

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	// Any failure below leaves no half-written file behind.
	fail := func(e error) (string, error) {
		f.Close()
		os.Remove(part)
		return "", e
	}

	total := resp.ContentLength
	if progress != nil {
		progress(0, total)
	}
	var done int64
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fail(werr)
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			return fail(rerr)
		}
	}
	if total >= 0 && done != total {
		return fail(fmt.Errorf("download: got %d bytes, expected %d", done, total))
	}
	if done == 0 {
		return fail(errors.New("download: empty body"))
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return "", err
	}
	os.Remove(final) // a previous attempt may have left one
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return "", err
	}
	return final, nil
}

// versionOutputMatches reports whether the "version" output of a downloaded
// exe names tag as a whole token on some line (so v0.3.3 never matches
// v0.3.30).
func versionOutputMatches(out, tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		for _, f := range strings.Fields(line) {
			if strings.EqualFold(f, tag) {
				return true
			}
		}
	}
	return false
}

// verifyDownloadedExe runs "<path> version" and checks that it reports tag —
// a cheap guard against a truncated, corrupted or wrong asset before we let
// it replace the installed binary.
func verifyDownloadedExe(path, tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("downloaded exe failed to run: %w", err)
	}
	if !versionOutputMatches(string(out), tag) {
		return fmt.Errorf("downloaded exe reports %q, expected %s", strings.TrimSpace(string(out)), tag)
	}
	return nil
}

// formatBytes renders a byte count for the progress label.
func formatBytes(n int64) string {
	switch {
	case n < 0:
		return "?"
	case n < 1024*1024:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
