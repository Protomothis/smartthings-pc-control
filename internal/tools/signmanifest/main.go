// Command signmanifest produces and checks the signed update manifest that
// every release of smartthings-pc-control carries (issue #66). It is run by
// .github/workflows/release.yml and by hand for key rotation.
//
//	signmanifest genkey
//	    Prints seed=<base64> and pub=<base64> for a fresh ed25519 keypair.
//	    The seed goes into the UPDATE_SIGNING_KEY GitHub secret (and the
//	    maintainer's backup); the pub is embedded in internal/release.
//
//	signmanifest sign -key <base64 seed | env:NAME> -version <tag>
//	    [-min-version <tag>] [-arch amd64] [-out update.json] <asset files...>
//	    Hashes each asset and writes update.json plus update.json.sig.
//
//	signmanifest verify [-pub <base64>] update.json update.json.sig
//	    Verifies the pair (default key: the embedded production key) and
//	    prints the manifest. Exit status 1 on any failure.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "signmanifest:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage: signmanifest genkey | sign -key <seed|env:NAME> -version <tag> [-min-version <tag>] [-arch <arch>] [-out update.json] <files...> | verify [-pub <base64>] update.json update.json.sig")

// run dispatches the subcommand; tests call it directly.
func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "genkey":
		return genkey(stdout)
	case "sign":
		return sign(args[1:], stdout)
	case "verify":
		return verify(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q\n%w", args[0], errUsage)
	}
}

func genkey(stdout io.Writer) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "seed=%s\n", base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Fprintf(stdout, "pub=%s\n", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func sign(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	key := fs.String("key", "", "base64 ed25519 seed, or env:NAME to read it from that environment variable")
	version := fs.String("version", "", "release tag the manifest is for (v1.2.3)")
	minVersion := fs.String("min-version", "", "oldest installed version allowed to auto-update (optional)")
	arch := fs.String("arch", "", "GOARCH the assets are built for; empty matches any")
	out := fs.String("out", release.ManifestName, "manifest path; the signature is written next to it as <out>.sig")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v\n%w", err, errUsage)
	}
	files := fs.Args()
	switch {
	case *key == "":
		return fmt.Errorf("-key is required\n%w", errUsage)
	case *version == "":
		return fmt.Errorf("-version is required\n%w", errUsage)
	case len(files) == 0:
		return fmt.Errorf("at least one asset file is required\n%w", errUsage)
	}
	if _, ok := release.ParseVersion(*version); !ok {
		return fmt.Errorf("-version %q is not a release tag like v1.2.3", *version)
	}
	if *minVersion != "" {
		if _, ok := release.ParseVersion(*minVersion); !ok {
			return fmt.Errorf("-min-version %q is not a release tag like v1.2.3", *minVersion)
		}
	}
	priv, err := loadSeed(*key)
	if err != nil {
		return err
	}

	m := &release.Manifest{
		Version:     strings.TrimSpace(*version),
		MinVersion:  strings.TrimSpace(*minVersion),
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, f := range files {
		a, err := hashAsset(f, *arch)
		if err != nil {
			return err
		}
		m.Assets = append(m.Assets, a)
	}
	data, err := release.EncodeManifest(m)
	if err != nil {
		return err
	}
	sig := release.EncodeSignature(release.Sign(data, priv))
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(*out+".sig", sig, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s and %s.sig (%d asset(s), version %s, pub=%s)\n", *out, *out, len(m.Assets), m.Version,
		base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

func verify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pubB64 := fs.String("pub", release.PublicKeyBase64, "base64 ed25519 public key (default: embedded production key)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v\n%w", err, errUsage)
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("verify needs exactly the manifest and its .sig\n%w", errUsage)
	}
	pubRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*pubB64))
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return fmt.Errorf("-pub is not a base64 %d-byte ed25519 public key", ed25519.PublicKeySize)
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(fs.Arg(1))
	if err != nil {
		return err
	}
	m, err := release.VerifyManifest(data, sig, ed25519.PublicKey(pubRaw))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "OK: %s signed manifest for %s", fs.Arg(0), m.Version)
	if m.MinVersion != "" {
		fmt.Fprintf(stdout, " (min_version %s)", m.MinVersion)
	}
	fmt.Fprintln(stdout)
	stdout.Write(data)
	return nil
}

// loadSeed turns "-key" into a private key: either the base64 seed itself
// or env:NAME naming an environment variable holding it (so the secret never
// appears on a command line or in CI logs).
func loadSeed(spec string) (ed25519.PrivateKey, error) {
	if name, ok := strings.CutPrefix(spec, "env:"); ok {
		spec = os.Getenv(name)
		if strings.TrimSpace(spec) == "" {
			return nil, fmt.Errorf("environment variable %s is empty or unset", name)
		}
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spec))
	if err != nil {
		return nil, fmt.Errorf("signing key is not valid base64: %v", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key decodes to %d bytes, want a %d-byte ed25519 seed", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// hashAsset computes the manifest entry for one file.
func hashAsset(path, arch string) (release.ManifestAsset, error) {
	f, err := os.Open(path)
	if err != nil {
		return release.ManifestAsset{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return release.ManifestAsset{}, err
	}
	if n == 0 {
		return release.ManifestAsset{}, fmt.Errorf("%s is empty", path)
	}
	return release.ManifestAsset{
		Name:   filepath.Base(path),
		SHA256: hex.EncodeToString(h.Sum(nil)),
		Arch:   strings.TrimSpace(arch),
		Size:   n,
	}, nil
}
