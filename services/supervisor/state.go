package supervisor

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Update phases in execution order (docs/RELEASE_AND_UPDATE.md §3). "migrating" runs the new
// binary's `migrate` command after the DB backup and before the symlink switch.
const (
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseExtracting  = "extracting"
	PhaseDraining    = "draining"
	PhaseStopping    = "stopping"
	PhaseBackup      = "backup"
	PhaseMigrating   = "migrating"
	PhaseSwitching   = "switching"
	PhaseStarting    = "starting"
	PhaseHealthcheck = "healthcheck"
	PhaseDone        = "done"
)

var phaseOrder = []string{
	PhaseDownloading,
	PhaseVerifying,
	PhaseExtracting,
	PhaseDraining,
	PhaseStopping,
	PhaseBackup,
	PhaseMigrating,
	PhaseSwitching,
	PhaseStarting,
	PhaseHealthcheck,
	PhaseDone,
}

func phaseIndex(phase string) int {
	for i, p := range phaseOrder {
		if p == phase {
			return i
		}
	}
	return -1
}

// Update record statuses (docs/API_SPEC.md UpdateRecord).
const (
	StatusSucceeded  = "succeeded"
	StatusRolledBack = "rolled_back"
	StatusFailed     = "failed"
)

// Journal is the crash-safe state of an in-flight update (update-journal.json).
type Journal struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Phase    string `json:"phase"`
	DBBackup string `json:"dbBackup,omitempty"`
	// Manual marks a journal written by an operator-requested rollback rather than an update.
	Manual    bool      `json:"manual,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UpdateRecord is one line of updates.jsonl.
type UpdateRecord struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Status     string    `json:"status"`
	Phase      string    `json:"phase"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	DBBackup   string    `json:"dbBackup,omitempty"`
}

// supervisorState is supervisor-state.json.
type supervisorState struct {
	Previous       string `json:"previous,omitempty"`
	PreviousBackup string `json:"previousBackup,omitempty"`
	// HaltedReason is set when the supervisor refuses to run any core, for example after a rollback
	// that had no database backup. It is cleared by a successful update or a manual rollback.
	HaltedReason string `json:"haltedReason,omitempty"`
}

// newID returns a sortable, random update identifier.
func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// writeJSONAtomic writes v as indented JSON via temp file + fsync + rename + directory fsync.
func writeJSONAtomic(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// WriteJournal persists the journal before a phase runs.
func WriteJournal(path string, j *Journal) error {
	return writeJSONAtomic(path, j, 0o600)
}

// ReadJournal returns the journal or nil when none exists.
func ReadJournal(path string) (*Journal, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &j, nil
}

// RemoveJournal deletes the journal; a missing file is not an error.
func RemoveJournal(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// AppendHistory appends one record to updates.jsonl and fsyncs it.
func AppendHistory(path string, rec UpdateRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReadHistory returns all records from updates.jsonl (empty when the file does not exist).
func ReadHistory(path string) ([]UpdateRecord, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []UpdateRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	records := []UpdateRecord{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec UpdateRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func readState(h Home) supervisorState {
	var st supervisorState
	data, err := os.ReadFile(h.StatePath())
	if err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func writeState(h Home, st supervisorState) error {
	return writeJSONAtomic(h.StatePath(), st, 0o600)
}

// currentVersion returns the version the current symlink points to, or "" when unset.
func currentVersion(h Home) string {
	target, err := os.Readlink(h.Current())
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// ensureToken returns the supervisor bearer token, creating a random one (mode 0600) if absent.
func ensureToken(h Home) (string, error) {
	if data, err := os.ReadFile(h.TokenPath()); err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	if err := os.MkdirAll(h.SecretsDir(), 0o700); err != nil {
		return "", err
	}
	tmp := h.TokenPath() + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, h.TokenPath()); err != nil {
		return "", err
	}
	return tok, nil
}
