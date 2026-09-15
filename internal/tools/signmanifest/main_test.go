package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// genkeyOutput runs genkey and returns the seed/pub it printed.
func genkeyOutput(t *testing.T) (seed, pub string) {
	t.Helper()
	var out bytes.Buffer
	if err := run([]string{"genkey"}, &out); err != nil {
		t.Fatalf("genkey: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "seed":
			seed = v
		case "pub":
			pub = v
		}
	}
	if seed == "" || pub == "" {
		t.Fatalf("genkey printed %q", out.String())
	}
	return seed, pub
}

func TestGenkey(t *testing.T) {
	seed, pub := genkeyOutput(t)
	s, err := base64.StdEncoding.DecodeString(seed)
	if err != nil || len(s) != ed25519.SeedSize {
		t.Fatalf("seed %q: %v (%d bytes)", seed, err, len(s))
	}
	p, err := base64.StdEncoding.DecodeString(pub)
	if err != nil || len(p) != ed25519.PublicKeySize {
		t.Fatalf("pub %q: %v (%d bytes)", pub, err, len(p))
	}
	if got := ed25519.NewKeyFromSeed(s).Public().(ed25519.PublicKey); !bytes.Equal(got, p) {
		t.Errorf("printed pub does not belong to printed seed")
	}
	if pub == release.PublicKeyBase64 {
		t.Errorf("genkey must not reproduce the production key")
	}
}

func TestSignThenVerify(t *testing.T) {
	seed, pub := genkeyOutput(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "smartthings-pc-control.exe")
	payload := []byte(strings.Repeat("MZ-fake-exe-", 500))
	if err := os.WriteFile(exe, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "update.json")

	// Seed via env:NAME, like CI.
	t.Setenv("TEST_SIGNING_KEY", seed)
	var stdout bytes.Buffer
	err := run([]string{"sign", "-key", "env:TEST_SIGNING_KEY", "-version", "v1.0.0", "-min-version", "v0.3.4", "-arch", "amd64", "-out", out, exe}, &stdout)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(out + ".sig")
	if err != nil {
		t.Fatal(err)
	}

	// Signature text is standard base64 of 64 bytes, over the exact file.
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		t.Fatalf("sig file %q: %v", sig, err)
	}
	pubRaw, _ := base64.StdEncoding.DecodeString(pub)
	m, err := release.VerifyManifest(data, sig, ed25519.PublicKey(pubRaw))
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if m.Version != "v1.0.0" || m.MinVersion != "v0.3.4" || len(m.Assets) != 1 {
		t.Errorf("manifest = %+v", m)
	}
	sum := sha256.Sum256(payload)
	a := m.Assets[0]
	if a.Name != "smartthings-pc-control.exe" || a.SHA256 != hex.EncodeToString(sum[:]) || a.Arch != "amd64" || a.Size != int64(len(payload)) {
		t.Errorf("asset = %+v", a)
	}
	if _, err := json.Marshal(m); err != nil {
		t.Error(err)
	}
	if !strings.HasSuffix(string(data), "\n") || !strings.Contains(string(data), "\n  \"version\"") {
		t.Errorf("manifest is not pretty-printed JSON: %q", data)
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	if ts, _ := generic["published_at"].(string); !strings.HasSuffix(ts, "Z") {
		t.Errorf("published_at %q is not RFC3339 UTC", ts)
	}

	// verify subcommand with the right key.
	stdout.Reset()
	if err := run([]string{"verify", "-pub", pub, out, out + ".sig"}, &stdout); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(stdout.String(), "OK:") || !strings.Contains(stdout.String(), `"version": "v1.0.0"`) {
		t.Errorf("verify printed %q", stdout.String())
	}
	// Default -pub is the production key, which did not sign this.
	if err := run([]string{"verify", out, out + ".sig"}, &stdout); !errors.Is(err, release.ErrBadSignature) {
		t.Errorf("verify with production key: err = %v, want ErrBadSignature", err)
	}
	// Wrong key.
	_, otherPub := genkeyOutput(t)
	if err := run([]string{"verify", "-pub", otherPub, out, out + ".sig"}, &stdout); !errors.Is(err, release.ErrBadSignature) {
		t.Errorf("verify with wrong key: err = %v, want ErrBadSignature", err)
	}
	// Tampered manifest byte.
	tampered := append([]byte(nil), data...)
	tampered[bytes.Index(tampered, []byte("v1.0.0"))+3] = '9'
	tPath := filepath.Join(dir, "tampered.json")
	os.WriteFile(tPath, tampered, 0o644)
	if err := run([]string{"verify", "-pub", pub, tPath, out + ".sig"}, &stdout); !errors.Is(err, release.ErrBadSignature) {
		t.Errorf("verify tampered: err = %v, want ErrBadSignature", err)
	}
}

func TestSignArgumentErrors(t *testing.T) {
	seed, _ := genkeyOutput(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "a.exe")
	os.WriteFile(exe, []byte("x"), 0o644)
	empty := filepath.Join(dir, "empty.exe")
	os.WriteFile(empty, nil, 0o644)
	out := filepath.Join(dir, "update.json")
	var sink bytes.Buffer
	cases := map[string][]string{
		"no key":            {"sign", "-version", "v1.0.0", "-out", out, exe},
		"no version":        {"sign", "-key", seed, "-out", out, exe},
		"bad version":       {"sign", "-key", seed, "-version", "latest", "-out", out, exe},
		"bad min version":   {"sign", "-key", seed, "-version", "v1.0.0", "-min-version", "x", "-out", out, exe},
		"no files":          {"sign", "-key", seed, "-version", "v1.0.0", "-out", out},
		"missing file":      {"sign", "-key", seed, "-version", "v1.0.0", "-out", out, filepath.Join(dir, "nope.exe")},
		"empty file":        {"sign", "-key", seed, "-version", "v1.0.0", "-out", out, empty},
		"garbage key":       {"sign", "-key", "not base64", "-version", "v1.0.0", "-out", out, exe},
		"short key":         {"sign", "-key", base64.StdEncoding.EncodeToString([]byte("short")), "-version", "v1.0.0", "-out", out, exe},
		"unset env":         {"sign", "-key", "env:SIGNMANIFEST_TEST_UNSET", "-version", "v1.0.0", "-out", out, exe},
		"unknown command":   {"frobnicate"},
		"no command":        {},
		"verify wrong argc": {"verify", out},
		"verify bad pub":    {"verify", "-pub", "zz", out, out + ".sig"},
	}
	for name, args := range cases {
		if err := run(args, &sink); err == nil {
			t.Errorf("%s: run(%q) succeeded, want error", name, args)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a failing sign wrote %s", out)
	}
}
