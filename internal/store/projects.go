package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// projectCols is the insert/update column list; projectSelect is the read list (nullable git_url coalesced).
const projectCols = `id, name, repo_path, git_url, default_branch, type, stack, build_cmd, test_cmd,
	release_strategy, release_cmd, default_agent, default_model, max_concurrent, timeout_minutes,
	auto_task_from_requests, auto_approve, auto_release, allowed_tools, sdk_status,
	installed_version, latest_version, is_self, archived, created_at, updated_at`

const projectSelect = `id, name, repo_path, COALESCE(git_url, ''), default_branch, type, stack, build_cmd, test_cmd,
	release_strategy, release_cmd, default_agent, default_model, max_concurrent, timeout_minutes,
	auto_task_from_requests, auto_approve, auto_release, allowed_tools, sdk_status,
	installed_version, latest_version, is_self, archived, created_at, updated_at`

// encodeList renders a string slice as a JSON array ("[]" for nil) for TEXT columns.
func encodeList(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: encode list: %w", err)
	}
	return string(b), nil
}

// decodeList parses a JSON array TEXT column. Empty text yields nil.
func decodeList(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode list %q: %w", s, err)
	}
	return out, nil
}

func scanProject(r rowScanner) (domain.Project, error) {
	var p domain.Project
	var tools, created, updated string
	err := r.Scan(&p.ID, &p.Name, &p.RepoPath, &p.GitURL, &p.DefaultBranch, &p.Type, &p.Stack,
		&p.BuildCmd, &p.TestCmd, &p.ReleaseStrategy, &p.ReleaseCmd, &p.DefaultAgent, &p.DefaultModel,
		&p.MaxConcurrent, &p.TimeoutMinutes, &p.AutoTaskFromRequests, &p.AutoApprove, &p.AutoRelease,
		&tools, &p.SDKStatus, &p.InstalledVersion, &p.LatestVersion, &p.IsSelf, &p.Archived,
		&created, &updated)
	if err != nil {
		return domain.Project{}, err
	}
	if p.AllowedTools, err = decodeList(tools); err != nil {
		return domain.Project{}, err
	}
	if p.CreatedAt, err = parseTime(created); err != nil {
		return domain.Project{}, err
	}
	if p.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Project{}, err
	}
	return p, nil
}

// CreateProject inserts a project. Empty ID/timestamps are filled in; MaxConcurrent < 1 becomes 1.
func (s *Store) CreateProject(ctx context.Context, p domain.Project) (domain.Project, error) {
	if p.ID == "" {
		p.ID = domain.NewID("prj")
	}
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.DefaultBranch == "" {
		p.DefaultBranch = "main"
	}
	if p.MaxConcurrent < 1 {
		p.MaxConcurrent = 1
	}
	tools, err := encodeList(p.AllowedTools)
	if err != nil {
		return domain.Project{}, err
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(`+projectCols+`) VALUES(
			?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Name, p.RepoPath, nullStr(p.GitURL), p.DefaultBranch, orDefault(p.Type, "generic"),
			p.Stack, p.BuildCmd, p.TestCmd, orDefault(p.ReleaseStrategy, "none"), p.ReleaseCmd,
			orDefault(p.DefaultAgent, "claude"), p.DefaultModel, p.MaxConcurrent, orDefaultInt(p.TimeoutMinutes, 30),
			boolInt(p.AutoTaskFromRequests), boolInt(p.AutoApprove), boolInt(p.AutoRelease), tools,
			orDefault(p.SDKStatus, "none"), p.InstalledVersion, p.LatestVersion, boolInt(p.IsSelf), boolInt(p.Archived),
			fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
		return duplicateErr(err, "project "+p.Name)
	})
	if err != nil {
		return domain.Project{}, err
	}
	return p, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// GetProject loads a project by id.
func (s *Store) GetProject(ctx context.Context, id string) (domain.Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectSelect+` FROM projects WHERE id = ?`, id))
	return p, wrapNotFound(err, "project "+id)
}

// ListProjects returns projects ordered by name. Archived projects are excluded unless includeArchived.
func (s *Store) ListProjects(ctx context.Context, includeArchived bool) ([]domain.Project, error) {
	q := `SELECT ` + projectSelect + ` FROM projects`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY name, id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject writes all editable fields of p (matched by ID) and bumps updated_at.
func (s *Store) UpdateProject(ctx context.Context, p domain.Project) (domain.Project, error) {
	if p.MaxConcurrent < 1 {
		p.MaxConcurrent = 1
	}
	tools, err := encodeList(p.AllowedTools)
	if err != nil {
		return domain.Project{}, err
	}
	p.UpdatedAt = time.Now()
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET name=?, repo_path=?, git_url=?, default_branch=?,
			type=?, stack=?, build_cmd=?, test_cmd=?, release_strategy=?, release_cmd=?, default_agent=?,
			default_model=?, max_concurrent=?, timeout_minutes=?, auto_task_from_requests=?, auto_approve=?,
			auto_release=?, allowed_tools=?, sdk_status=?, installed_version=?, latest_version=?, is_self=?,
			archived=?, updated_at=? WHERE id=?`,
			p.Name, p.RepoPath, nullStr(p.GitURL), p.DefaultBranch, p.Type, p.Stack, p.BuildCmd, p.TestCmd,
			p.ReleaseStrategy, p.ReleaseCmd, p.DefaultAgent, p.DefaultModel, p.MaxConcurrent, p.TimeoutMinutes,
			boolInt(p.AutoTaskFromRequests), boolInt(p.AutoApprove), boolInt(p.AutoRelease), tools,
			p.SDKStatus, p.InstalledVersion, p.LatestVersion, boolInt(p.IsSelf), boolInt(p.Archived),
			fmtTime(p.UpdatedAt), p.ID)
		if err != nil {
			return duplicateErr(err, "project "+p.Name)
		}
		return expectOne(res, "project "+p.ID)
	})
	return p, err
}

// --- project keys (SDK reporter keys) ---------------------------------------------------------

const projectKeyCols = `id, project_id, name, key_hash, key_prefix, allowed_origins, created_at, last_used_at, revoked_at`

func scanProjectKey(r rowScanner) (domain.ProjectKey, error) {
	var k domain.ProjectKey
	var origins, created string
	var lastUsed, revoked sql.NullString
	if err := r.Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.KeyPrefix, &origins, &created, &lastUsed, &revoked); err != nil {
		return domain.ProjectKey{}, err
	}
	var err error
	if k.AllowedOrigins, err = decodeList(origins); err != nil {
		return domain.ProjectKey{}, err
	}
	if k.CreatedAt, err = parseTime(created); err != nil {
		return domain.ProjectKey{}, err
	}
	if k.LastUsedAt, err = scanNullTime(lastUsed); err != nil {
		return domain.ProjectKey{}, err
	}
	if k.RevokedAt, err = scanNullTime(revoked); err != nil {
		return domain.ProjectKey{}, err
	}
	return k, nil
}

// CreateProjectKey stores a key record. Only KeyHash is persisted; the plaintext is never stored.
func (s *Store) CreateProjectKey(ctx context.Context, k domain.ProjectKey) (domain.ProjectKey, error) {
	if k.ID == "" {
		k.ID = domain.NewID("pkey")
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now()
	}
	if k.AllowedOrigins == nil {
		k.AllowedOrigins = []string{"*"}
	}
	origins, err := encodeList(k.AllowedOrigins)
	if err != nil {
		return domain.ProjectKey{}, err
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO project_keys(`+projectKeyCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
			k.ID, k.ProjectID, k.Name, k.KeyHash, k.KeyPrefix, origins, fmtTime(k.CreatedAt),
			nullTime(k.LastUsedAt), nullTime(k.RevokedAt))
		return duplicateErr(err, "project key")
	})
	return k, err
}

// GetProjectKeyByHash finds an SDK key by the hash of the presented value.
func (s *Store) GetProjectKeyByHash(ctx context.Context, keyHash string) (domain.ProjectKey, error) {
	k, err := scanProjectKey(s.db.QueryRowContext(ctx,
		`SELECT `+projectKeyCols+` FROM project_keys WHERE key_hash = ?`, keyHash))
	return k, wrapNotFound(err, "project key")
}

// ListProjectKeys lists the keys of one project, newest first.
func (s *Store) ListProjectKeys(ctx context.Context, projectID string) ([]domain.ProjectKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+projectKeyCols+` FROM project_keys WHERE project_id = ? ORDER BY created_at DESC, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ProjectKey
	for rows.Next() {
		k, err := scanProjectKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeProjectKey marks a key revoked. Already revoked keys return domain.ErrNotFound.
func (s *Store) RevokeProjectKey(ctx context.Context, id string, at time.Time) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE project_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, fmtTime(at), id)
		if err != nil {
			return err
		}
		return expectOne(res, "project key "+id)
	})
}

// TouchProjectKey records last use of a key.
func (s *Store) TouchProjectKey(ctx context.Context, id string, at time.Time) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE project_keys SET last_used_at = ? WHERE id = ?`, fmtTime(at), id)
		return err
	})
}
