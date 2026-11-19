package supervisor

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lafin716/rhymehub/internal/signing"
)

// Bootstrap prepares a RHYMEHUB_HOME: directory layout, API token, and a default config.json when
// none exists. It never overwrites an existing config.
func Bootstrap(h Home) error {
	if h.Root == "" {
		return errors.New("bootstrap: home is required")
	}
	if err := h.EnsureLayout(); err != nil {
		return err
	}
	if _, err := ensureToken(h); err != nil {
		return fmt.Errorf("bootstrap token: %w", err)
	}
	if _, err := os.Stat(h.ConfigPath()); errors.Is(err, os.ErrNotExist) {
		data, err := json.MarshalIndent(CoreConfig{Listen: DefaultCoreListen}, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFileAtomic(h.ConfigPath(), append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("bootstrap config: %w", err)
		}
	} else if err != nil {
		return err
	}
	return nil
}

// Keygen creates the release signing key pair: the private key under secrets/ (0600) and the
// public key under keys/ (0644). It refuses to overwrite existing keys.
func Keygen(h Home) (ed25519.PublicKey, error) {
	if h.Root == "" {
		return nil, errors.New("keygen: home is required")
	}
	if err := h.EnsureLayout(); err != nil {
		return nil, err
	}
	return signing.GenerateKeyPair(h.PrivateKeyPath(), h.PublicKeyPath())
}

// SignReleaseOptions describes one artifact to sign and record in the feed manifest.
type SignReleaseOptions struct {
	Home          Home
	Artifact      string // path to the tarball
	Version       string
	OS            string
	Arch          string
	URL           string // artifact URL as published; may be relative to the manifest
	Manifest      string // manifest path; created when missing
	KeyPath       string // private key; defaults to secrets/release-signing.key
	Name          string // product name for a new manifest; defaults to "rhymehub"
	PublishedAt   string
	Notes         string
	SchemaVersion int
}

// SignResult reports what was signed and where it was recorded.
type SignResult struct {
	Digest    string
	Signature string
	Manifest  string
}

// SignRelease hashes and signs an artifact, then upserts it into the manifest and writes the
// manifest atomically. The manifest's latest version is set to this release.
func SignRelease(opts SignReleaseOptions) (SignResult, error) {
	if opts.Version == "" || opts.Artifact == "" || opts.OS == "" || opts.Arch == "" || opts.URL == "" || opts.Manifest == "" {
		return SignResult{}, errors.New("sign-release: version, artifact, os, arch, url and manifest are required")
	}
	if !filepath.IsAbs(opts.Manifest) {
		abs, err := filepath.Abs(opts.Manifest)
		if err != nil {
			return SignResult{}, err
		}
		opts.Manifest = abs
	}
	keyPath := opts.KeyPath
	if keyPath == "" {
		keyPath = opts.Home.PrivateKeyPath()
	}
	priv, err := signing.LoadPrivateKey(keyPath)
	if err != nil {
		return SignResult{}, fmt.Errorf("sign-release: load private key: %w", err)
	}
	digest, sig, err := signing.SignFile(priv, opts.Artifact)
	if err != nil {
		return SignResult{}, fmt.Errorf("sign-release: sign artifact: %w", err)
	}
	m, err := loadOrNewManifest(opts.Manifest, opts.Name)
	if err != nil {
		return SignResult{}, err
	}
	m.Upsert(
		signing.Release{Version: opts.Version, PublishedAt: opts.PublishedAt, Notes: opts.Notes, SchemaVersion: opts.SchemaVersion},
		signing.Artifact{OS: opts.OS, Arch: opts.Arch, URL: opts.URL, SHA256: digest, Signature: sig},
	)
	m.Latest = opts.Version
	if err := signing.WriteManifest(opts.Manifest, m); err != nil {
		return SignResult{}, fmt.Errorf("sign-release: write manifest: %w", err)
	}
	return SignResult{Digest: digest, Signature: sig, Manifest: opts.Manifest}, nil
}

// loadOrNewManifest reads an existing manifest, or starts an empty one when the file is missing.
func loadOrNewManifest(path, name string) (*signing.Manifest, error) {
	m, err := signing.LoadManifest(path)
	if errors.Is(err, os.ErrNotExist) {
		if name == "" {
			name = "rhymehub"
		}
		return &signing.Manifest{Name: name}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sign-release: load manifest: %w", err)
	}
	return m, nil
}
