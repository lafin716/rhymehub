package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/signing"
)

// migrateTimeout bounds the new binary's `migrate` run.
const migrateTimeout = 10 * time.Minute

// ErrUnsigned is returned for an artifact without a signature when unsigned installs are not allowed.
var ErrUnsigned = errors.New("artifact is unsigned")

// ErrSimulatedCrash is the conventional error a PhaseHook returns to simulate a crash. An update
// aborted by a hook error records no outcome, so the journal is left for Recover.
var ErrSimulatedCrash = errors.New("simulated crash")

// crashError marks an update that was aborted by PhaseHook. It is not recorded in history.
type crashError struct{ err error }

func (e *crashError) Error() string { return "update interrupted: " + e.err.Error() }
func (e *crashError) Unwrap() error { return e.err }

// Update downloads, verifies, and applies version from the feed. The returned record is the
// outcome, and it is also appended to updates.jsonl. A record with an empty ID means the update
// was refused before it began.
func (s *Supervisor) Update(ctx context.Context, version string) (UpdateRecord, error) {
	if version == "" {
		return UpdateRecord{}, errors.New("version is required")
	}
	if !s.acquire() {
		return UpdateRecord{}, ErrBusy
	}
	defer s.release()
	h := s.opts.Home
	from := currentVersion(h)
	if version == from {
		return UpdateRecord{}, fmt.Errorf("version %s is already current", version)
	}
	if s.cfg.UpdateFeed == "" {
		return UpdateRecord{}, errors.New("no update feed configured")
	}
	man, err := fetchManifest(ctx, s.feedClient, s.cfg.UpdateFeed)
	if err != nil {
		return UpdateRecord{}, err
	}
	_, art, err := man.ArtifactFor(version, s.opts.GOOS, s.opts.GOARCH)
	if err != nil {
		return UpdateRecord{}, err
	}
	ref, err := resolveRef(s.cfg.UpdateFeed, art.URL)
	if err != nil {
		return UpdateRecord{}, err
	}
	now := time.Now().UTC()
	j := &Journal{ID: newID(), From: from, To: version, StartedAt: now, UpdatedAt: now}
	return s.runUpdate(ctx, j, art, ref)
}

// runUpdate drives the phase sequence. Failures before stopping leave the running core alone.
// Failures from stopping onward roll back to the previous version.
func (s *Supervisor) runUpdate(ctx context.Context, j *Journal, art signing.Artifact, ref string) (UpdateRecord, error) {
	h := s.opts.Home
	tarball := filepath.Join(h.DownloadsDir(), j.ID+".tar.gz")
	defer func() { _ = os.Remove(tarball) }()

	if err := s.enter(j, PhaseDownloading); err != nil {
		return UpdateRecord{}, err
	}
	digest, err := downloadArtifact(ctx, s.feedClient, ref, tarball, s.opts.MaxArtifactBytes)
	if err != nil {
		return s.failBeforeStop(j, err)
	}

	if err := s.enter(j, PhaseVerifying); err != nil {
		return UpdateRecord{}, err
	}
	if err := s.verifyArtifact(digest, art); err != nil {
		return s.failBeforeStop(j, err)
	}

	if err := s.enter(j, PhaseExtracting); err != nil {
		return UpdateRecord{}, err
	}
	if err := installVersion(ctx, h, j.To, tarball, s.opts.MaxArtifactBytes); err != nil {
		return s.failBeforeStop(j, err)
	}

	if err := s.enter(j, PhaseDraining); err != nil {
		return UpdateRecord{}, err
	}
	s.drain(ctx)

	// From here on the old core is stopped, and any failure rolls back.
	if err := s.enter(j, PhaseStopping); err != nil {
		return UpdateRecord{}, err
	}
	if err := s.stopTracked(); err != nil {
		return s.rollback(ctx, j, fmt.Errorf("stop core: %w", err), StatusRolledBack)
	}

	if err := s.enter(j, PhaseBackup); err != nil {
		return UpdateRecord{}, err
	}
	backupDir := filepath.Join(h.BackupsDir(), j.ID+"-"+j.From)
	ok, err := backupDB(h, backupDir)
	if err != nil {
		return s.rollback(ctx, j, fmt.Errorf("backup db: %w", err), StatusRolledBack)
	}
	if ok {
		j.DBBackup = backupDir
	}
	if err := s.save(j); err != nil {
		return s.rollback(ctx, j, err, StatusRolledBack)
	}

	if err := s.enter(j, PhaseMigrating); err != nil {
		return UpdateRecord{}, err
	}
	if err := runMigrate(ctx, h, j.To); err != nil {
		return s.rollback(ctx, j, err, StatusRolledBack)
	}

	if err := s.enter(j, PhaseSwitching); err != nil {
		return UpdateRecord{}, err
	}
	if err := switchCurrent(h, j.To); err != nil {
		return s.rollback(ctx, j, fmt.Errorf("switch: %w", err), StatusRolledBack)
	}

	if err := s.enter(j, PhaseStarting); err != nil {
		return UpdateRecord{}, err
	}
	p, err := spawnCore(h, h.CurrentBinary())
	if err != nil {
		return s.rollback(ctx, j, err, StatusRolledBack)
	}
	s.setCore(p)

	if err := s.enter(j, PhaseHealthcheck); err != nil {
		return UpdateRecord{}, err
	}
	if err := s.healthy(ctx, p, j.To); err != nil {
		return s.rollback(ctx, j, err, StatusRolledBack)
	}

	if err := s.enter(j, PhaseDone); err != nil {
		return UpdateRecord{}, err
	}
	return s.commit(j)
}

// enter journals the phase, then runs the test hook. A hook error aborts without an outcome.
func (s *Supervisor) enter(j *Journal, phase string) error {
	j.Phase = phase
	if err := s.save(j); err != nil {
		return fmt.Errorf("journal %s: %w", phase, err)
	}
	s.opts.Logf("update %s %s -> %s: %s", j.ID, j.From, j.To, phase)
	if s.opts.PhaseHook != nil {
		if err := s.opts.PhaseHook(phase); err != nil {
			return &crashError{err: err}
		}
	}
	return nil
}

func (s *Supervisor) save(j *Journal) error {
	j.UpdatedAt = time.Now().UTC()
	return WriteJournal(s.opts.Home.JournalPath(), j)
}

// verifyArtifact checks the sha256 pin and, when present, the ed25519 signature over the digest.
func (s *Supervisor) verifyArtifact(digest string, art signing.Artifact) error {
	if art.SHA256 == "" {
		return errors.New("artifact has no sha256")
	}
	if !strings.EqualFold(art.SHA256, digest) {
		return fmt.Errorf("%w: want %s, got %s", signing.ErrDigestMismatch, art.SHA256, digest)
	}
	if art.Signature == "" {
		if s.opts.AllowUnsigned {
			return nil
		}
		return ErrUnsigned
	}
	if s.pubKey == nil {
		return fmt.Errorf("%w: no trusted release key installed", signing.ErrBadKey)
	}
	return signing.VerifyDigest(s.pubKey, digest, art.Signature)
}

// drain asks the running core to stop taking new work. It is best effort: stopping the core
// still delivers SIGTERM, so a failed drain is logged and the update continues.
func (s *Supervisor) drain(ctx context.Context) {
	s.mu.Lock()
	running := s.core != nil && !s.core.exited()
	s.mu.Unlock()
	if !running {
		return
	}
	addr, err := coreHealthAddr(s.cfg.Listen)
	if err != nil {
		s.opts.Logf("drain skipped: %v", err)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, s.opts.DrainTimeout)
	defer cancel()
	if err := drainCore(dctx, &http.Client{}, addr, s.token); err != nil {
		s.opts.Logf("drain: %v (continuing)", err)
	}
}

// healthy waits for the core process p to report the target version.
func (s *Supervisor) healthy(ctx context.Context, p *coreProc, version string) error {
	addr, err := coreHealthAddr(s.cfg.Listen)
	if err != nil {
		return err
	}
	return waitHealthy(ctx, s.probe, addr, p, version, s.opts.HealthTimeout, s.opts.HealthInterval)
}

// stopTracked stops the core the supervisor is tracking. On failure the process is kept tracked,
// so nothing else is started alongside it.
func (s *Supervisor) stopTracked() error {
	p := s.takeCore()
	if err := stopCore(s.opts.Home, p, s.opts.StopTimeout); err != nil {
		s.setCore(p)
		return err
	}
	return nil
}

// failBeforeStop records a failure that happened while the old core was still running.
func (s *Supervisor) failBeforeStop(j *Journal, cause error) (UpdateRecord, error) {
	err := fmt.Errorf("update %s failed during %s: %w", j.ID, j.Phase, cause)
	return s.finish(j, StatusFailed, err)
}

// rollback restores the database, switches back to the previous version, and starts it. status is
// the outcome recorded when the rollback succeeds. If any step fails, the update is recorded as
// failed and the old core is not started on a state that could not be restored.
func (s *Supervisor) rollback(ctx context.Context, j *Journal, cause error, status string) (UpdateRecord, error) {
	ctx = context.WithoutCancel(ctx)
	h := s.opts.Home
	fail := func(err error) (UpdateRecord, error) {
		all := errors.Join(cause, err)
		return s.finish(j, StatusFailed, fmt.Errorf("rollback after %s: %w", j.Phase, all))
	}
	if err := s.stopTracked(); err != nil {
		return fail(err)
	}
	if phaseIndex(j.Phase) >= phaseIndex(PhaseMigrating) {
		if j.DBBackup == "" {
			// The migration may have run and there is no backup to restore from. The old core has no
			// check that refuses a newer schema, so starting it would be an unconditional binary rollback
			// on a migrated database. The supervisor halts instead: no core runs and the database is left
			// as it is, until an explicit successful update or a manual rollback clears the halt.
			manualErr := errors.New(noBackupReason)
			haltErr := s.halt(noBackupReason)
			return s.finish(j, StatusFailed, errors.Join(cause, manualErr, haltErr))
		}
		if err := restoreDB(h, j.DBBackup); err != nil {
			return fail(fmt.Errorf("restore db: %w", err))
		}
	}
	if phaseIndex(j.Phase) >= phaseIndex(PhaseSwitching) && currentVersion(h) != j.From {
		if err := switchCurrent(h, j.From); err != nil {
			return fail(fmt.Errorf("switch back: %w", err))
		}
	}
	p, err := s.launchCurrent(ctx)
	if err != nil {
		return fail(fmt.Errorf("start previous core: %w", err))
	}
	s.setCore(p)
	return s.finish(j, status, cause)
}

// noBackupReason is the halt reason and record error when a rollback has no database backup.
const noBackupReason = "db backup missing; manual recovery required"

// commit records a successful update, updates the rollback pointer, and clears the journal.
// The state file is written first, so a crash here is repeated safely by Recover.
func (s *Supervisor) commit(j *Journal) (UpdateRecord, error) {
	h := s.opts.Home
	if err := s.saveState(supervisorState{Previous: j.From, PreviousBackup: j.DBBackup}); err != nil {
		return UpdateRecord{}, fmt.Errorf("write state: %w", err)
	}
	rec := recordOf(j, StatusSucceeded, PhaseDone, nil)
	if err := AppendHistory(h.UpdatesLogPath(), rec); err != nil {
		return rec, err
	}
	if err := RemoveJournal(h.JournalPath()); err != nil {
		return rec, err
	}
	return rec, nil
}

// finish appends the outcome to history and removes the journal. cause is returned to the caller.
func (s *Supervisor) finish(j *Journal, status string, cause error) (UpdateRecord, error) {
	h := s.opts.Home
	rec := recordOf(j, status, j.Phase, cause)
	if err := AppendHistory(h.UpdatesLogPath(), rec); err != nil {
		return rec, errors.Join(cause, err)
	}
	if err := RemoveJournal(h.JournalPath()); err != nil {
		return rec, errors.Join(cause, err)
	}
	return rec, cause
}

func recordOf(j *Journal, status, phase string, cause error) UpdateRecord {
	rec := UpdateRecord{
		ID:         j.ID,
		From:       j.From,
		To:         j.To,
		Status:     status,
		Phase:      phase,
		StartedAt:  j.StartedAt,
		FinishedAt: time.Now().UTC(),
		DBBackup:   j.DBBackup,
	}
	if cause != nil {
		rec.Error = cause.Error()
	}
	return rec
}

// runMigrate runs the target version's `migrate` command. Exit code 3 means the migration failed.
func runMigrate(ctx context.Context, h Home, version string) error {
	ctx, cancel := context.WithTimeout(ctx, migrateTimeout)
	defer cancel()
	bin := filepath.Join(h.VersionDir(version), "bin", "rhymehub")
	cmd := exec.CommandContext(ctx, bin, "migrate")
	cmd.Env = scrubbedEnv(h)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("migrate %s: %w: %s", version, err, tailText(out))
	}
	return nil
}

// tailText returns at most the last 512 bytes of command output, for error messages.
func tailText(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		s = s[len(s)-512:]
	}
	return s
}

// switchCurrent points the current symlink at versions/<version>. A rename over the old link is
// atomic, so readers see either the old or the new version.
func switchCurrent(h Home, version string) error {
	tmp := h.Current() + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(h.VersionDir(version), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, h.Current()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(h.Root)
}

// ErrNoBackupRollback is returned by Rollback when the previous version can only be reached by
// restoring a database backup that does not exist. A successful update always runs migrate, so a
// missing backup means the database has a schema the previous binary was never checked against.
var ErrNoBackupRollback = errors.New(noBackupReason)

// Rollback switches back to the version recorded before the last successful update and restores
// the database backup taken before that update. Its data changes since then are lost. It refuses,
// and changes nothing, when that update had no database backup (ErrNoBackupRollback).
func (s *Supervisor) Rollback(ctx context.Context) (UpdateRecord, error) {
	if !s.acquire() {
		return UpdateRecord{}, ErrBusy
	}
	defer s.release()
	h := s.opts.Home
	st := readState(h)
	if st.Previous == "" {
		return UpdateRecord{}, errors.New("no previous version to roll back to")
	}
	if st.PreviousBackup == "" {
		// Refused before the core is stopped. The halt, if any, is kept.
		return UpdateRecord{}, fmt.Errorf("%w: rollback to %s refused", ErrNoBackupRollback, st.Previous)
	}
	if _, err := os.Stat(h.VersionDir(st.Previous)); err != nil {
		return UpdateRecord{}, fmt.Errorf("previous version %s is not installed: %w", st.Previous, err)
	}
	now := time.Now().UTC()
	j := &Journal{
		ID:        newID(),
		From:      currentVersion(h),
		To:        st.Previous,
		StartedAt: now,
		UpdatedAt: now,
		DBBackup:  st.PreviousBackup,
		Manual:    true,
	}

	// Journal every phase, as runUpdate does, so a crash mid-rollback is visible to Recover.
	if err := s.enter(j, PhaseStopping); err != nil {
		return UpdateRecord{}, err
	}
	if err := s.stopTracked(); err != nil {
		return s.finish(j, StatusFailed, fmt.Errorf("stop core: %w", err))
	}
	if err := s.enter(j, PhaseSwitching); err != nil {
		return UpdateRecord{}, err
	}
	if st.PreviousBackup != "" {
		if err := restoreDB(h, st.PreviousBackup); err != nil {
			return s.finish(j, StatusFailed, fmt.Errorf("restore db: %w", err))
		}
	}
	if err := switchCurrent(h, st.Previous); err != nil {
		return s.finish(j, StatusFailed, fmt.Errorf("switch: %w", err))
	}
	// The rollback pointer is consumed whether or not the previous core comes up. An operator rollback
	// is an explicit decision, so it also clears any halt.
	if err := s.saveState(supervisorState{}); err != nil {
		return s.finish(j, StatusFailed, fmt.Errorf("write state: %w", err))
	}
	if err := s.enter(j, PhaseStarting); err != nil {
		return UpdateRecord{}, err
	}
	p, err := s.launchCurrent(context.WithoutCancel(ctx))
	if err != nil {
		return s.finish(j, StatusFailed, fmt.Errorf("start previous core: %w", err))
	}
	s.setCore(p)
	return s.finish(j, StatusRolledBack, nil)
}

// Recover resolves an update journal left by an earlier supervisor run. It runs before the core
// is started and before the API is served.
//
//   - Before the symlink switch: the old version is still current. The database is restored when
//     migrate may have run, the journal is discarded, the update is recorded as failed, and the old
//     core is started.
//   - At or after the switch: if the current version is the target and it passes the health check,
//     the update is committed. Otherwise it is rolled back.
func (s *Supervisor) Recover(ctx context.Context) error {
	h := s.opts.Home
	j, err := ReadJournal(h.JournalPath())
	if err != nil {
		return fmt.Errorf("read journal: %w", err)
	}
	if j == nil {
		return nil
	}
	s.opts.Logf("recovering update %s interrupted at phase %s", j.ID, j.Phase)

	if j.Manual {
		// A manual rollback that died midway may have restored the database without switching
		// versions, or the reverse. Neither state can be recognised safely, so the operator decides.
		_, err := s.finish(j, StatusFailed, fmt.Errorf("manual rollback interrupted by supervisor restart during %s; manual recovery needed", j.Phase))
		return err
	}

	if phaseIndex(j.Phase) >= phaseIndex(PhaseSwitching) && currentVersion(h) == j.To {
		p, err := s.launchCurrent(ctx)
		if err == nil {
			s.setCore(p)
			_, err = s.commit(j)
			return err
		}
		_, err = s.rollback(ctx, j, fmt.Errorf("recovery health check: %w", err), StatusRolledBack)
		return err
	}
	cause := fmt.Errorf("interrupted by supervisor restart during %s", j.Phase)
	_, err = s.rollback(ctx, j, cause, StatusFailed)
	return err
}
