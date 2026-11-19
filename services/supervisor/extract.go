package supervisor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// verifyTimeout bounds the `rhymehub version` self-check of an extracted binary.
const verifyTimeout = 10 * time.Second

// safeExtract unpacks a .tar.gz into dest. Only regular files and directories are accepted;
// absolute names, `..` segments, links and devices are rejected. Total uncompressed size is
// capped at maxBytes.
func safeExtract(tarball, dest string, maxBytes int64) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		target, err := safeTarget(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxBytes {
				return fmt.Errorf("archive exceeds %d bytes", maxBytes)
			}
			if err := writeExtracted(target, tr, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry %q (type %q)", hdr.Name, hdr.Typeflag)
		}
	}
}

// safeTarget maps an archive member name to a path under dest, rejecting escapes.
func safeTarget(dest, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	for _, seg := range strings.Split(filepath.ToSlash(name), "/") {
		if seg == ".." {
			return "", fmt.Errorf("unsafe archive path %q", name)
		}
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	rel, err := filepath.Rel(dest, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

// writeExtracted writes one regular file. Permissions keep the archive's exec bits but always
// include owner read/write.
func writeExtracted(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	perm := (mode & 0o755) | 0o600
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// installVersion extracts tarball into versions/<version>, checking that the bundled core
// reports exactly that version. The final directory is only created by an atomic rename, so a
// failed install never leaves a half-written version behind.
func installVersion(ctx context.Context, h Home, version, tarball string, maxBytes int64) error {
	if version == "" || strings.ContainsAny(version, `/\`) || version == "." || version == ".." {
		return fmt.Errorf("invalid version %q", version)
	}
	if currentVersion(h) == version {
		return fmt.Errorf("version %s is already current", version)
	}
	final := h.VersionDir(version)
	tmp := final + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := safeExtract(tarball, tmp, maxBytes); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	bin := filepath.Join(tmp, "bin", "rhymehub")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("archive has no bin/rhymehub: %w", err)
	}
	if err := verifyCoreBinary(ctx, bin, version, scrubbedEnv(h)); err != nil {
		return err
	}
	// A previous install of this version that is not the current one is replaced.
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	ok = true
	return syncDir(h.VersionsDir())
}

// verifyCoreBinary runs `<bin> version` and requires the last field of its output to equal the
// expected version.
func verifyCoreBinary(ctx context.Context, bin, version string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("verify %s: %w", bin, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || fields[len(fields)-1] != version {
		return fmt.Errorf("verify %s: reported %q, want version %s", bin, strings.TrimSpace(string(out)), version)
	}
	return nil
}
