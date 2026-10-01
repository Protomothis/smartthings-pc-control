package gui

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// stagedUpdate is one self-update as the tray leaves it for the elevated
// updater: a downloaded exe plus the signed manifest it was checked
// against, all in the user's own (writable) folder.
type stagedUpdate struct {
	args    UpdateApplyArgs
	cur     string // installed exe
	payload []byte // what the release really contains
	asset   *release.ManifestAsset
	m       *release.Manifest
	pub     ed25519.PublicKey
}

func newStagedUpdate(t *testing.T, version string) *stagedUpdate {
	t.Helper()
	// The real lockUpdateDir would lock this non-elevated test out of its
	// own temp dir; the DACL itself is tested in internal/secureacl.
	orig := lockUpdateDir
	lockUpdateDir = func(string) error { return nil }
	t.Cleanup(func() { lockUpdateDir = orig })
	// updateLog writes gui.log under %LOCALAPPDATA%; keep it out of the
	// developer's real one.
	t.Setenv("LOCALAPPDATA", t.TempDir())

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &stagedUpdate{pub: pub, payload: []byte("MZ new release " + version)}
	sum := sha256.Sum256(s.payload)
	data, err := release.EncodeManifest(&release.Manifest{
		Version: version,
		Assets: []release.ManifestAsset{{
			Name: "smartthings-pc-control.exe", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(s.payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// What the tray did: verify the manifest, then stage everything.
	if s.m, err = release.VerifyManifest(data, release.EncodeSignature(release.Sign(data, priv)), pub); err != nil {
		t.Fatal(err)
	}
	s.asset = &s.m.Assets[0]

	install := t.TempDir()
	s.cur = filepath.Join(install, "smartthings-pc-control.exe")
	if err := os.WriteFile(s.cur, []byte("MZ installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "update")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	s.args.NewExe = stagingPath(staging, version)
	if err := os.WriteFile(s.args.NewExe, s.payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if s.args.Manifest, s.args.Signature, err = stageManifest(staging, s.m); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *stagedUpdate) updateDir() string { return filepath.Join(filepath.Dir(s.cur), "update") }

func (s *stagedUpdate) assertInstalledUntouched(t *testing.T) {
	t.Helper()
	if got, _ := os.ReadFile(s.cur); string(got) != "MZ installed" {
		t.Errorf("installed exe changed: %q", got)
	}
	if _, err := os.Stat(s.cur + oldExeSuffix); err == nil {
		t.Error("installed exe was renamed")
	}
	if _, err := os.Stat(s.updateDir()); err == nil {
		t.Error("rejected update left <install>\\update behind")
	}
}

// Happy path: the verified copy in <install>\update is what gets installed,
// even when the user-writable original changes right after the check.
func TestStageVerifiedUpdateInstallsTheCopy(t *testing.T) {
	s := newStagedUpdate(t, "v1.2.0")
	verified, err := stageVerifiedUpdate(s.args, s.cur, "v1.1.2", s.pub)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(verified) != s.updateDir() {
		t.Errorf("verified copy at %s, want inside %s", verified, s.updateDir())
	}
	for _, name := range []string{release.ManifestName, release.ManifestSigName} {
		if _, err := os.Stat(filepath.Join(s.updateDir(), name)); err != nil {
			t.Errorf("%s was not copied: %v", name, err)
		}
	}

	// A same-user process swaps the staged file after the check …
	if err := os.WriteFile(s.args.NewExe, []byte("MZ evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	// … which no longer matters: the swap uses the verified copy.
	if err := swapExe(verified, s.cur); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(s.cur); !bytes.Equal(got, s.payload) {
		t.Errorf("installed exe = %q, want the verified release %q", got, s.payload)
	}
	if got, _ := os.ReadFile(s.cur + oldExeSuffix); string(got) != "MZ installed" {
		t.Errorf("old exe = %q", got)
	}
}

// The tray's check passes, then the staged exe is replaced before the
// elevated updater runs: the copy no longer matches the signed manifest.
func TestStageVerifiedUpdateRejectsTamperedExe(t *testing.T) {
	s := newStagedUpdate(t, "v1.2.0")
	if err := verifyDownloadedHash(s.args.NewExe, s.asset); err != nil {
		t.Fatalf("tray-side check: %v", err)
	}
	evil := bytes.Repeat([]byte{'X'}, len(s.payload)) // same size, other bytes
	if err := os.WriteFile(s.args.NewExe, evil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := stageVerifiedUpdate(s.args, s.cur, "v1.1.2", s.pub)
	if !errors.Is(err, errHashMismatch) {
		t.Errorf("err = %v, want errHashMismatch", err)
	}
	s.assertInstalledUntouched(t)
}

func TestStageVerifiedUpdateRejectsBadSignature(t *testing.T) {
	t.Run("tampered signature", func(t *testing.T) {
		s := newStagedUpdate(t, "v1.2.0")
		sig, _ := os.ReadFile(s.args.Signature) // base64 text
		if sig[0] == 'A' {
			sig[0] = 'B'
		} else {
			sig[0] = 'A'
		}
		os.WriteFile(s.args.Signature, sig, 0o644)
		if _, err := stageVerifiedUpdate(s.args, s.cur, "v1.1.2", s.pub); !errors.Is(err, release.ErrBadSignature) {
			t.Errorf("err = %v, want ErrBadSignature", err)
		}
		s.assertInstalledUntouched(t)
	})
	t.Run("manifest rewritten for an evil exe", func(t *testing.T) {
		s := newStagedUpdate(t, "v1.2.0")
		evil := []byte("MZ evil build")
		sum := sha256.Sum256(evil)
		data, _ := release.EncodeManifest(&release.Manifest{Version: "v1.2.0", Assets: []release.ManifestAsset{{
			Name: "smartthings-pc-control.exe", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(evil)),
		}}})
		os.WriteFile(s.args.Manifest, data, 0o644) // signature left as is
		os.WriteFile(s.args.NewExe, evil, 0o644)
		if _, err := stageVerifiedUpdate(s.args, s.cur, "v1.1.2", s.pub); !errors.Is(err, release.ErrBadSignature) {
			t.Errorf("err = %v, want ErrBadSignature", err)
		}
		s.assertInstalledUntouched(t)
	})
	t.Run("signed with another key", func(t *testing.T) {
		s := newStagedUpdate(t, "v1.2.0")
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		if _, err := stageVerifiedUpdate(s.args, s.cur, "v1.1.2", other); !errors.Is(err, release.ErrBadSignature) {
			t.Errorf("err = %v, want ErrBadSignature", err)
		}
		s.assertInstalledUntouched(t)
	})
}

// A validly signed but older (or the same) release must not be replayed
// to downgrade the installation.
func TestStageVerifiedUpdateRejectsDowngrade(t *testing.T) {
	for _, installed := range []string{"v1.2.0", "v1.3.0", "dev"} {
		s := newStagedUpdate(t, "v1.2.0")
		if _, err := stageVerifiedUpdate(s.args, s.cur, installed, s.pub); err == nil {
			t.Errorf("installed %s: update to v1.2.0 accepted", installed)
		}
		s.assertInstalledUntouched(t)
	}
}

// Leftovers in <install>\update, including a link planted while the folder
// was still user-writable, are cleared before anything is copied there.
func TestPrepareUpdateDirReplacesLeftovers(t *testing.T) {
	s := newStagedUpdate(t, "v1.2.0")
	target := t.TempDir()
	victim := filepath.Join(target, "keep.txt")
	os.WriteFile(victim, []byte("keep"), 0o644)
	if err := os.Symlink(target, s.updateDir()); err != nil {
		// Symlinks need developer mode or admin rights; a plain leftover
		// file at that path exercises the same replace.
		os.WriteFile(s.updateDir(), []byte("leftover"), 0o644)
	}
	if _, err := stageVerifiedUpdate(s.args, s.cur, "v1.1.2", s.pub); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(s.updateDir()); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("update dir is not a fresh directory: %v %v", fi, err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "keep" {
		t.Errorf("link target was touched: %q %v", got, err)
	}
}

func TestStageManifestNeedsSignedBytes(t *testing.T) {
	if _, _, err := stageManifest(t.TempDir(), &release.Manifest{Version: "v1.2.0"}); err == nil {
		t.Error("manifest without signed bytes was staged")
	}
}
