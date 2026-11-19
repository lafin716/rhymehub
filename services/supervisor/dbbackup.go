package supervisor

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// dbSidecars are the SQLite WAL companions copied alongside the database file.
var dbSidecars = []string{"-wal", "-shm"}

// backupDB copies the database (and any WAL sidecars) into dir. It must only run while the core
// is stopped. It reports false when there is no database yet.
func backupDB(h Home, dir string) (bool, error) {
	if _, err := os.Stat(h.DBPath()); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	if err := copyFileAtomic(h.DBPath(), filepath.Join(dir, filepath.Base(h.DBPath())), 0o600); err != nil {
		return false, err
	}
	for _, s := range dbSidecars {
		src := h.DBPath() + s
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFileAtomic(src, filepath.Join(dir, filepath.Base(src)), 0o600); err != nil {
			return false, err
		}
	}
	return true, syncDir(dir)
}

// restoreDB puts the database back from a backup directory. Sidecars missing from the backup are
// removed so a stale WAL cannot be replayed onto the restored file.
func restoreDB(h Home, dir string) error {
	if err := copyFileAtomic(filepath.Join(dir, filepath.Base(h.DBPath())), h.DBPath(), 0o600); err != nil {
		return err
	}
	for _, s := range dbSidecars {
		dst := h.DBPath() + s
		src := filepath.Join(dir, filepath.Base(dst))
		if _, err := os.Stat(src); err == nil {
			if err := copyFileAtomic(src, dst, 0o600); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return syncDir(filepath.Dir(h.DBPath()))
}

// copyFileAtomic copies src to dst through a temp file, fsync and rename.
func copyFileAtomic(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}
