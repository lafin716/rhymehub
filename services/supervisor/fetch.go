package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/lafin716/rhymehub/internal/signing"
)

// maxManifestBytes caps the feed manifest size.
const maxManifestBytes = 4 << 20

// openSource opens an http(s) URL, a file:// URL, or a plain filesystem path.
func openSource(ctx context.Context, client *http.Client, ref string) (io.ReadCloser, error) {
	switch {
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("GET %s: status %d", ref, resp.StatusCode)
		}
		return resp.Body, nil
	case strings.HasPrefix(ref, "file://"):
		return os.Open(strings.TrimPrefix(ref, "file://"))
	default:
		return os.Open(ref)
	}
}

// resolveRef resolves an artifact reference against the manifest location. Absolute URLs and
// absolute paths are returned unchanged.
func resolveRef(base, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "file://") || filepath.IsAbs(ref) {
		return ref, nil
	}
	switch {
	case strings.HasPrefix(base, "http://"), strings.HasPrefix(base, "https://"):
		b, err := url.Parse(base)
		if err != nil {
			return "", err
		}
		r, err := url.Parse(ref)
		if err != nil {
			return "", err
		}
		return b.ResolveReference(r).String(), nil
	case strings.HasPrefix(base, "file://"):
		return filepath.Join(filepath.Dir(strings.TrimPrefix(base, "file://")), ref), nil
	default:
		return filepath.Join(filepath.Dir(base), ref), nil
	}
}

// fetchManifest downloads and parses the feed manifest.
func fetchManifest(ctx context.Context, client *http.Client, feedURL string) (*signing.Manifest, error) {
	rc, err := openSource(ctx, client, feedURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return nil, errors.New("manifest exceeds size limit")
	}
	return signing.ParseManifest(data)
}

// downloadArtifact copies ref into dest and returns the sha256 hex digest of the bytes written.
// The copy is capped at maxBytes.
func downloadArtifact(ctx context.Context, client *http.Client, ref, dest string, maxBytes int64) (string, error) {
	rc, err := openSource(ctx, client, ref)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", ref, err)
	}
	defer rc.Close()
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(rc, maxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", ref, err)
	}
	if n > maxBytes {
		return "", fmt.Errorf("artifact exceeds %d bytes", maxBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
