package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// --- releases ---------------------------------------------------------------------------------

const releaseCols = `id, project_id, version, tag, strategy, task_ids, notes, status, artifacts, log,
	created_by, COALESCE(approved_by, ''), created_at, updated_at`

// releaseInsertCols mirrors releaseCols without the read-side COALESCE, which is invalid in an INSERT column list.
const releaseInsertCols = `id, project_id, version, tag, strategy, task_ids, notes, status, artifacts, log,
	created_by, approved_by, created_at, updated_at`

func scanRelease(r rowScanner) (domain.Release, error) {
	var rel domain.Release
	var taskIDs, artifacts, status, created, updated string
	if err := r.Scan(&rel.ID, &rel.ProjectID, &rel.Version, &rel.Tag, &rel.Strategy, &taskIDs, &rel.Notes,
		&status, &artifacts, &rel.Log, &rel.CreatedBy, &rel.ApprovedBy, &created, &updated); err != nil {
		return domain.Release{}, err
	}
	rel.Status = domain.ReleaseStatus(status)
	var err error
	if rel.TaskIDs, err = decodeList(taskIDs); err != nil {
		return domain.Release{}, err
	}
	if artifacts != "" && artifacts != "[]" {
		if err := json.Unmarshal([]byte(artifacts), &rel.Artifacts); err != nil {
			return domain.Release{}, fmt.Errorf("store: decode artifacts: %w", err)
		}
	}
	if rel.CreatedAt, err = parseTime(created); err != nil {
		return domain.Release{}, err
	}
	if rel.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Release{}, err
	}
	return rel, nil
}

func encodeArtifacts(a []domain.Artifact) (string, error) {
	if a == nil {
		a = []domain.Artifact{}
	}
	b, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("store: encode artifacts: %w", err)
	}
	return string(b), nil
}

func validReleaseStatus(s domain.ReleaseStatus) bool {
	switch s {
	case domain.ReleaseBuilding, domain.ReleasePublished, domain.ReleaseFailed, domain.ReleaseRolledBack:
		return true
	}
	return false
}

// CreateRelease inserts a release. Status defaults to "building". Version must be unique per project.
func (s *Store) CreateRelease(ctx context.Context, rel domain.Release) (domain.Release, error) {
	if rel.ID == "" {
		rel.ID = domain.NewID("rel")
	}
	if rel.Status == "" {
		rel.Status = domain.ReleaseBuilding
	}
	if !validReleaseStatus(rel.Status) {
		return domain.Release{}, fmt.Errorf("%w: release status %q", domain.ErrInvalid, rel.Status)
	}
	now := time.Now()
	if rel.CreatedAt.IsZero() {
		rel.CreatedAt = now
	}
	rel.UpdatedAt = now
	taskIDs, err := encodeList(rel.TaskIDs)
	if err != nil {
		return domain.Release{}, err
	}
	artifacts, err := encodeArtifacts(rel.Artifacts)
	if err != nil {
		return domain.Release{}, err
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO releases(`+releaseInsertCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			rel.ID, rel.ProjectID, rel.Version, rel.Tag, rel.Strategy, taskIDs, rel.Notes, string(rel.Status),
			artifacts, rel.Log, rel.CreatedBy, nullStr(rel.ApprovedBy), fmtTime(rel.CreatedAt), fmtTime(rel.UpdatedAt))
		return duplicateErr(err, "release "+rel.Version)
	})
	return rel, err
}

// GetRelease loads a release by id.
func (s *Store) GetRelease(ctx context.Context, id string) (domain.Release, error) {
	rel, err := scanRelease(s.db.QueryRowContext(ctx, `SELECT `+releaseCols+` FROM releases WHERE id = ?`, id))
	return rel, wrapNotFound(err, "release "+id)
}

// ListReleases returns releases of a project, newest first. Empty projectID lists all.
func (s *Store) ListReleases(ctx context.Context, projectID string) ([]domain.Release, error) {
	q := `SELECT ` + releaseCols + ` FROM releases`
	var args []any
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Release
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

// UpdateRelease writes the mutable fields of rel (matched by ID): tag, strategy, notes, status, artifacts,
// log and approved_by. Task links and version are immutable after creation.
func (s *Store) UpdateRelease(ctx context.Context, rel domain.Release) (domain.Release, error) {
	if !validReleaseStatus(rel.Status) {
		return domain.Release{}, fmt.Errorf("%w: release status %q", domain.ErrInvalid, rel.Status)
	}
	artifacts, err := encodeArtifacts(rel.Artifacts)
	if err != nil {
		return domain.Release{}, err
	}
	rel.UpdatedAt = time.Now()
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE releases SET tag=?, strategy=?, notes=?, status=?, artifacts=?,
			log=?, approved_by=?, updated_at=? WHERE id=?`,
			rel.Tag, rel.Strategy, rel.Notes, string(rel.Status), artifacts, rel.Log, nullStr(rel.ApprovedBy),
			fmtTime(rel.UpdatedAt), rel.ID)
		if err != nil {
			return err
		}
		return expectOne(res, "release "+rel.ID)
	})
	return rel, err
}

// --- audit log --------------------------------------------------------------------------------

// AppendAudit appends one audit record. The log is append-only: there is no update or delete API.
func (s *Store) AppendAudit(ctx context.Context, e domain.AuditEntry) (domain.AuditEntry, error) {
	if e.Action == "" {
		return domain.AuditEntry{}, fmt.Errorf("%w: audit action is empty", domain.ErrInvalid)
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	detail := "{}"
	if len(e.Detail) > 0 {
		detail = string(e.Detail)
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO audit_log(at, actor, actor_role, action, target_type,
			target_id, detail, ip) VALUES(?,?,?,?,?,?,?,?)`,
			fmtTime(e.At), e.Actor, e.ActorRole, e.Action, e.TargetType, e.TargetID, detail, e.IP)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		e.ID = id
		return nil
	})
	return e, err
}

// ListAudit returns the most recent audit entries first, at most limit (0 = 100). Use afterID > 0 for
// keyset paging: only entries with id < afterID are returned.
func (s *Store) ListAudit(ctx context.Context, limit int, beforeID int64) ([]domain.AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, at, actor, actor_role, action, target_type, target_id, detail, ip FROM audit_log`
	var args []any
	if beforeID > 0 {
		q += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var at, detail string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.ActorRole, &e.Action, &e.TargetType, &e.TargetID, &detail, &e.IP); err != nil {
			return nil, err
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, err
		}
		e.Detail = json.RawMessage(detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- push subscriptions -----------------------------------------------------------------------

// UpsertPushSubscription stores a Web Push subscription. The endpoint is unique: a re-subscribe replaces
// the keys and owner of the existing row.
func (s *Store) UpsertPushSubscription(ctx context.Context, ps domain.PushSubscription) (domain.PushSubscription, error) {
	if ps.ID == "" {
		ps.ID = domain.NewID("push")
	}
	if ps.CreatedAt.IsZero() {
		ps.CreatedAt = time.Now()
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO push_subscriptions(id, user_id, endpoint, p256dh, auth, created_at)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth`,
			ps.ID, ps.UserID, ps.Endpoint, ps.P256DH, ps.Auth, fmtTime(ps.CreatedAt))
		return err
	})
	return ps, err
}

// DeletePushSubscription removes a subscription by endpoint. Missing endpoints are not an error.
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
		return err
	})
}

// ListPushSubscriptionsByUser returns the subscriptions of one user, oldest first.
func (s *Store) ListPushSubscriptionsByUser(ctx context.Context, userID string) ([]domain.PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, endpoint, p256dh, auth, created_at
		FROM push_subscriptions WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PushSubscription
	for rows.Next() {
		var ps domain.PushSubscription
		var created string
		if err := rows.Scan(&ps.ID, &ps.UserID, &ps.Endpoint, &ps.P256DH, &ps.Auth, &created); err != nil {
			return nil, err
		}
		if ps.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

// --- notifications ----------------------------------------------------------------------------

const notificationCols = `id, event_key, kind, title, body, url, COALESCE(project_id, ''), COALESCE(task_id, ''),
	status, attempts, last_error, read_at, created_at, next_attempt_at`

func scanNotification(r rowScanner) (domain.Notification, error) {
	var n domain.Notification
	var status, created, next string
	var readAt sql.NullString
	if err := r.Scan(&n.ID, &n.EventKey, &n.Kind, &n.Title, &n.Body, &n.URL, &n.ProjectID, &n.TaskID,
		&status, &n.Attempts, &n.LastError, &readAt, &created, &next); err != nil {
		return domain.Notification{}, err
	}
	n.Status = domain.NotifyStatus(status)
	var err error
	if n.ReadAt, err = scanNullTime(readAt); err != nil {
		return domain.Notification{}, err
	}
	if n.CreatedAt, err = parseTime(created); err != nil {
		return domain.Notification{}, err
	}
	if n.NextAttemptAt, err = parseTime(next); err != nil {
		return domain.Notification{}, err
	}
	return n, nil
}

// EnqueueNotification inserts a notification. A repeated EventKey is ignored (dedupe) and the stored row
// is returned with inserted=false.
func (s *Store) EnqueueNotification(ctx context.Context, n domain.Notification) (domain.Notification, bool, error) {
	if n.EventKey == "" {
		return domain.Notification{}, false, fmt.Errorf("%w: notification event key is empty", domain.ErrInvalid)
	}
	if n.ID == "" {
		n.ID = domain.NewID("ntf")
	}
	if n.Status == "" {
		n.Status = domain.NotifyPending
	}
	now := time.Now()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	if n.NextAttemptAt.IsZero() {
		n.NextAttemptAt = now
	}
	var inserted bool
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO notifications(id, event_key, kind, title, body, url,
			project_id, task_id, status, attempts, last_error, read_at, created_at, next_attempt_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(event_key) DO NOTHING`,
			n.ID, n.EventKey, n.Kind, n.Title, n.Body, n.URL, n.ProjectID, n.TaskID, string(n.Status),
			n.Attempts, n.LastError, nullTime(n.ReadAt), fmtTime(n.CreatedAt), fmtTime(n.NextAttemptAt))
		if err != nil {
			return err
		}
		c, err := res.RowsAffected()
		if err != nil {
			return err
		}
		inserted = c > 0
		if !inserted {
			row := tx.QueryRowContext(ctx, `SELECT `+notificationCols+` FROM notifications WHERE event_key = ?`, n.EventKey)
			n, err = scanNotification(row)
			return err
		}
		return nil
	})
	return n, inserted, err
}

// ListDueNotifications returns pending notifications whose next attempt time is not after now, oldest first.
func (s *Store) ListDueNotifications(ctx context.Context, now time.Time, limit int) ([]domain.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+notificationCols+` FROM notifications
		WHERE status = ? AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`,
		string(domain.NotifyPending), fmtTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkNotification updates delivery status, attempt count, last error and next attempt time.
func (s *Store) MarkNotification(ctx context.Context, id string, status domain.NotifyStatus, attempts int, lastError string, nextAttempt time.Time) error {
	switch status {
	case domain.NotifyPending, domain.NotifySent, domain.NotifyFailed, domain.NotifySkipped:
	default:
		return fmt.Errorf("%w: notification status %q", domain.ErrInvalid, status)
	}
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notifications SET status = ?, attempts = ?, last_error = ?,
			next_attempt_at = ? WHERE id = ?`,
			string(status), attempts, lastError, fmtTime(nextAttempt), id)
		if err != nil {
			return err
		}
		return expectOne(res, "notification "+id)
	})
}

// --- settings ---------------------------------------------------------------------------------

// GetSetting returns a settings value, or domain.ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, wrapNotFound(err, "setting "+key)
}

// SetSetting upserts a settings value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: setting key is empty", domain.ErrInvalid)
	}
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	})
}

// ListAllPushSubscriptions returns every Web Push subscription across users, oldest first.
func (s *Store) ListAllPushSubscriptions(ctx context.Context) ([]domain.PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, endpoint, p256dh, auth, created_at
		FROM push_subscriptions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PushSubscription
	for rows.Next() {
		var ps domain.PushSubscription
		var created string
		if err := rows.Scan(&ps.ID, &ps.UserID, &ps.Endpoint, &ps.P256DH, &ps.Auth, &created); err != nil {
			return nil, err
		}
		if ps.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

// ListNotifications returns the most recent notifications (any delivery status), newest first.
// Notifications are global in v0.1: every user sees the same feed.
func (s *Store) ListNotifications(ctx context.Context, limit int) ([]domain.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+notificationCols+` FROM notifications
		ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkNotificationsRead sets read_at on the given notifications that are still unread. Unknown ids are ignored.
// It returns the number of rows changed.
func (s *Store) MarkNotificationsRead(ctx context.Context, ids []string, at time.Time) (int, error) {
	n := 0
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE id = ? AND read_at IS NULL`,
				fmtTime(at), id)
			if err != nil {
				return err
			}
			c, err := res.RowsAffected()
			if err != nil {
				return err
			}
			n += int(c)
		}
		return nil
	})
	return n, err
}
