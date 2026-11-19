// Package signing provides ed25519 release signing and sha256 helpers shared by
// the release manager (core) and the supervisor.
//
// Signature format (docs/RELEASE_AND_UPDATE.md §2): the signed message is the
// lowercase hex sha256 digest of the artifact (ASCII bytes), and the signature is
// base64 (StdEncoding) of the 64-byte ed25519 signature.
//
// On-disk key formats:
//   - private key (secrets/release-signing.key, 0600): base64 of the 64-byte ed25519 private key
//   - public key (keys/release-signing.pub, 0644): base64 of the 32-byte ed25519 public key
package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PrivateKeyFileName is the release signing private key, stored under secrets/.
const PrivateKeyFileName = "release-signing.key"

// PublicKeyFileName is the trusted release public key, stored under keys/.
const PublicKeyFileName = "release-signing.pub"

var (
	// ErrDigestMismatch means the file's sha256 does not match the expected digest.
	ErrDigestMismatch = errors.New("signing: sha256 digest mismatch")
	// ErrBadSignature means the ed25519 signature does not verify.
	ErrBadSignature = errors.New("signing: bad signature")
	// ErrBadKey means a key file or string is malformed.
	ErrBadKey = errors.New("signing: malformed key")
	// ErrBadDigest means a digest string is not 64 lowercase hex characters.
	ErrBadDigest = errors.New("signing: malformed sha256 digest")
	// ErrKeyExists means GenerateKeyPair refused to overwrite an existing key file.
	ErrKeyExists = errors.New("signing: key file already exists")
	// ErrKeyPermissions means the private key file is readable by group or others.
	ErrKeyPermissions = errors.New("signing: private key must not be group/world accessible")
)

// GenerateKeyPair creates a new ed25519 key pair. The private key is written with
// mode 0600 and the public key with 0644. Existing files are never overwritten.
func GenerateKeyPair(privPath, pubPath string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("signing: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(privPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(pubPath), 0o755); err != nil {
		return nil, err
	}
	if err := writeNewFile(privPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := writeNewFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		_ = os.Remove(privPath) // do not leave an unpaired private key behind
		return nil, err
	}
	return pub, nil
}

func writeNewFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrKeyExists, path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// LoadPrivateKey reads a private key file. It refuses files accessible to group or others.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s", ErrKeyPermissions, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: private key %s", ErrBadKey, path)
	}
	return ed25519.PrivateKey(raw), nil
}

// LoadPublicKey reads a public key file.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePublicKey(string(data))
}

// ParsePublicKey decodes a base64 encoded ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrBadKey
	}
	return ed25519.PublicKey(raw), nil
}

// PublicKeyOf returns the public half of a private key.
func PublicKeyOf(priv ed25519.PrivateKey) ed25519.PublicKey {
	return priv.Public().(ed25519.PublicKey)
}

// SHA256File returns the lowercase hex sha256 of a file and its size.
func SHA256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return SHA256Reader(f)
}

// SHA256Reader returns the lowercase hex sha256 of everything read and the byte count.
func SHA256Reader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ValidDigest reports whether s is a 64-character lowercase hex sha256 digest.
func ValidDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// SignDigest signs a sha256 hex digest and returns the base64 signature.
func SignDigest(priv ed25519.PrivateKey, digest string) (string, error) {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !ValidDigest(digest) {
		return "", ErrBadDigest
	}
	if len(priv) != ed25519.PrivateKeySize {
		return "", ErrBadKey
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(digest))), nil
}

// VerifyDigest checks a base64 signature over a sha256 hex digest.
func VerifyDigest(pub ed25519.PublicKey, digest, signature string) error {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !ValidDigest(digest) {
		return ErrBadDigest
	}
	if len(pub) != ed25519.PublicKeySize {
		return ErrBadKey
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, []byte(digest), sig) {
		return ErrBadSignature
	}
	return nil
}

// SignFile hashes a file and signs its digest. It returns the digest and signature.
func SignFile(priv ed25519.PrivateKey, path string) (digest, signature string, err error) {
	digest, _, err = SHA256File(path)
	if err != nil {
		return "", "", err
	}
	signature, err = SignDigest(priv, digest)
	if err != nil {
		return "", "", err
	}
	return digest, signature, nil
}

// VerifyFile checks that the file's sha256 equals digest and that signature is a valid
// ed25519 signature of that digest. Both checks are required.
func VerifyFile(pub ed25519.PublicKey, path, digest, signature string) error {
	got, _, err := SHA256File(path)
	if err != nil {
		return err
	}
	if got != strings.ToLower(strings.TrimSpace(digest)) {
		return ErrDigestMismatch
	}
	return VerifyDigest(pub, got, signature)
}
