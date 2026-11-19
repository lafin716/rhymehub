package release

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// version is a semantic version without prerelease or build metadata.
type version struct {
	major, minor, patch int
}

// parseVersion accepts "X.Y.Z" or "vX.Y.Z" where every part is a non-negative decimal integer.
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" {
			return version{}, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return version{}, false
			}
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		n[i] = v
	}
	return version{n[0], n[1], n[2]}, true
}

// String renders the version without a "v" prefix.
func (v version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// less reports whether v sorts before o.
func (v version) less(o version) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// bump returns the next version. Without a previous version the first release is 0.1.0. A "feature" change
// bumps the minor version and resets the patch; anything else bumps the patch.
func bump(latest version, found bool, changeType string) version {
	if !found {
		return version{0, 1, 0}
	}
	if strings.EqualFold(changeType, "feature") {
		return version{latest.major, latest.minor + 1, 0}
	}
	return version{latest.major, latest.minor, latest.patch + 1}
}

// latestTagVersion returns the highest valid vX.Y.Z tag in repo. Tags that do not parse are ignored.
func latestTagVersion(ctx context.Context, repo string) (version, bool, error) {
	tags, err := listVersionTags(ctx, repo)
	if err != nil {
		return version{}, false, err
	}
	var best version
	found := false
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok {
			continue
		}
		if !found || best.less(v) {
			best, found = v, true
		}
	}
	return best, found, nil
}

// latestTagBefore returns the highest vX.Y.Z tag strictly below before (a bare version), or "" when there is none.
// It is the baseline for release notes.
func latestTagBefore(ctx context.Context, repo, before string) (string, error) {
	limit, ok := parseVersion(before)
	if !ok {
		return "", nil
	}
	tags, err := listVersionTags(ctx, repo)
	if err != nil {
		return "", err
	}
	var best version
	bestName := ""
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || !v.less(limit) {
			continue
		}
		if bestName == "" || best.less(v) {
			best, bestName = v, t
		}
	}
	return bestName, nil
}

// listVersionTags returns the names of the v* tags in repo.
func listVersionTags(ctx context.Context, repo string) ([]string, error) {
	out, err := gitOut(ctx, repo, "tag", "--list", "v*")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			tags = append(tags, l)
		}
	}
	return tags, nil
}
