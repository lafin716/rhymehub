package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

const requestSelect = `id, project_id, source, type, title, description, priority, status,
	COALESCE(installation_id, ''), COALESCE(app_version, ''), COALESCE(platform, ''),
	COALESCE(reporter_user_id, ''), COALESCE(reporter_key_id, ''), diagnostics, redactions,
	COALESCE(task_id, ''), created_at, updated_at`

func scanRequest(r rowScanner) (domain.Request, error) {
	var q domain.Request
	var src, typ, status, diags, created, updated string
	err := r.Scan(&q.ID, &q.ProjectID, &src, &typ, &q.Title, &q.Description, &q.Priority, &status,
		&q.InstallationID, &q.AppVersion, &q.Platform, &q.ReporterUserID, &q.ReporterKeyID,
		&diags, &q.Redactions, &q.TaskID, &created, &updated)
	if err != nil {
		return domain.Request{}, err
	}
	q.Source = domain.RequestSource(src)
	q.Type = domain.RequestType(typ)
	q.Status = domain.RequestStatus(status)
	if diags != "" && diags != "[]" {
		if err := json.Unmarshal([]byte(diags), &q.Diagnostics); err != nil {
			return domain.Request{}, fmt.Errorf("store: decode diagnostics: %w", err)
		}
	}
	if q.CreatedAt, err = parseTime(created); err != nil {
		return domain.Request{}, err
	}
	if q.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Request{}, err
	}
	return q, nil
}

// CreateRequest inserts an inbox item. Status defaults to "new". Source and type must be documented values.
func (s *Store) CreateRequest(ctx context.Context, q domain.Request) (domain.Request, error) {
	if !q.Source.Valid() {
		return domain.Request{}, fmt.Errorf("%w: source %q", domain.ErrInvalid, q.Source)
	}
	if !q.Type.Valid() {
		return domain.Request{}, fmt.Errorf("%w: type %q", domain.ErrInvalid, q.Type)
	}
	if q.ID == "" {
		q.ID = domain.NewID("req")
	}
	if q.Status == "" {
		q.Status = domain.RequestNew
	}
	now := time.Now()
	if q.CreatedAt.IsZero() {
		q.CreatedAt = now
	}
	q.UpdatedAt = now
	diags := "[]"
	if q.Diagnostics != nil {
		b, err := json.Marshal(q.Diagnostics)
		if err != nil {
			return domain.Request{}, fmt.Errorf("store: encode diagnostics: %w", err)
		}
		diags = string(b)
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO requests(id, project_id, source, type, title, description,
			priority, status, installation_id, app_version, platform, reporter_user_id, reporter_key_id,
			diagnostics, redactions, task_id, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			q.ID, q.ProjectID, string(q.Source), string(q.Type), q.Title, q.Description, q.Priority,
			string(q.Status), nullStr(q.InstallationID), nullStr(q.AppVersion), nullStr(q.Platform),
			nullStr(q.ReporterUserID), nullStr(q.ReporterKeyID), diags, q.Redactions, nullStr(q.TaskID),
			fmtTime(q.CreatedAt), fmtTime(q.UpdatedAt))
		return duplicateErr(err, "request "+q.ID)
	})
	return q, err
}

// GetRequest loads an inbox item by id.
func (s *Store) GetRequest(ctx context.Context, id string) (domain.Request, error) {
	q, err := scanRequest(s.db.QueryRowContext(ctx, `SELECT `+requestSelect+` FROM requests WHERE id = ?`, id))
	return q, wrapNotFound(err, "request "+id)
}

// ListRequests returns the inbox of one project (newest first). Empty status means all statuses.
func (s *Store) ListRequests(ctx context.Context, projectID string, status domain.RequestStatus, limit int) ([]domain.Request, error) {
	q := `SELECT ` + requestSelect + ` FROM requests WHERE project_id = ?`
	args := []any{projectID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Request
	for rows.Next() {
		item, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SetRequestStatus updates status and optionally links a task (taskID empty leaves the link unchanged).
func (s *Store) SetRequestStatus(ctx context.Context, id string, status domain.RequestStatus, taskID string) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE requests SET status = ?,
			task_id = COALESCE(NULLIF(?, ''), task_id), updated_at = ? WHERE id = ?`,
			string(status), taskID, fmtTime(time.Now()), id)
		if err != nil {
			return err
		}
		return expectOne(res, "request "+id)
	})
}

// --- attachments ------------------------------------------------------------------------------

const attachmentCols = `id, request_id, kind, filename, mime, size, path, redacted, created_at`

func scanAttachment(r rowScanner) (domain.Attachment, error) {
	var a domain.Attachment
	var kind, created string
	var redacted int
	if err := r.Scan(&a.ID, &a.RequestID, &kind, &a.Filename, &a.Mime, &a.Size, &a.Path, &redacted, &created); err != nil {
		return domain.Attachment{}, err
	}
	a.Kind = domain.AttachKind(kind)
	a.Redacted = redacted != 0
	t, err := parseTime(created)
	if err != nil {
		return domain.Attachment{}, err
	}
	a.CreatedAt = t
	return a, nil
}

// CreateAttachment records an attachment whose file already lives outside worktrees at a.Path.
func (s *Store) CreateAttachment(ctx context.Context, a domain.Attachment) (domain.Attachment, error) {
	if !(a.Kind == domain.AttachScreenshot || a.Kind == domain.AttachLog || a.Kind == domain.AttachFile) {
		return domain.Attachment{}, fmt.Errorf("%w: attachment kind %q", domain.ErrInvalid, a.Kind)
	}
	if a.ID == "" {
		a.ID = domain.NewID("att")
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO attachments(`+attachmentCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
			a.ID, a.RequestID, string(a.Kind), a.Filename, a.Mime, a.Size, a.Path, boolInt(a.Redacted), fmtTime(a.CreatedAt))
		return duplicateErr(err, "attachment "+a.ID)
	})
	return a, err
}

// GetAttachment loads one attachment by id.
func (s *Store) GetAttachment(ctx context.Context, id string) (domain.Attachment, error) {
	a, err := scanAttachment(s.db.QueryRowContext(ctx, `SELECT `+attachmentCols+` FROM attachments WHERE id = ?`, id))
	return a, wrapNotFound(err, "attachment "+id)
}

// ListAttachments returns the attachments of one request in creation order.
func (s *Store) ListAttachments(ctx context.Context, requestID string) ([]domain.Attachment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+attachmentCols+` FROM attachments WHERE request_id = ? ORDER BY created_at, id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
