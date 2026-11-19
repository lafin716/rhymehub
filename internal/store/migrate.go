package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one embedded, numbered schema step ("0001_init.sql").
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// MigrationError reports which migration failed. The failing migration is rolled back; earlier
// migrations in the same call stay applied.
type MigrationError struct {
	Version int
	Name    string
	Err     error
}

func (e *MigrationError) Error() string {
	return fmt.Sprintf("store: migration %04d_%s failed: %v", e.Version, e.Name, e.Err)
}

// Unwrap exposes the underlying database error.
func (e *MigrationError) Unwrap() error { return e.Err }

// Migrations returns the embedded migrations sorted by version. It fails on malformed or duplicate names.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []Migration
	seen := map[int]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		num, name, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 || name == "" {
			return nil, fmt.Errorf("store: migration file %q must be NNNN_name.sql", e.Name())
		}
		if seen[v] {
			return nil, fmt.Errorf("store: duplicate migration version %d", v)
		}
		seen[v] = true
		raw, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrate applies every embedded migration that is not yet recorded in schema_migrations.
// It is idempotent and safe to call from several processes; each migration runs in its own
// transaction together with its schema_migrations row. Failures are returned as *MigrationError.
func (s *Store) Migrate(ctx context.Context) error {
	migs, err := Migrations()
	if err != nil {
		return err
	}
	// Created outside writeTx: writeTx locks on this table. IF NOT EXISTS makes concurrent calls safe.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	for _, m := range migs {
		if err := s.applyMigration(ctx, m); err != nil {
			return &MigrationError{Version: m.Version, Name: m.Name, Err: err}
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, m Migration) error {
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil // applied by another process while we waited for the lock
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
			m.Version, m.Name, fmtTime(time.Now()))
		return err
	})
}

// SchemaVersion returns the highest applied migration version, or 0 if none (or no table yet).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return int(v.Int64), nil
}

// ErrMigrationFailed reports whether err (or anything it wraps) is a *MigrationError.
func ErrMigrationFailed(err error) bool {
	var me *MigrationError
	return errors.As(err, &me)
}
