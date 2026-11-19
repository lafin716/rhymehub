package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGenerateKeyPairWritesModesAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "secrets", PrivateKeyFileName)
	pub := filepath.Join(dir, "keys", PublicKeyFileName)

	pk, err := GenerateKeyPair(priv, pub)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	info, err := os.Stat(priv)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o, want 600", info.Mode().Perm())
	}
	loadedPub, err := LoadPublicKey(pub)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}
	if !pk.Equal(loadedPub) {
		t.Fatal("returned public key differs from file")
	}
	loadedPriv, err := LoadPrivateKey(priv)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	if !PublicKeyOf(loadedPriv).Equal(loadedPub) {
		t.Fatal("private key does not match public key file")
	}

	if _, err := GenerateKeyPair(priv, filepath.Join(dir, "other.pub")); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("overwrite err = %v, want ErrKeyExists", err)
	}
}

func TestLoadPrivateKeyRejectsWorldReadable(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, PrivateKeyFileName)
	if _, err := GenerateKeyPair(priv, filepath.Join(dir, PublicKeyFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(priv, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivateKey(priv); !errors.Is(err, ErrKeyPermissions) {
		t.Fatalf("err = %v, want ErrKeyPermissions", err)
	}
}

func TestSignVerifyDigestRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("artifact bytes"))
	digest := hex.EncodeToString(sum[:])

	sig, err := SignDigest(priv, digest)
	if err != nil {
		t.Fatalf("SignDigest: %v", err)
	}
	if err := VerifyDigest(pub, digest, sig); err != nil {
		t.Fatalf("VerifyDigest valid sig: %v", err)
	}
	// Uppercase digest input is normalized.
	if err := VerifyDigest(pub, strings.ToUpper(digest), sig); err != nil {
		t.Fatalf("VerifyDigest uppercase: %v", err)
	}

	other := strings.Repeat("0", 64)
	if err := VerifyDigest(pub, other, sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong digest err = %v, want ErrBadSignature", err)
	}

	_, otherPriv, _ := ed25519.GenerateKey(nil)
	forged, _ := SignDigest(otherPriv, digest)
	if err := VerifyDigest(pub, digest, forged); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("foreign key err = %v, want ErrBadSignature", err)
	}
	if err := VerifyDigest(pub, digest, "not-base64!!"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("garbage sig err = %v, want ErrBadSignature", err)
	}
	if _, err := SignDigest(priv, "xyz"); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("bad digest sign err = %v, want ErrBadDigest", err)
	}
}

func TestSignVerifyFile(t *testing.T) {
	dir := t.TempDir()
	pubPath := filepath.Join(dir, PublicKeyFileName)
	privPath := filepath.Join(dir, PrivateKeyFileName)
	pub, err := GenerateKeyPair(privPath, pubPath)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact := writeTemp(t, "rhymehub.tar.gz", []byte("payload"))

	digest, sig, err := SignFile(priv, artifact)
	if err != nil {
		t.Fatalf("SignFile: %v", err)
	}
	want := sha256.Sum256([]byte("payload"))
	if digest != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %s", digest)
	}
	if err := VerifyFile(pub, artifact, digest, sig); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}

	// Tampered content must fail the digest check.
	if err := os.WriteFile(artifact, []byte("payloaD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(pub, artifact, digest, sig); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("tampered err = %v, want ErrDigestMismatch", err)
	}
}

func TestSHA256FileAndReader(t *testing.T) {
	p := writeTemp(t, "f", []byte("abc"))
	got, n, err := SHA256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("size = %d", n)
	}
	// Known sha256("abc").
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("sha = %s", got)
	}
	if !ValidDigest(got) || ValidDigest("ABC") {
		t.Fatal("ValidDigest misbehaves")
	}
}

func TestManifestRoundTripAndLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	m := &Manifest{Name: "rhymehub"}
	m.Upsert(Release{Version: "0.1.0", SchemaVersion: 1}, Artifact{OS: "darwin", Arch: "arm64", URL: "a.tar.gz", SHA256: strings.Repeat("a", 64)})
	m.Upsert(Release{Version: "0.1.0"}, Artifact{OS: "darwin", Arch: "amd64", URL: "b.tar.gz", SHA256: strings.Repeat("b", 64)})
	m.Upsert(Release{Version: "0.1.0"}, Artifact{OS: "darwin", Arch: "arm64", URL: "a2.tar.gz", SHA256: strings.Repeat("c", 64)})
	m.Latest = "0.1.0"
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Latest != "0.1.0" || len(loaded.Releases) != 1 || len(loaded.Releases[0].Artifacts) != 2 {
		t.Fatalf("unexpected manifest: %+v", loaded)
	}
	if loaded.Releases[0].SchemaVersion != 1 {
		t.Fatal("schemaVersion lost")
	}
	_, art, err := loaded.ArtifactFor("0.1.0", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if art.URL != "a2.tar.gz" {
		t.Fatalf("artifact not replaced: %+v", art)
	}
	if _, _, err := loaded.ArtifactFor("0.1.0", "linux", "amd64"); err == nil {
		t.Fatal("expected missing artifact error")
	}
	if _, _, err := loaded.ArtifactFor("9.9.9", "darwin", "arm64"); err == nil {
		t.Fatal("expected missing version error")
	}
}
