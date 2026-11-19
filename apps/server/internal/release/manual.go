package release

import (
	"context"
	"fmt"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

// ReleaseProject cuts a manual release of the project's checked-out default branch HEAD. It is the
// POST /projects/{id}/releases path and has no task: the release is not linked to any task. The project's
// configured strategy is used. version may be empty (the next version is computed from tags and existing
// releases); notes may be empty (generated from git log by the tag strategy). actorUserID is recorded as the
// creator in the audit detail.
//
// A project with strategy "none" is rejected with an error wrapping domain.ErrInvalid. A strategy failure leaves
// the release row in FAILED and returns an error wrapping domain.ErrStateConflict.
func (m *Manager) ReleaseProject(ctx context.Context, projectID, version, notes, actorUserID string) (domain.Release, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, err := m.st.GetProject(ctx, projectID)
	if err != nil {
		return domain.Release{}, err
	}
	if p.ReleaseStrategy == StrategyNone || p.ReleaseStrategy == "" {
		return domain.Release{}, fmt.Errorf("%w: project %s has no release strategy configured", domain.ErrInvalid, p.ID)
	}
	if f := m.preflight(p); f != nil {
		return domain.Release{}, fmt.Errorf("%w: %s: %s", domain.ErrStateConflict, f.reason, f.msg)
	}

	ver := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if ver == "" {
		ver, err = m.nextVersionFor(ctx, p, "")
		if err != nil {
			return domain.Release{}, err
		}
	} else if _, ok := parseVersion(ver); !ok {
		return domain.Release{}, fmt.Errorf("%w: version %q must be X.Y.Z", domain.ErrInvalid, version)
	}

	rel, err := m.st.CreateRelease(ctx, domain.Release{ProjectID: p.ID, Version: ver, Tag: "v" + ver,
		Strategy: p.ReleaseStrategy, Notes: strings.TrimSpace(notes), Status: domain.ReleaseBuilding, CreatedBy: actorUserID})
	if err != nil {
		return domain.Release{}, err
	}

	// A manual release has no task, so the tag strategy falls back to the repository HEAD.
	out, f, err := m.runStrategy(ctx, p, domain.Task{}, rel)
	if err != nil {
		return domain.Release{}, err
	}
	if f != nil {
		rel.Status = domain.ReleaseFailed
		rel.Log = f.log
		if _, err := m.st.UpdateRelease(ctx, rel); err != nil {
			return domain.Release{}, err
		}
		if err := m.audit(ctx, "release.fail", "release", rel.ID, map[string]any{
			"version": rel.Version, "strategy": rel.Strategy, "reason": f.reason, "message": f.msg, "actor": actorUserID,
		}); err != nil {
			return domain.Release{}, err
		}
		return rel, fmt.Errorf("%w: %s: %s", domain.ErrStateConflict, f.reason, f.msg)
	}

	rel.Status = domain.ReleasePublished
	rel.Tag = out.tag
	if strings.TrimSpace(notes) == "" {
		rel.Notes = out.notes
	}
	rel.Log = out.log
	rel.Artifacts = out.artifacts
	if rel, err = m.st.UpdateRelease(ctx, rel); err != nil {
		return domain.Release{}, err
	}
	if err := m.audit(ctx, "release.publish", "release", rel.ID, map[string]any{
		"version": rel.Version, "strategy": rel.Strategy, "manual": true, "actor": actorUserID,
	}); err != nil {
		return domain.Release{}, err
	}
	return rel, nil
}
