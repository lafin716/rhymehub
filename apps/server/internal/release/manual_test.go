package release

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/internal/domain"
)

func TestReleaseProjectRejectsNoneStrategy(t *testing.T) {
	f := newFixture(t, StrategyNone, "", nil)
	_, err := f.mgr.ReleaseProject(f.ctx, f.prj.ID, "", "", "usr_1")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("strategy none: want ErrInvalid, got %v", err)
	}
	rels, err := f.st.ListReleases(f.ctx, f.prj.ID)
	if err != nil || len(rels) != 0 {
		t.Fatalf("no release row expected, got %d (%v)", len(rels), err)
	}
}

func TestReleaseProjectRejectsBadVersion(t *testing.T) {
	f := newFixture(t, StrategyTag, "", nil)
	for _, v := range []string{"1.2", "a.b.c", "1.2.3.4"} {
		if _, err := f.mgr.ReleaseProject(f.ctx, f.prj.ID, v, "", "usr_1"); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("version %q: want ErrInvalid, got %v", v, err)
		}
	}
}

func TestReleaseProjectTagStrategyComputesVersion(t *testing.T) {
	f := newFixture(t, StrategyTag, "", nil)
	rel, err := f.mgr.ReleaseProject(f.ctx, f.prj.ID, "", "hand written notes", "usr_1")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if rel.Version != "0.1.0" || rel.Tag != "v0.1.0" || rel.Status != domain.ReleasePublished {
		t.Fatalf("release = %+v", rel)
	}
	if rel.Notes != "hand written notes" || rel.CreatedBy != "usr_1" {
		t.Fatalf("notes/creator not kept: %+v", rel)
	}
	if got := runIn(t, f.repo, "tag", "--list", "v0.1.0"); got != "v0.1.0" {
		t.Fatalf("git tag v0.1.0 not created, list = %q", got)
	}

	// The next release without an explicit version bumps the patch from the release rows.
	rel2, err := f.mgr.ReleaseProject(f.ctx, f.prj.ID, "", "", "usr_1")
	if err != nil {
		t.Fatalf("second release: %v", err)
	}
	if rel2.Version != "0.1.1" {
		t.Fatalf("second version = %s, want 0.1.1", rel2.Version)
	}
}

func TestReleaseProjectExplicitVersionAndAudit(t *testing.T) {
	var gotEnv []string
	runner := func(_ context.Context, _ string, env []string, _ string, _ ...string) ([]byte, error) {
		gotEnv = env
		return []byte("built"), nil
	}
	f := newFixture(t, StrategyCommand, "make release", runner)
	rel, err := f.mgr.ReleaseProject(f.ctx, f.prj.ID, "v2.0.0", "", "usr_7")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if rel.Version != "2.0.0" || rel.Status != domain.ReleasePublished || !strings.Contains(rel.Log, "built") {
		t.Fatalf("release = %+v", rel)
	}
	if !containsEnv(gotEnv, "VERSION=2.0.0") {
		t.Fatalf("release command env lacks VERSION: %v", gotEnv)
	}

	entries, err := f.st.ListAudit(f.ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "release.publish" && e.TargetID == rel.ID && strings.Contains(string(e.Detail), "usr_7") {
			found = true
		}
	}
	if !found {
		t.Fatalf("release.publish audit row with actor usr_7 not found in %d entries", len(entries))
	}
}

func TestReleaseProjectCommandFailureMarksRelease(t *testing.T) {
	runner := func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit 2")
	}
	f := newFixture(t, StrategyCommand, "false", runner)
	rel, err := f.mgr.ReleaseProject(f.ctx, f.prj.ID, "", "", "usr_1")
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("want ErrStateConflict, got %v", err)
	}
	if rel.Status != domain.ReleaseFailed || !strings.Contains(rel.Log, "boom") {
		t.Fatalf("failed release = %+v", rel)
	}
	stored, err := f.st.GetRelease(f.ctx, rel.ID)
	if err != nil || stored.Status != domain.ReleaseFailed {
		t.Fatalf("stored release status = %v (%v)", stored.Status, err)
	}
}

func TestReleaseProjectMissingProject(t *testing.T) {
	f := newFixture(t, StrategyTag, "", nil)
	if _, err := f.mgr.ReleaseProject(f.ctx, "prj_missing", "", "", "usr_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
