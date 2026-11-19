// Package release merges an approved task into the project's default branch and publishes a release.
//
// State flow: APPROVED -> MERGING -> (RELEASING) -> COMPLETED, or FAILED with a reason. Every step is
// idempotent: a task that is already past a step skips it, and events, audit rows and transitions carry
// idempotency keys so that a retry after a crash does not duplicate work.
package release

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/signing"
	"github.com/lafin716/rhymehub/internal/store"
)

// Failure reasons stored in tasks.failure_reason.
const (
	ReasonMergeConflict     = "merge_conflict"
	ReasonDirtyBase         = "dirty_base"
	ReasonBaseNotCheckedOut = "base_not_checked_out"
	ReasonMergeFailed       = "merge_failed"
	ReasonReleaseFailed     = "release_failed"
)

// Release strategies (projects.release_strategy).
const (
	StrategyNone    = "none"
	StrategyTag     = "tag"
	StrategyCommand = "command"
	StrategySelf    = "rhymehub-self"
)

const (
	actor             = "release-manager"
	feedName          = "rhymehub"
	feedSchemaVersion = 1
	feedFileName      = "manifest.json"
	selfBuildScript   = "scripts/build-release.sh"
	maxLogBytes       = 64 << 10
	maxTailBytes      = 4 << 10
)

// ErrFailed is wrapped by every error that means the task ended in FAILED. The task row carries the reason.
var ErrFailed = errors.New("release: task failed")

// Manifest is the release feed document (docs/RELEASE_AND_UPDATE.md §2).
type Manifest = signing.Manifest

// CommandRunner runs an external command in dir with exactly env as the child environment and returns
// its combined output. Production uses os/exec; tests inject stubs.
type CommandRunner func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

// Options configures a Manager.
type Options struct {
	Store  *store.Store
	Paths  config.Paths
	Config config.Config
	// Now returns the current time. nil means time.Now.
	Now func() time.Time
	// RunCmd runs release_cmd and scripts/build-release.sh. nil means os/exec.
	RunCmd CommandRunner
}

// Manager drives tasks through merge and release. One Manager serializes its work: merges into the same
// repository must not interleave.
type Manager struct {
	st    *store.Store
	paths config.Paths
	cfg   config.Config
	now   func() time.Time
	run   CommandRunner
	mu    sync.Mutex
}

// New returns a Manager. Store is required.
func New(opts Options) *Manager {
	m := &Manager{st: opts.Store, paths: opts.Paths, cfg: opts.Config, now: opts.Now, run: opts.RunCmd}
	if m.now == nil {
		m.now = time.Now
	}
	if m.run == nil {
		m.run = execRunner
	}
	return m
}

func execRunner(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

// failure is a task-level failure: it moves the task to FAILED with reason. Infrastructure errors (database,
// filesystem I/O) are returned as plain errors instead, so the caller can retry.
type failure struct {
	reason string
	msg    string
	log    string
	detail json.RawMessage
}

func failf(reason, format string, args ...any) *failure {
	return &failure{reason: reason, msg: fmt.Sprintf(format, args...)}
}

// MergeAndRelease merges the approved task into the project's default branch and runs the project's release
// strategy. It returns nil when the task is COMPLETED (including when it already was). When the task ends in
// FAILED it returns an error wrapping ErrFailed; the reason is in the task row and the task events.
func (m *Manager) MergeAndRelease(ctx context.Context, taskID string) error {
	return m.ReleaseTask(ctx, taskID, true)
}

// ReleaseTask implements the API Releaser contract. With release true it behaves as MergeAndRelease. With
// release false the task is merged and completed without running the project's release strategy
// (MERGING -> COMPLETED), which is how an approval with {"release": false} is honoured.
func (m *Manager) ReleaseTask(ctx context.Context, taskID string, release bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.st.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	switch t.State {
	case domain.StateCompleted:
		return nil
	case domain.StateFailed:
		return fmt.Errorf("%w: %s", ErrFailed, t.FailureReason)
	case domain.StateApproved, domain.StateMerging, domain.StateReleasing:
	default:
		return fmt.Errorf("%w: task %s is %s, expected APPROVED", domain.ErrStateConflict, taskID, t.State)
	}
	p, err := m.st.GetProject(ctx, t.ProjectID)
	if err != nil {
		return err
	}
	if t.State == domain.StateApproved {
		t, err = m.st.TransitionTask(ctx, taskID, []domain.TaskState{domain.StateApproved}, domain.StateMerging,
			&store.EventInput{Reason: "merge started", IdemKey: "release:merging:" + taskID})
		if err != nil {
			return err
		}
	}
	if t.State == domain.StateMerging {
		if release {
			if f := m.preflight(p); f != nil {
				return m.fail(ctx, taskID, domain.StateMerging, f)
			}
		}
		_, f, err := m.mergeTask(ctx, t, p)
		if err != nil {
			return err
		}
		if f != nil {
			return m.fail(ctx, taskID, domain.StateMerging, f)
		}
		next, key := domain.StateReleasing, "release:releasing:"+taskID
		if p.ReleaseStrategy == StrategyNone || !release {
			next, key = domain.StateCompleted, "release:completed:"+taskID
		}
		t, err = m.st.TransitionTask(ctx, taskID, []domain.TaskState{domain.StateMerging}, next,
			&store.EventInput{Reason: "merged", IdemKey: key})
		if err != nil {
			return err
		}
	}
	switch t.State {
	case domain.StateCompleted:
		return nil
	case domain.StateReleasing:
		return m.releaseTask(ctx, t, p)
	}
	return fmt.Errorf("%w: task %s is %s after merge", domain.ErrStateConflict, taskID, t.State)
}

// releaseTask runs the release strategy for a task in RELEASING and completes or fails the task.
func (m *Manager) releaseTask(ctx context.Context, t domain.Task, p domain.Project) error {
	rel, found, err := m.findRelease(ctx, p.ID, t.ID)
	if err != nil {
		return err
	}
	if found && rel.Status == domain.ReleasePublished {
		return m.complete(ctx, t.ID, rel)
	}
	if !found {
		ct, err := m.changeType(ctx, t)
		if err != nil {
			return err
		}
		ver, err := m.nextVersionFor(ctx, p, ct)
		if err != nil {
			return err
		}
		rel, err = m.st.CreateRelease(ctx, domain.Release{ProjectID: p.ID, Version: ver, Tag: "v" + ver,
			Strategy: p.ReleaseStrategy, TaskIDs: []string{t.ID}, Status: domain.ReleaseBuilding, CreatedBy: actor})
		if err != nil {
			return err
		}
	}

	out, f, err := m.runStrategy(ctx, p, t, rel)
	if err != nil {
		return err
	}
	if f != nil {
		rel.Status = domain.ReleaseFailed
		rel.Log = f.log
		if _, err := m.st.UpdateRelease(ctx, rel); err != nil {
			return err
		}
		return m.fail(ctx, t.ID, domain.StateReleasing, f)
	}

	rel.Status = domain.ReleasePublished
	rel.Tag = out.tag
	rel.Notes = out.notes
	rel.Log = out.log
	rel.Artifacts = out.artifacts
	if rel, err = m.st.UpdateRelease(ctx, rel); err != nil {
		return err
	}
	if out.log != "" {
		if _, err := m.st.AppendTaskEvent(ctx, t.ID, store.NewEvent{Type: domain.EventLog,
			Payload: mustJSON(map[string]any{"level": "info", "message": tail(out.log)}),
			IdemKey: "release-log:" + rel.ID}); err != nil {
			return err
		}
	}
	if err := m.audit(ctx, "release.publish", "release", rel.ID, map[string]any{
		"version": rel.Version, "strategy": rel.Strategy, "taskId": t.ID,
	}); err != nil {
		return err
	}
	return m.complete(ctx, t.ID, rel)
}

// complete links the release to the task, records the release event and moves the task to COMPLETED.
func (m *Manager) complete(ctx context.Context, taskID string, rel domain.Release) error {
	if _, err := m.st.MutateTask(ctx, taskID, func(t *domain.Task) error {
		t.ReleaseID = rel.ID
		return nil
	}); err != nil {
		return err
	}
	if _, err := m.st.AppendTaskEvent(ctx, taskID, store.NewEvent{Type: domain.EventRelease,
		Payload: mustJSON(map[string]any{"release_id": rel.ID, "version": rel.Version,
			"status": string(domain.ReleasePublished)}),
		IdemKey: "release:" + rel.ID + ":published"}); err != nil {
		return err
	}
	_, err := m.st.TransitionTask(ctx, taskID, []domain.TaskState{domain.StateReleasing}, domain.StateCompleted,
		&store.EventInput{Reason: "released " + rel.Version, IdemKey: "release:completed:" + taskID})
	return err
}

// fail records a task-level failure: reason and message on the task, a log event, a FAILED transition and an
// audit row. It always returns an error wrapping ErrFailed.
func (m *Manager) fail(ctx context.Context, taskID string, from domain.TaskState, f *failure) error {
	if _, err := m.st.MutateTask(ctx, taskID, func(t *domain.Task) error {
		t.FailureReason = f.reason
		t.Error = f.msg
		return nil
	}); err != nil {
		return err
	}
	if _, err := m.st.AppendTaskEvent(ctx, taskID, store.NewEvent{Type: domain.EventLog,
		Payload: mustJSON(map[string]any{"level": "error", "message": f.msg}),
		IdemKey: "release:failed-log:" + taskID}); err != nil {
		return err
	}
	if _, err := m.st.TransitionTask(ctx, taskID, []domain.TaskState{from}, domain.StateFailed,
		&store.EventInput{Reason: f.reason, IdemKey: "release:failed:" + taskID, Detail: f.detail}); err != nil {
		return err
	}
	if err := m.audit(ctx, "release.fail", "task", taskID, map[string]any{"reason": f.reason, "message": f.msg}); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s: %s", ErrFailed, f.reason, f.msg)
}

// preflight checks the release configuration before anything is merged, so a task that cannot be released is
// failed without touching the base branch.
func (m *Manager) preflight(p domain.Project) *failure {
	switch p.ReleaseStrategy {
	case StrategyNone, StrategyTag:
		return nil
	case StrategyCommand:
		if strings.TrimSpace(p.ReleaseCmd) == "" {
			return failf(ReasonReleaseFailed, "release strategy command has no release_cmd configured")
		}
	case StrategySelf:
		if _, f := m.loadSigningKey(); f != nil {
			return f
		}
		if _, err := os.Stat(filepath.Join(p.RepoPath, filepath.FromSlash(selfBuildScript))); err != nil {
			return failf(ReasonReleaseFailed, "%s not found in %s", selfBuildScript, p.RepoPath)
		}
	default:
		return failf(ReasonReleaseFailed, "unknown release strategy %q", p.ReleaseStrategy)
	}
	return nil
}

// loadSigningKey reads the release signing key from the secrets directory. Only the core process reads it;
// agents are denied access to secrets/.
func (m *Manager) loadSigningKey() (ed25519.PrivateKey, *failure) {
	priv, err := signing.LoadPrivateKey(filepath.Join(m.paths.SecretsDir(), signing.PrivateKeyFileName))
	if err != nil {
		return nil, failf(ReasonReleaseFailed, "release signing key unavailable: %v", err)
	}
	return priv, nil
}

// findRelease returns the release that already includes the task, if any.
func (m *Manager) findRelease(ctx context.Context, projectID, taskID string) (domain.Release, bool, error) {
	rels, err := m.st.ListReleases(ctx, projectID)
	if err != nil {
		return domain.Release{}, false, err
	}
	for _, r := range rels {
		if slices.Contains(r.TaskIDs, taskID) {
			return r, true, nil
		}
	}
	return domain.Release{}, false, nil
}

// changeType returns the request type of the task ("" when the task has no request).
func (m *Manager) changeType(ctx context.Context, t domain.Task) (string, error) {
	if t.RequestID == "" {
		return "", nil
	}
	req, err := m.st.GetRequest(ctx, t.RequestID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(req.Type), nil
}

// nextVersionFor computes the next version from the git tags and the existing release rows of the project.
// Release rows count too, because a command or rhymehub-self release does not create a git tag.
func (m *Manager) nextVersionFor(ctx context.Context, p domain.Project, changeType string) (string, error) {
	latest, found, err := latestTagVersion(ctx, p.RepoPath)
	if err != nil {
		return "", err
	}
	rels, err := m.st.ListReleases(ctx, p.ID)
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		v, ok := parseVersion(r.Version)
		if !ok {
			continue
		}
		if !found || latest.less(v) {
			latest, found = v, true
		}
	}
	return bump(latest, found, changeType).String(), nil
}

// NextVersion returns the next release version (without a "v" prefix) for repo, computed from its v* tags.
// changeType "feature" bumps the minor version; anything else bumps the patch. With no tags the first version is 0.1.0.
func (m *Manager) NextVersion(ctx context.Context, repo string, changeType string) (string, error) {
	latest, found, err := latestTagVersion(ctx, repo)
	if err != nil {
		return "", err
	}
	return bump(latest, found, changeType).String(), nil
}

// LatestFeed returns the local release feed (releases/manifest.json). A missing feed is an empty manifest named
// rhymehub, not an error.
func (m *Manager) LatestFeed(ctx context.Context) (Manifest, error) {
	man, err := signing.LoadManifest(m.feedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{Name: feedName}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	return *man, nil
}

// ListReleases returns the releases of a project, newest first. An empty projectID lists all projects.
func (m *Manager) ListReleases(ctx context.Context, projectID string) ([]domain.Release, error) {
	return m.st.ListReleases(ctx, projectID)
}

func (m *Manager) feedPath() string {
	return filepath.Join(m.paths.ReleasesDir(), feedFileName)
}

// audit appends one audit row. Actor is the release manager (a system actor, not a user).
func (m *Manager) audit(ctx context.Context, action, targetType, targetID string, detail any) error {
	_, err := m.st.AppendAudit(ctx, domain.AuditEntry{At: m.now(), Actor: actor, ActorRole: "system",
		Action: action, TargetType: targetType, TargetID: targetID, Detail: mustJSON(detail)})
	return err
}

// scrubbedEnv is the complete environment for release commands and the build script. It contains only the
// variables a build needs plus the version. RHYMEHUB_HOME, tokens and every other variable are not passed.
func scrubbedEnv(version string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "VERSION=" + version, "RHYMEHUB_VERSION=" + version}
	for _, k := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func truncate(s string) string {
	if len(s) <= maxLogBytes {
		return s
	}
	return s[:maxLogBytes] + "\n...[truncated]"
}

func tail(s string) string {
	if len(s) <= maxTailBytes {
		return s
	}
	return "...[truncated]\n" + s[len(s)-maxTailBytes:]
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
