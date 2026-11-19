package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// --- users ----------------------------------------------------------------------------------

const userCols = `id, username, password_hash, role, disabled, created_at`

func scanUser(r rowScanner) (domain.User, error) {
	var u domain.User
	var role, created string
	var disabled int
	if err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &disabled, &created); err != nil {
		return domain.User{}, err
	}
	u.Role = domain.Role(role)
	u.Disabled = disabled != 0
	t, err := parseTime(created)
	if err != nil {
		return domain.User{}, err
	}
	u.CreatedAt = t
	return u, nil
}

// CreateUser inserts a user. Empty ID or zero CreatedAt are filled in. Returns domain.ErrDuplicate on
// a taken username and domain.ErrInvalid on an unknown role.
func (s *Store) CreateUser(ctx context.Context, u domain.User) (domain.User, error) {
	if !u.Role.Valid() {
		return domain.User{}, fmt.Errorf("%w: role %q", domain.ErrInvalid, u.Role)
	}
	if u.ID == "" {
		u.ID = domain.NewID("usr")
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO users(`+userCols+`) VALUES(?,?,?,?,?,?)`,
			u.ID, u.Username, u.PasswordHash, string(u.Role), boolInt(u.Disabled), fmtTime(u.CreatedAt))
		return duplicateErr(err, "user "+u.Username)
	})
	if err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// GetUser loads a user by id.
func (s *Store) GetUser(ctx context.Context, id string) (domain.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	return u, wrapNotFound(err, "user "+id)
}

// GetUserByUsername loads a user by username.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (domain.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
	return u, wrapNotFound(err, "user "+username)
}

// ListUsers returns all users ordered by creation time.
func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of users (0 means first-run setup is needed).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// UpdateUser writes username, password hash, role and disabled flag of u (matched by ID).
func (s *Store) UpdateUser(ctx context.Context, u domain.User) error {
	if !u.Role.Valid() {
		return fmt.Errorf("%w: role %q", domain.ErrInvalid, u.Role)
	}
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET username = ?, password_hash = ?, role = ?, disabled = ? WHERE id = ?`,
			u.Username, u.PasswordHash, string(u.Role), boolInt(u.Disabled), u.ID)
		if err != nil {
			return duplicateErr(err, "user "+u.Username)
		}
		return expectOne(res, "user "+u.ID)
	})
}

// --- browser sessions -----------------------------------------------------------------------

// CreateSession stores a session keyed by the SHA-256 hash of its cookie token.
func (s *Store) CreateSession(ctx context.Context, se domain.Session) error {
	if se.CreatedAt.IsZero() {
		se.CreatedAt = time.Now()
	}
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)`,
			se.TokenHash, se.UserID, fmtTime(se.CreatedAt), fmtTime(se.ExpiresAt))
		return duplicateErr(err, "session")
	})
}

// GetSession returns the session for a token hash, or domain.ErrNotFound. Expiry is the caller's check.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (domain.Session, error) {
	var se domain.Session
	var created, expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, user_id, created_at, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&se.TokenHash, &se.UserID, &created, &expires)
	if err != nil {
		return domain.Session{}, wrapNotFound(err, "session")
	}
	var perr error
	if se.CreatedAt, perr = parseTime(created); perr != nil {
		return domain.Session{}, perr
	}
	if se.ExpiresAt, perr = parseTime(expires); perr != nil {
		return domain.Session{}, perr
	}
	return se, nil
}

// DeleteSession removes one session (logout). Missing sessions are not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		return err
	})
}

// DeleteUserSessions removes every session of a user (password reset, disable) and returns how many.
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// DeleteExpiredSessions removes sessions that expired before now and returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, fmtTime(now))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// --- API tokens -----------------------------------------------------------------------------

const apiTokenCols = `id, user_id, name, token_hash, created_at, last_used_at, revoked_at`

func scanAPIToken(r rowScanner) (domain.APIToken, error) {
	var t domain.APIToken
	var created string
	var lastUsed, revoked sql.NullString
	if err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &created, &lastUsed, &revoked); err != nil {
		return domain.APIToken{}, err
	}
	var err error
	if t.CreatedAt, err = parseTime(created); err != nil {
		return domain.APIToken{}, err
	}
	if t.LastUsedAt, err = scanNullTime(lastUsed); err != nil {
		return domain.APIToken{}, err
	}
	if t.RevokedAt, err = scanNullTime(revoked); err != nil {
		return domain.APIToken{}, err
	}
	return t, nil
}

// CreateAPIToken stores a token record (the caller keeps the plaintext "rht_..." once and stores only the hash).
func (s *Store) CreateAPIToken(ctx context.Context, t domain.APIToken) (domain.APIToken, error) {
	if t.ID == "" {
		t.ID = domain.NewID("tok")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO api_tokens(`+apiTokenCols+`) VALUES(?,?,?,?,?,?,?)`,
			t.ID, t.UserID, t.Name, t.TokenHash, fmtTime(t.CreatedAt), nullTime(t.LastUsedAt), nullTime(t.RevokedAt))
		return duplicateErr(err, "api token")
	})
	return t, err
}

// GetAPITokenByHash finds a token by the SHA-256 hash of the presented bearer value.
func (s *Store) GetAPITokenByHash(ctx context.Context, tokenHash string) (domain.APIToken, error) {
	t, err := scanAPIToken(s.db.QueryRowContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE token_hash = ?`, tokenHash))
	return t, wrapNotFound(err, "api token")
}

// ListAPITokens lists tokens of one user (or all users when userID is empty). Hashes are not exposed by JSON.
func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]domain.APIToken, error) {
	q := `SELECT ` + apiTokenCols + ` FROM api_tokens`
	var args []any
	if userID != "" {
		q += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	q += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken marks a token revoked at the given time.
func (s *Store) RevokeAPIToken(ctx context.Context, id string, at time.Time) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			fmtTime(at), id)
		if err != nil {
			return err
		}
		return expectOne(res, "api token "+id)
	})
}

// TouchAPIToken records last use of a token.
func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, fmtTime(at), id)
		return err
	})
}

// expectOne returns domain.ErrNotFound when a write affected no rows.
func expectOne(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, what)
	}
	return nil
}
