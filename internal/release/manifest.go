package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Signed update manifest (#66).
//
// Every release carries update.json (what to download, its SHA-256, which
// version it is) and update.json.sig (an ed25519 signature over the exact
// bytes of update.json). The GUI verifies the signature with PublicKey
// before it trusts anything in the manifest, downloads the asset the
// manifest names and compares the hash before the file is ever executed.
// The signing seed lives only in the UPDATE_SIGNING_KEY GitHub secret; see
// docs/design/update-signing.md and internal/tools/signmanifest.

// ManifestName and ManifestSigName are the release asset names.
const (
	ManifestName    = "update.json"
	ManifestSigName = "update.json.sig"
)

// PublicKeyBase64 is the production verification key (standard base64 of
// the 32-byte ed25519 public key). Rotating the key means embedding the new
// public key here and shipping a release; older apps then refuse
// auto-update and fall back to the release page.
const PublicKeyBase64 = "iCD0rkk0Bgo6AAZsFXOiKYGWiPd0SMVeFl2D+l/4vNc="

// PublicKey is PublicKeyBase64 decoded at package init. It is nil when the
// constant is malformed — VerifyManifest then rejects everything instead of
// panicking, and TestPublicKey fails loudly.
var PublicKey = decodePublicKey(PublicKeyBase64)

func decodePublicKey(s string) ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(b)
}

// Size limits for the two manifest assets; anything larger is not ours.
const (
	maxManifestSize = 64 * 1024
	maxSigSize      = 1024
)

var (
	// ErrNoManifest: the release has no update.json / update.json.sig pair
	// (a release made before signing existed, or a hand-made one). The GUI
	// falls back to opening the release page.
	ErrNoManifest = errors.New("release has no signed update manifest")
	// ErrBadSignature: the manifest is present but does not verify, is
	// malformed, or names a different release. Never install from it.
	ErrBadSignature = errors.New("update manifest signature is invalid")
)

// Manifest describes one release's installable assets.
type Manifest struct {
	// Version is the release tag (v1.2.3); must equal the GitHub tag it is
	// attached to, so a manifest cannot be replayed onto another release.
	Version string `json:"version"`
	// MinVersion, when set, is the oldest installed version that may
	// auto-update to this one (e.g. after an incompatible updater change).
	MinVersion string `json:"min_version,omitempty"`
	// PublishedAt is RFC 3339 UTC, informational.
	PublishedAt string          `json:"published_at"`
	Assets      []ManifestAsset `json:"assets"`
}

// ManifestAsset is one downloadable file listed in the manifest.
type ManifestAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	// Arch is "amd64"/"arm64" (runtime.GOARCH); empty matches any arch.
	Arch string `json:"arch,omitempty"`
	Size int64  `json:"size"`
}

// EncodeManifest renders m as the canonical bytes that get signed and
// uploaded: indented JSON with a trailing newline.
func EncodeManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Sign returns the raw 64-byte ed25519 signature over data. The .sig asset
// stores it as standard base64 text (see EncodeSignature).
func Sign(data []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, data)
}

// EncodeSignature renders a raw signature as the text of the .sig asset.
func EncodeSignature(sig []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// decodeSignature accepts the raw 64 bytes or their standard-base64 text
// (surrounding whitespace ignored).
func decodeSignature(sig []byte) ([]byte, error) {
	if len(sig) == ed25519.SignatureSize {
		return sig, nil
	}
	txt := strings.TrimSpace(string(sig))
	raw, err := base64.StdEncoding.DecodeString(txt)
	if err != nil {
		return nil, fmt.Errorf("signature is neither %d raw bytes nor base64: %w", ed25519.SignatureSize, err)
	}
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(raw), ed25519.SignatureSize)
	}
	return raw, nil
}

// VerifyManifest checks sig over the exact bytes of data with pub and only
// then decodes the manifest. A manifest without a version or without
// assets is rejected too. All failures wrap ErrBadSignature.
func VerifyManifest(data, sig []byte, pub ed25519.PublicKey) (*Manifest, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key is %d bytes, want %d", ErrBadSignature, len(pub), ed25519.PublicKeySize)
	}
	raw, err := decodeSignature(sig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if !ed25519.Verify(pub, data, raw) {
		return nil, ErrBadSignature
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest is not valid JSON: %v", ErrBadSignature, err)
	}
	m.Version = strings.TrimSpace(m.Version)
	if m.Version == "" {
		return nil, fmt.Errorf("%w: manifest has no version", ErrBadSignature)
	}
	if len(m.Assets) == 0 {
		return nil, fmt.Errorf("%w: manifest lists no assets", ErrBadSignature)
	}
	for i, a := range m.Assets {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.SHA256) == "" {
			return nil, fmt.Errorf("%w: asset %d lacks name or sha256", ErrBadSignature, i)
		}
	}
	return &m, nil
}

// FetchManifest downloads update.json and update.json.sig from rel's
// assets, verifies them with PublicKey and checks that the manifest is for
// rel.TagName. ErrNoManifest when either asset is missing; ErrBadSignature
// (wrapped) when verification fails or the versions differ.
func FetchManifest(ctx context.Context, client *http.Client, rel *Info) (*Manifest, error) {
	return fetchManifest(ctx, client, rel, PublicKey)
}

func fetchManifest(ctx context.Context, client *http.Client, rel *Info, pub ed25519.PublicKey) (*Manifest, error) {
	if rel == nil {
		return nil, ErrNoManifest
	}
	manURL := AssetURL(rel, ManifestName)
	sigURL := AssetURL(rel, ManifestSigName)
	if manURL == "" || sigURL == "" {
		return nil, ErrNoManifest
	}
	data, err := download(ctx, client, manURL, maxManifestSize)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	sig, err := download(ctx, client, sigURL, maxSigSize)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestSigName, err)
	}
	m, err := VerifyManifest(data, sig, pub)
	if err != nil {
		return nil, err
	}
	if m.Version != strings.TrimSpace(rel.TagName) {
		return nil, fmt.Errorf("%w: manifest is for %s, release is %s", ErrBadSignature, m.Version, rel.TagName)
	}
	return m, nil
}

// download fetches url and returns at most max bytes, failing when the body
// is larger than that.
func download(ctx context.Context, client *http.Client, url string, max int64) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("body exceeds %d bytes", max)
	}
	return buf.Bytes(), nil
}

// AssetURL returns the download URL of the release asset called name
// (exact match), or "" when the release has no such asset.
func AssetURL(rel *Info, name string) string {
	if rel == nil {
		return ""
	}
	for _, a := range rel.Assets {
		if a.Name == name && a.DownloadURL != "" {
			return a.DownloadURL
		}
	}
	return ""
}

// AssetFor picks the manifest asset for arch (runtime.GOARCH). An asset
// whose Arch matches wins; one with an empty Arch matches any arch.
func (m *Manifest) AssetFor(arch string) (*ManifestAsset, bool) {
	if m == nil {
		return nil, false
	}
	var wild *ManifestAsset
	for i := range m.Assets {
		a := &m.Assets[i]
		switch {
		case strings.EqualFold(a.Arch, arch):
			return a, true
		case a.Arch == "" && wild == nil:
			wild = a
		}
	}
	if wild != nil {
		return wild, true
	}
	return nil, false
}

// Allows reports whether an installation running current may auto-update
// to this manifest's release: false only when MinVersion is set and current
// is a release version older than it. Dev builds ("dev", "") get true.
func (m *Manifest) Allows(current string) bool {
	if m == nil || strings.TrimSpace(m.MinVersion) == "" {
		return true
	}
	if _, ok := ParseVersion(current); !ok {
		return true
	}
	return !IsNewer(current, m.MinVersion)
}

// Verify compares a downloaded file's SHA-256 (hex, any case) and size with
// the asset's; Size 0 in the manifest means "not checked".
func (a *ManifestAsset) Verify(sumHex string, size int64) error {
	if !strings.EqualFold(strings.TrimSpace(sumHex), strings.TrimSpace(a.SHA256)) {
		return fmt.Errorf("sha256 %s does not match manifest %s", sumHex, a.SHA256)
	}
	if a.Size > 0 && size != a.Size {
		return fmt.Errorf("size %d does not match manifest %d", size, a.Size)
	}
	return nil
}
