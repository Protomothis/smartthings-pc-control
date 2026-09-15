package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testKey makes a throwaway keypair; the production key is never used to
// sign anything in tests.
func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sampleManifest() *Manifest {
	return &Manifest{
		Version:     "v1.0.0",
		PublishedAt: "2026-09-15T00:00:00Z",
		Assets: []ManifestAsset{
			{Name: "smartthings-pc-control.exe", SHA256: strings.Repeat("ab", 32), Arch: "amd64", Size: 12345},
		},
	}
}

// signed encodes and signs m, returning the two asset bodies.
func signed(t *testing.T, m *Manifest, priv ed25519.PrivateKey) (data, sig []byte) {
	t.Helper()
	data, err := EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	return data, EncodeSignature(Sign(data, priv))
}

func TestPublicKey(t *testing.T) {
	if len(PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("embedded PublicKey decodes to %d bytes, want %d (PublicKeyBase64 = %q)", len(PublicKey), ed25519.PublicKeySize, PublicKeyBase64)
	}
	if got := base64.StdEncoding.EncodeToString(PublicKey); got != PublicKeyBase64 {
		t.Errorf("round trip = %q, want %q", got, PublicKeyBase64)
	}
}

func TestVerifyManifest(t *testing.T) {
	pub, priv := testKey(t)
	data, sig := signed(t, sampleManifest(), priv)

	m, err := VerifyManifest(data, sig, pub)
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if m.Version != "v1.0.0" || len(m.Assets) != 1 || m.Assets[0].Size != 12345 || m.Assets[0].Arch != "amd64" {
		t.Errorf("decoded %+v", m)
	}
	// JSON shape: snake_case keys.
	for _, key := range []string{`"version"`, `"published_at"`, `"assets"`, `"name"`, `"sha256"`, `"arch"`, `"size"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("encoded manifest lacks %s: %s", key, data)
		}
	}
	if strings.Contains(string(data), "min_version") {
		t.Errorf("empty min_version should be omitted: %s", data)
	}

	// Raw 64-byte signature is accepted too, as is base64 with whitespace.
	raw := Sign(data, priv)
	if _, err := VerifyManifest(data, raw, pub); err != nil {
		t.Errorf("raw signature rejected: %v", err)
	}
	if _, err := VerifyManifest(data, []byte("  \r\n"+base64.StdEncoding.EncodeToString(raw)+"\r\n"), pub); err != nil {
		t.Errorf("padded base64 signature rejected: %v", err)
	}

	// One flipped byte in the manifest.
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)/2] ^= 0x01
	if _, err := VerifyManifest(tampered, sig, pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered manifest: err = %v, want ErrBadSignature", err)
	}
	// Wrong key.
	other, _ := testKey(t)
	if _, err := VerifyManifest(data, sig, other); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: err = %v, want ErrBadSignature", err)
	}
	// Nil / short key must not panic (ed25519.Verify panics on bad sizes).
	if _, err := VerifyManifest(data, sig, nil); !errors.Is(err, ErrBadSignature) {
		t.Errorf("nil key: err = %v", err)
	}
	// Garbage signature.
	if _, err := VerifyManifest(data, []byte("not base64!"), pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("garbage sig: err = %v", err)
	}
	if _, err := VerifyManifest(data, []byte(base64.StdEncoding.EncodeToString([]byte("short"))), pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("short sig: err = %v", err)
	}

	// Correctly signed but semantically empty manifests are rejected.
	for name, bad := range map[string]*Manifest{
		"no version": {Assets: sampleManifest().Assets},
		"no assets":  {Version: "v1.0.0"},
		"no sha256":  {Version: "v1.0.0", Assets: []ManifestAsset{{Name: "x.exe"}}},
	} {
		d, s := signed(t, bad, priv)
		if _, err := VerifyManifest(d, s, pub); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", name, err)
		}
	}
	// Signed bytes that are not JSON.
	notJSON := []byte("hello")
	if _, err := VerifyManifest(notJSON, Sign(notJSON, priv), pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("non-JSON: err = %v", err)
	}
}

func TestFetchManifest(t *testing.T) {
	pub, priv := testKey(t)
	data, sig := signed(t, sampleManifest(), priv)
	_, otherPriv := testKey(t)
	_, badSig := signed(t, sampleManifest(), otherPriv)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/update.json":
			w.Write(data)
		case "/update.json.sig":
			w.Write(sig)
		case "/bad.sig":
			w.Write(badSig)
		case "/huge.json":
			w.Write(make([]byte, maxManifestSize+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel := func(tag string, assets ...Asset) *Info { return &Info{TagName: tag, Assets: assets} }
	man := Asset{Name: ManifestName, DownloadURL: srv.URL + "/update.json"}
	sigA := Asset{Name: ManifestSigName, DownloadURL: srv.URL + "/update.json.sig"}
	exe := Asset{Name: "smartthings-pc-control.exe", DownloadURL: srv.URL + "/exe"}
	ctx := context.Background()

	m, err := fetchManifest(ctx, nil, rel("v1.0.0", exe, man, sigA), pub)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if m.Version != "v1.0.0" {
		t.Errorf("Version = %q", m.Version)
	}

	// Missing either asset → ErrNoManifest (GUI falls back to release page).
	for name, r := range map[string]*Info{
		"no assets":          rel("v1.0.0", exe),
		"no sig":             rel("v1.0.0", exe, man),
		"no manifest":        rel("v1.0.0", exe, sigA),
		"case differs":       rel("v1.0.0", exe, Asset{Name: "Update.json", DownloadURL: man.DownloadURL}, sigA),
		"manifest lacks url": rel("v1.0.0", exe, Asset{Name: ManifestName}, sigA),
		"nil release":        nil,
	} {
		if _, err := fetchManifest(ctx, nil, r, pub); !errors.Is(err, ErrNoManifest) {
			t.Errorf("%s: err = %v, want ErrNoManifest", name, err)
		}
	}

	// Replayed onto a different release tag.
	if _, err := fetchManifest(ctx, nil, rel("v1.0.1", exe, man, sigA), pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tag mismatch: err = %v, want ErrBadSignature", err)
	}
	// Signature by another key.
	bad := Asset{Name: ManifestSigName, DownloadURL: srv.URL + "/bad.sig"}
	if _, err := fetchManifest(ctx, nil, rel("v1.0.0", exe, man, bad), pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("bad sig: err = %v, want ErrBadSignature", err)
	}
	// Production key must not verify a test-signed manifest.
	if _, err := FetchManifest(ctx, nil, rel("v1.0.0", exe, man, sigA)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("production key accepted test signature: err = %v", err)
	}
	// Oversized manifest body.
	huge := Asset{Name: ManifestName, DownloadURL: srv.URL + "/huge.json"}
	if _, err := fetchManifest(ctx, nil, rel("v1.0.0", exe, huge, sigA), pub); err == nil || errors.Is(err, ErrNoManifest) {
		t.Errorf("oversized manifest: err = %v", err)
	}
	// Download failure is neither "no manifest" nor "bad signature".
	gone := Asset{Name: ManifestSigName, DownloadURL: srv.URL + "/missing"}
	if _, err := fetchManifest(ctx, nil, rel("v1.0.0", exe, man, gone), pub); err == nil || errors.Is(err, ErrNoManifest) || errors.Is(err, ErrBadSignature) {
		t.Errorf("HTTP 404 on sig: err = %v", err)
	}
}

func TestAssetForAndURL(t *testing.T) {
	m := &Manifest{Assets: []ManifestAsset{
		{Name: "any.exe", SHA256: "aa"},
		{Name: "arm.exe", SHA256: "bb", Arch: "arm64"},
		{Name: "amd.exe", SHA256: "cc", Arch: "AMD64"},
	}}
	if a, ok := m.AssetFor("amd64"); !ok || a.Name != "amd.exe" {
		t.Errorf("amd64 → %+v %v", a, ok)
	}
	if a, ok := m.AssetFor("arm64"); !ok || a.Name != "arm.exe" {
		t.Errorf("arm64 → %+v %v", a, ok)
	}
	if a, ok := m.AssetFor("386"); !ok || a.Name != "any.exe" {
		t.Errorf("386 falls back to arch-less asset: %+v %v", a, ok)
	}
	strict := &Manifest{Assets: []ManifestAsset{{Name: "arm.exe", Arch: "arm64"}}}
	if _, ok := strict.AssetFor("amd64"); ok {
		t.Errorf("arch mismatch without wildcard should not match")
	}
	if _, ok := (*Manifest)(nil).AssetFor("amd64"); ok {
		t.Errorf("nil manifest matched")
	}

	rel := &Info{Assets: []Asset{
		{Name: "smartthings-pc-control.exe", DownloadURL: "https://x/a.exe"},
		{Name: "update.json", DownloadURL: "https://x/u.json"},
		{Name: "nourl.exe"},
	}}
	if got := AssetURL(rel, "smartthings-pc-control.exe"); got != "https://x/a.exe" {
		t.Errorf("AssetURL = %q", got)
	}
	if got := AssetURL(rel, "SMARTTHINGS-PC-CONTROL.EXE"); got != "" {
		t.Errorf("AssetURL must match exactly, got %q", got)
	}
	if got := AssetURL(rel, "nourl.exe"); got != "" {
		t.Errorf("AssetURL returned asset without URL: %q", got)
	}
	if got := AssetURL(nil, "x"); got != "" {
		t.Errorf("AssetURL(nil) = %q", got)
	}
}

func TestAllows(t *testing.T) {
	none := &Manifest{Version: "v1.0.0"}
	min := &Manifest{Version: "v1.0.0", MinVersion: "v0.3.4"}
	cases := []struct {
		m       *Manifest
		current string
		want    bool
	}{
		{none, "v0.1.0", true},
		{min, "v0.3.4", true},
		{min, "v0.3.9", true},
		{min, "v0.3.3", false},
		{min, "v0.2.0", false},
		{min, "dev", true},
		{min, "", true},
		{nil, "v0.1.0", true},
		{&Manifest{MinVersion: "garbage"}, "v0.1.0", true},
	}
	for _, c := range cases {
		if got := c.m.Allows(c.current); got != c.want {
			t.Errorf("Allows(min=%v, current=%q) = %v, want %v", c.m, c.current, got, c.want)
		}
	}
}

func TestAssetVerify(t *testing.T) {
	a := &ManifestAsset{Name: "x.exe", SHA256: "ABCDEF0123", Size: 10}
	if err := a.Verify("abcdef0123", 10); err != nil {
		t.Errorf("case-insensitive hex rejected: %v", err)
	}
	if err := a.Verify(" abcdef0123 ", 10); err != nil {
		t.Errorf("whitespace rejected: %v", err)
	}
	if err := a.Verify("abcdef0124", 10); err == nil {
		t.Errorf("wrong hash accepted")
	}
	if err := a.Verify("abcdef0123", 11); err == nil {
		t.Errorf("wrong size accepted")
	}
	unsized := &ManifestAsset{SHA256: "aa"}
	if err := unsized.Verify("aa", 999); err != nil {
		t.Errorf("size 0 in manifest must not be checked: %v", err)
	}
}
