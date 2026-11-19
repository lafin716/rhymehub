// Package store is the SQLite persistence layer shared by the core server, the scheduler and detached workers.
//
// Multiple OS processes open the same database file. Every write transaction starts by taking the SQLite
// write lock with a no-op UPDATE, so that the busy handler (busy_timeout) applies at the start of the
// transaction and read-then-write upgrades cannot fail with SQLITE_BUSY mid-transaction.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"

	"github.com/lafin716/rhymehub/internal/domain"
)

// timeLayout is fixed-width RFC3339 UTC with milliseconds so that TEXT ordering equals time ordering.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Store wraps the database handle. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the SQLite database at path and applies connection pragmas
// (WAL, busy_timeout=5000, foreign_keys=ON) to every pooled connection. It does not migrate;
// call Migrate before using the store.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty database path", domain.ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: abs path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: create db dir: %w", err)
	}
	dsn := "file:" + abs +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", abs, err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", abs, err)
	}
	return &Store{db: db, path: abs}, nil
}

// Close closes the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Path returns the absolute database file path.
func (s *Store) Path() string {
	return s.path
}

// DB exposes the underlying handle for backup tooling. Prefer the typed methods.
func (s *Store) DB() *sql.DB {
	return s.db
}

// writeTx runs fn inside a write transaction. The leading no-op UPDATE acquires the SQLite
// write lock up front (waiting up to busy_timeout), so fn can read then write without upgrade races.
// The lock statement targets schema_migrations, which Migrate creates before any other table, so it
// works while the first migration is still pending.
func (s *Store) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET version = version WHERE 0 = 1`); err != nil {
		return fmt.Errorf("store: acquire write lock: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// readTx runs fn inside a read transaction so multi-statement reads see one snapshot.
func (s *Store) readTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("store: begin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// Backup writes a consistent copy of the database to dst using VACUUM INTO. dst must not exist.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: backup target exists: %s", domain.ErrInvalid, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("store: backup dir: %w", err)
	}
	_, err := s.db.ExecContext(ctx, "VACUUM INTO "+sqlQuote(dst))
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}

// sqlQuote renders s as a single-quoted SQL string literal.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// --- time and nullable helpers -------------------------------------------------------------

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: bad timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// nullTime converts a pointer to a SQL value (NULL when nil).
func nullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return fmtTime(*t)
}

// scanNullTime parses a nullable timestamp column.
func scanNullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// nullStr returns NULL for empty strings, so optional TEXT columns stay NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// rowScanner is the subset of *sql.Row / *sql.Rows used by scan helpers.
type rowScanner interface {
	Scan(dest ...any) error
}

// wrapNotFound converts sql.ErrNoRows to domain.ErrNotFound with context.
func wrapNotFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, what)
	}
	return err
}

// isUniqueViolation detects SQLite UNIQUE/PRIMARY KEY constraint failures.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// duplicateErr maps a unique constraint error to domain.ErrDuplicate.
func duplicateErr(err error, what string) error {
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %s", domain.ErrDuplicate, what)
	}
	return err
}
