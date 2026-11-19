package release

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/signing"
)

// outcome is what a successful release strategy produced. It is copied onto the release row.
type outcome struct {
	tag       string
	notes     string
	log       string
	artifacts []domain.Artifact
}

// runStrategy runs the project's release strategy for rel. A strategy problem comes back as a *failure; an
// infrastructure error comes back as error.
func (m *Manager) runStrategy(ctx context.Context, p domain.Project, t domain.Task, rel domain.Release) (outcome, *failure, error) {
	switch p.ReleaseStrategy {
	case StrategyTag:
		return m.runTag(ctx, p, t, rel)
	case StrategyCommand:
		return m.runCommand(ctx, p, rel)
	case StrategySelf:
		return m.runSelf(ctx, p, rel)
	}
	return outcome{}, failf(ReasonReleaseFailed, "unknown release strategy %q", p.ReleaseStrategy), nil
}

// runTag creates an annotated tag vX.Y.Z at the merge commit with the release notes as the message. Nothing is
// pushed. A tag that already points at the merge commit (a retry) is reused.
func (m *Manager) runTag(ctx context.Context, p domain.Project, t domain.Task, rel domain.Release) (outcome, *failure, error) {
	repo := p.RepoPath
	tag := "v" + rel.Version
	head := t.HeadCommit
	if head == "" {
		// The merge was checked out on the base branch, so HEAD is the base tip.
		h, err := gitOut(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return outcome{}, nil, err
		}
		head = h
	}
	prev, err := latestTagBefore(ctx, repo, rel.Version)
	if err != nil {
		return outcome{}, nil, err
	}
	notes, err := releaseNotes(ctx, repo, prev, head)
	if err != nil {
		return outcome{}, nil, err
	}

	existing, exists, err := revParseQuiet(ctx, repo, "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return outcome{}, nil, err
	}
	if exists {
		if existing != head {
			return outcome{}, failf(ReasonReleaseFailed, "tag %s already exists at %s, not at the merge commit %s",
				tag, existing, head), nil
		}
		return outcome{tag: tag, notes: notes, log: "tag " + tag + " already exists at " + head}, nil, nil
	}

	msg := "Release " + tag
	if notes != "" {
		msg += "\n\n" + notes
	}
	if _, _, err := gitRun(ctx, repo, "tag", "-a", tag, "-m", msg, head); err != nil {
		return outcome{}, failf(ReasonReleaseFailed, "create tag %s: %v", tag, err), nil
	}
	return outcome{tag: tag, notes: notes, log: "created annotated tag " + tag + " at " + head}, nil, nil
}

// runCommand runs the Maintainer-configured release_cmd in the repository with the scrubbed environment. The
// command text comes from the project config only, never from request text.
func (m *Manager) runCommand(ctx context.Context, p domain.Project, rel domain.Release) (outcome, *failure, error) {
	out, err := m.run(ctx, p.RepoPath, scrubbedEnv(rel.Version), "sh", "-c", p.ReleaseCmd)
	log := truncate(string(out))
	if err != nil {
		return outcome{}, &failure{reason: ReasonReleaseFailed, msg: fmt.Sprintf("release command failed: %v", err),
			log: log}, nil
	}
	return outcome{tag: rel.Tag, log: log}, nil, nil
}

// runSelf builds the rhymehub release with scripts/build-release.sh, signs and verifies each tarball, moves the
// tarballs into the release directory and publishes them to the local feed. It does not trigger an update; that
// is a separate admin action through the supervisor.
func (m *Manager) runSelf(ctx context.Context, p domain.Project, rel domain.Release) (outcome, *failure, error) {
	priv, f := m.loadSigningKey()
	if f != nil {
		return outcome{}, f, nil
	}
	ver := rel.Version
	stage := filepath.Join(m.paths.ReleasesDir(), ".build", ver)
	if err := os.RemoveAll(stage); err != nil {
		return outcome{}, nil, err
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return outcome{}, nil, err
	}
	defer os.RemoveAll(stage)

	script := filepath.Join(p.RepoPath, filepath.FromSlash(selfBuildScript))
	out, err := m.run(ctx, p.RepoPath, scrubbedEnv(ver), script, ver, stage)
	log := truncate(string(out))
	if err != nil {
		return outcome{}, &failure{reason: ReasonReleaseFailed, msg: fmt.Sprintf("%s failed: %v", selfBuildScript, err),
			log: log}, nil
	}

	arts, f, err := m.collectArtifacts(stage, ver, priv)
	if err != nil {
		return outcome{}, nil, err
	}
	if f != nil {
		f.log = log
		return outcome{}, f, nil
	}
	for i := range arts {
		dst := filepath.Join(m.paths.ReleasesDir(), arts[i].Name)
		if err := os.Rename(arts[i].Path, dst); err != nil {
			return outcome{}, nil, fmt.Errorf("release: move artifact %s: %w", arts[i].Name, err)
		}
		arts[i].Path = dst
	}
	if err := m.publishFeed(ver, arts, m.now()); err != nil {
		return outcome{}, nil, err
	}
	return outcome{tag: rel.Tag, log: log, artifacts: arts}, nil, nil
}

// collectArtifacts finds the tarballs the build wrote for version ver, checks them against SHA256SUMS when that
// file is present, signs each one with priv and verifies the signature with the matching public key. Nothing is
// moved here: a failure leaves the stage directory for the caller to discard.
func (m *Manager) collectArtifacts(stage, ver string, priv ed25519.PrivateKey) ([]domain.Artifact, *failure, error) {
	paths, err := filepath.Glob(filepath.Join(stage, "rhymehub_"+ver+"_*.tar.gz"))
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, failf(ReasonReleaseFailed, "build produced no rhymehub_%s_*.tar.gz artifacts", ver), nil
	}
	sums, err := readSha256Sums(filepath.Join(stage, "SHA256SUMS"))
	if err != nil {
		return nil, failf(ReasonReleaseFailed, "invalid SHA256SUMS from build: %v", err), nil
	}
	pub := signing.PublicKeyOf(priv)

	var arts []domain.Artifact
	for _, path := range paths {
		name := filepath.Base(path)
		if _, _, ok := parseArtifactName(name, ver); !ok {
			return nil, failf(ReasonReleaseFailed, "unexpected artifact name %q", name), nil
		}
		digest, _, err := signing.SHA256File(path)
		if err != nil {
			return nil, nil, err
		}
		if want, ok := sums[name]; ok && want != digest {
			return nil, failf(ReasonReleaseFailed, "SHA256SUMS mismatch for %s", name), nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		signedDigest, sig, err := signing.SignFile(priv, path)
		if err != nil {
			return nil, nil, err
		}
		if signedDigest != digest {
			return nil, failf(ReasonReleaseFailed, "digest changed while signing %s", name), nil
		}
		if err := signing.VerifyFile(pub, path, digest, sig); err != nil {
			return nil, failf(ReasonReleaseFailed, "signature verification failed for %s: %v", name, err), nil
		}
		arts = append(arts, domain.Artifact{Name: name, Path: path, SHA256: digest, Size: info.Size(), Signature: sig})
	}
	return arts, nil, nil
}

// publishFeed records the artifacts in releases/manifest.json (docs/RELEASE_AND_UPDATE.md §2). Re-publishing the
// same version replaces its artifacts, so a retry is harmless. Latest only moves forward.
func (m *Manager) publishFeed(ver string, arts []domain.Artifact, at time.Time) error {
	man, err := signing.LoadManifest(m.feedPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		man = &signing.Manifest{}
	case err != nil:
		return err
	}
	if man.Name == "" {
		man.Name = feedName
	}
	rel := signing.Release{
		Version:       ver,
		PublishedAt:   at.UTC().Format(time.RFC3339),
		SchemaVersion: feedSchemaVersion,
	}
	for _, a := range arts {
		osName, arch, _ := parseArtifactName(a.Name, ver)
		man.Upsert(rel, signing.Artifact{OS: osName, Arch: arch, URL: a.Name, SHA256: a.SHA256, Signature: a.Signature})
	}
	if cur, ok := parseVersion(man.Latest); !ok {
		man.Latest = ver
	} else if want, ok := parseVersion(ver); ok && cur.less(want) {
		man.Latest = ver
	}
	return signing.WriteManifest(m.feedPath(), man)
}

// parseArtifactName splits rhymehub_<ver>_<os>_<arch>.tar.gz into its os and arch.
func parseArtifactName(name, ver string) (string, string, bool) {
	prefix := "rhymehub_" + ver + "_"
	const suffix = ".tar.gz"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", "", false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	goos, arch, ok := strings.Cut(mid, "_")
	if !ok || goos == "" || arch == "" || strings.Contains(arch, "_") {
		return "", "", false
	}
	return goos, arch, true
}

// readSha256Sums parses a sha256sum-style file ("<hex>  <name>" per line). A missing file is not an error.
func readSha256Sums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed line %q", line)
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return sums, nil
}

// releaseNotes lists the non-merge commit subjects in prev..head as "- subject" lines. With no previous tag it
// lists the most recent 200 commits reachable from head.
func releaseNotes(ctx context.Context, repo, prev, head string) (string, error) {
	args := []string{"log", "--no-merges", "--format=%s"}
	if prev != "" {
		args = append(args, "refs/tags/"+prev+".."+head)
	} else {
		args = append(args, "-n", "200", head)
	}
	out, err := gitOut(ctx, repo, args...)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, "- "+l)
		}
	}
	return strings.Join(lines, "\n"), nil
}
