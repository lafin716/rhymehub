package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lafin716/rhymehub/internal/domain"
)

// ErrNotConfigured is returned by the default collaborators when no implementation was injected.
var ErrNotConfigured = errors.New("not configured")

// Releaser finishes an approved task. The task is already in MERGING when this is called (the approve
// handler performs APPROVED -> MERGING). The implementation merges the branch and, when release is true,
// runs the release (MERGING -> RELEASING -> COMPLETED). When release is false it stops at COMPLETED
// (MERGING -> COMPLETED). On error the server moves a task that is still MERGING to FAILED.
// The release manager is owned by another component; the server only depends on this interface. Calls are
// made asynchronously after approval, so implementations must not assume the HTTP request is still open.
type Releaser interface {
	ReleaseTask(ctx context.Context, taskID string, release bool) error
}

// Canceller stops the worker process of a running task. It is called after a RUNNING, PREPARING or
// VALIDATING task was moved to CANCELLED. Nil means no process control is available.
type Canceller interface {
	CancelTask(ctx context.Context, taskID string) error
}

// Supervisor forwards self-update operations to the supervisor (127.0.0.1:7781, config.SupervisorAddr).
type Supervisor interface {
	// Update asks the supervisor to install the given version.
	Update(ctx context.Context, version string) error
	// Rollback asks the supervisor to roll back the update identified by updateID.
	Rollback(ctx context.Context, updateID string) error
}

type unconfiguredReleaser struct{}

func (unconfiguredReleaser) ReleaseTask(context.Context, string, bool) error { return ErrNotConfigured }

// ---- CORE-2 integration contracts --------------------------------------------------------------------
// Implementations live outside this package and are injected through Options by `rhymehub serve`.
// Every field is optional: a nil collaborator makes its endpoints answer 503 {code:"internal",
// message:"<feature> is not configured"} (or a documented degraded payload), never panic.

// AgentLimits mirrors scheduler.Limits for the API (docs/API_SPEC.md Agents). PerAgent overrides Default.
type AgentLimits struct {
	Global          int            `json:"global"`
	DefaultPerAgent int            `json:"defaultPerAgent"`
	PerAgent        map[string]int `json:"perAgent"`
}

// SchedulerControl is the scheduler surface the API needs (drain, worker count, limits).
type SchedulerControl interface {
	Drain(on bool)
	Draining() bool
	ActiveWorkers() int
	CurrentLimits() AgentLimits
	SetLimits(AgentLimits)
}

// AgentInfo is one entry of GET /agents (without running/limit, which the handler fills from the store and limits).
type AgentInfo struct {
	Name         string            `json:"name"`
	Available    bool              `json:"available"`
	Version      string            `json:"version"`
	Path         string            `json:"path"`
	Capabilities AgentCapabilities `json:"capabilities"`
	Error        string            `json:"error,omitempty"`
}

// AgentCapabilities is the honest capability matrix of an adapter.
type AgentCapabilities struct {
	Streaming           bool     `json:"streaming"`
	Resume              bool     `json:"resume"`
	MultiTurn           bool     `json:"multiTurn"`
	PermissionRequests  bool     `json:"permissionRequests"`
	InteractiveApproval bool     `json:"interactiveApproval"`
	Cancel              bool     `json:"cancel"`
	CostReporting       bool     `json:"costReporting"`
	Models              []string `json:"models"`
}

// AgentDirectory detects installed agent CLIs. Implementations should cache detection (it shells out to
// `<cli> --version`) and must never read agent credential stores.
type AgentDirectory interface {
	ListAgents(ctx context.Context) []AgentInfo
}

// Notifier is the notification service surface (push + in-app feed producer).
type Notifier interface {
	// PublicKey is the VAPID application server key (base64url) for PushManager.subscribe.
	PublicKey() string
	// Enqueue stores a notification (deduped by eventKey) and schedules push delivery.
	Enqueue(ctx context.Context, kind, eventKey, title, body, url, projectID, taskID string) (domain.Notification, bool, error)
}

// SupervisorStatus is the supervisor's GET /status as seen by the core.
type SupervisorStatus struct {
	Reachable        bool   `json:"reachable"`
	Version          string `json:"version"`
	CurrentVersion   string `json:"currentVersion"`
	UpdateInProgress bool   `json:"updateInProgress"`
}

// SupervisorInfo is optionally implemented by the Supervisor collaborator to report status and history.
// UpdateHistory returns supervisor UpdateRecords as raw JSON objects (shape owned by services/supervisor).
type SupervisorInfo interface {
	Status(ctx context.Context) SupervisorStatus
	UpdateHistory(ctx context.Context) ([]json.RawMessage, error)
}

// ProjectReleaser cuts a manual release of a project's default branch HEAD (POST /projects/{id}/releases).
// version may be empty (next version is computed); notes may be empty (generated from git log).
type ProjectReleaser interface {
	ReleaseProject(ctx context.Context, projectID, version, notes, actorUserID string) (domain.Release, error)
}
