package signing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest is the release feed document (docs/RELEASE_AND_UPDATE.md §2).
type Manifest struct {
	Name     string    `json:"name"`
	Latest   string    `json:"latest"`
	Releases []Release `json:"releases"`
}

// Release is one published version in the feed.
type Release struct {
	Version       string     `json:"version"`
	PublishedAt   string     `json:"publishedAt,omitempty"`
	Notes         string     `json:"notes,omitempty"`
	SchemaVersion int        `json:"schemaVersion,omitempty"`
	Artifacts     []Artifact `json:"artifacts"`
}

// Artifact is one downloadable tarball for an os/arch pair. URL may be relative to the manifest.
type Artifact struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature,omitempty"`
}

// ParseManifest decodes a manifest document.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("signing: parse manifest: %w", err)
	}
	return &m, nil
}

// LoadManifest reads and decodes a manifest file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// FindRelease returns the release with the given version.
func (m *Manifest) FindRelease(version string) (Release, bool) {
	for _, r := range m.Releases {
		if r.Version == version {
			return r, true
		}
	}
	return Release{}, false
}

// ArtifactFor returns the release and artifact matching version and os/arch.
func (m *Manifest) ArtifactFor(version, goos, goarch string) (Release, Artifact, error) {
	rel, ok := m.FindRelease(version)
	if !ok {
		return Release{}, Artifact{}, fmt.Errorf("signing: version %q not in manifest", version)
	}
	for _, a := range rel.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return rel, a, nil
		}
	}
	return Release{}, Artifact{}, fmt.Errorf("signing: no artifact for %s/%s in version %q", goos, goarch, version)
}

// Upsert records a release artifact. The release is created when missing; an artifact with
// the same os/arch is replaced. Non-empty release metadata in rel overrides the stored one.
func (m *Manifest) Upsert(rel Release, art Artifact) {
	idx := -1
	for i := range m.Releases {
		if m.Releases[i].Version == rel.Version {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.Releases = append(m.Releases, Release{Version: rel.Version})
		idx = len(m.Releases) - 1
	}
	r := &m.Releases[idx]
	if rel.PublishedAt != "" {
		r.PublishedAt = rel.PublishedAt
	}
	if rel.Notes != "" {
		r.Notes = rel.Notes
	}
	if rel.SchemaVersion != 0 {
		r.SchemaVersion = rel.SchemaVersion
	}
	replaced := false
	for i := range r.Artifacts {
		if r.Artifacts[i].OS == art.OS && r.Artifacts[i].Arch == art.Arch {
			r.Artifacts[i] = art
			replaced = true
			break
		}
	}
	if !replaced {
		r.Artifacts = append(r.Artifacts, art)
	}
}

// WriteManifest writes the manifest atomically (temp file + rename) with indentation.
func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
